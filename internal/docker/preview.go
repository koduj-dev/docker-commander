package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Comparing a project's resolved compose against what is actually running.
//
// `docker compose up` is the moment a change becomes real, and until now the
// only way to learn what it would do was to do it. This turns that into a
// question that can be asked first: which services appear, which disappear, and
// which change image.
//
// It compares against the CONTAINERS currently running under the project label
// rather than against a stored copy of the last deploy. That is deliberate: a
// stored copy tells you what someone last asked for, while the containers tell
// you what is actually there — and those differ precisely when it matters, after
// a manual `docker rm` or a deploy that half-failed.

// ServiceSpec is one service as compose resolves it, or (when Detailed) as a
// running container actually has it — see deployfields.go for the extended
// fields and how the running side is built (LiveServiceSpec).
type ServiceSpec struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`

	// Detailed is true once the fields below have actually been populated.
	// RunningServices below leaves it false (image only, no extra Docker API
	// calls) — BuildDeployPreview's extended comparisons only run when BOTH
	// sides are Detailed, so a caller that hasn't paid for LiveServiceSpec
	// gets exactly the old add/removed/image-only behaviour.
	Detailed bool `json:"-"`

	Env         map[string]string `json:"env,omitempty"`
	Ports       []ServicePort     `json:"ports,omitempty"`
	Volumes     []VolumeSpec      `json:"volumes,omitempty"`
	Networks    []string          `json:"networks,omitempty"`
	Restart     string            `json:"restart,omitempty"`
	CPULimit    float64           `json:"cpuLimit,omitempty"`
	MemoryLimit int64             `json:"memoryLimit,omitempty"`
	Healthcheck *HealthcheckSpec  `json:"healthcheck,omitempty"`
}

// ServiceChange is one difference between the resolved config and reality.
type ServiceChange struct {
	Service string `json:"service"`
	// Kind is "added" (would be created), "removed" (running but no longer in
	// the file), "image" or "digest" (same service, different image), or one
	// of the extended-comparison kinds from ExtendServiceComparison: "env",
	// "ports", "volumes", "networks", "restart" or "resources"/"healthcheck".
	Kind     string `json:"kind"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Existing bool   `json:"existing"`
	// Recreates flags that applying this change means the running container
	// is destroyed and recreated (the downtime-risk callout) — true for
	// everything except "added" (a brand-new container) and "removed" (an
	// orphan a deploy leaves running, untouched).
	Recreates bool `json:"recreates"`
}

// DeployPreview is what a deploy would change.
type DeployPreview struct {
	Services  []ServiceSpec   `json:"services"`
	Running   []ServiceSpec   `json:"running"`
	Changes   []ServiceChange `json:"changes"`
	Unchanged int             `json:"unchanged"`
}

// ParseComposeServices pulls the full service list out of `docker compose
// config --format json` — image plus env/ports/volumes/networks/restart/
// resources/healthcheck (see deployfields.go).
func ParseComposeServices(configJSON []byte) ([]ServiceSpec, error) {
	cfg, err := parseComposeConfigDoc(configJSON)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceSpec, 0, len(cfg.Services))
	for name := range cfg.Services {
		out = append(out, serviceSpecFromComposeDoc(cfg, name))
	}
	// Stable order: map iteration is randomised, and a preview that reshuffles
	// between calls is impossible to read as a diff.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// BuildDeployPreview compares the resolved services against what is running.
//
// A service present in both with the same image is counted as unchanged rather
// than listed: the useful output is what MOVES, and a project with thirty stable
// services should not bury its one change in a wall of them.
func BuildDeployPreview(resolved, running []ServiceSpec) DeployPreview {
	p := DeployPreview{Services: resolved, Running: running, Changes: []ServiceChange{}}

	runningBy := map[string]ServiceSpec{}
	for _, r := range running {
		runningBy[r.Name] = r
	}
	resolvedBy := map[string]ServiceSpec{}
	for _, r := range resolved {
		resolvedBy[r.Name] = r
	}

	for _, want := range resolved {
		have, ok := runningBy[want.Name]
		if !ok {
			p.Changes = append(p.Changes, ServiceChange{
				Service: want.Name, Kind: "added", To: want.Image,
				Detail: "not running; a deploy would create it",
			})
			continue
		}
		// An image comparison is only meaningful when both sides name one. A
		// service that builds its image locally has no image in the resolved
		// config until it is built, and reporting that as a change every time
		// would make the preview cry wolf.
		if want.Image != "" && have.Image != "" && want.Image != have.Image {
			p.Changes = append(p.Changes, ServiceChange{
				Service: want.Name, Kind: "image", From: have.Image, To: want.Image,
				Detail: "running a different image; a deploy would recreate it", Existing: true, Recreates: true,
			})
			continue
		}
		p.Unchanged++
	}

	for _, have := range running {
		if _, ok := resolvedBy[have.Name]; ok {
			continue
		}
		// Compose does not remove these unless --remove-orphans is passed, and it
		// is not — so this is "will be left behind", not "will be deleted". Saying
		// it the other way round would be alarming and wrong.
		p.Changes = append(p.Changes, ServiceChange{
			Service: have.Name, Kind: "removed", From: have.Image, Existing: true,
			Detail: "running but no longer in the compose file; a deploy leaves it running (orphans are not removed)",
		})
	}

	sort.Slice(p.Changes, func(i, j int) bool {
		if p.Changes[i].Kind != p.Changes[j].Kind {
			return p.Changes[i].Kind < p.Changes[j].Kind
		}
		return p.Changes[i].Service < p.Changes[j].Service
	})
	return p
}

// AugmentDigestDrift checks, for each service BuildDeployPreview judged
// unchanged, whether its image tag now resolves to a different manifest
// digest on its registry than what is actually running — the "mutable tag"
// case a name/image string comparison can't see (the tag reads the same, but
// `docker compose up` would still recreate the container). Mutates prev in
// place, appending a "digest" change per service found to have drifted and
// correcting Unchanged to match.
//
// Best-effort by design: any lookup failure (registry unreachable, host not
// configured, image never pulled from a registry) just skips that service —
// this only ever informs a preview, never blocks one.
func (m *Manager) AugmentDigestDrift(ctx context.Context, hostID int64, prev *DeployPreview, containers []StackContainer) {
	containerByService := map[string]string{}
	for _, c := range containers {
		if c.Service != "" {
			containerByService[c.Service] = c.ID
		}
	}
	changed := map[string]bool{}
	for _, ch := range prev.Changes {
		changed[ch.Service] = true
	}

	for _, svc := range prev.Services {
		if svc.Image == "" || changed[svc.Name] {
			continue
		}
		cid, ok := containerByService[svc.Name]
		if !ok {
			continue
		}
		remote, err := m.ResolveImageDigest(ctx, svc.Image)
		if err != nil || remote == "" {
			continue
		}
		local, err := m.RunningImageDigest(ctx, hostID, cid, svc.Image)
		if err != nil || local == "" || local == remote {
			continue
		}
		prev.Changes = append(prev.Changes, ServiceChange{
			Service: svc.Name, Kind: "digest", From: local, To: remote, Existing: true, Recreates: true,
			Detail: "same tag now resolves to a different image digest; a deploy would recreate it",
		})
		prev.Unchanged--
	}

	sort.Slice(prev.Changes, func(i, j int) bool {
		if prev.Changes[i].Kind != prev.Changes[j].Kind {
			return prev.Changes[i].Kind < prev.Changes[j].Kind
		}
		return prev.Changes[i].Service < prev.Changes[j].Service
	})
}

// ExtendServiceComparison adds env/port/volume/network/restart/resource/
// healthcheck comparisons on top of what BuildDeployPreview already found,
// for services on both sides that carry full detail (see LiveServiceSpec in
// deployfields.go) and that BuildDeployPreview/AugmentDigestDrift didn't
// already flag — any of those already implies a recreate, so a field-level
// diff on top of one would just be noise.
//
// Every comparison here is deliberately one-directional: it only flags a
// field the compose file actually DECLARES and disagrees with reality. A
// field compose is silent on (no env override, no resource limit, no
// healthcheck) is never compared — there is no stored record of which of a
// running container's current settings were ever compose-managed (closing
// that gap is exactly what the planned revision store is for, see NEXT.md),
// so silence must read as "not judged", never as "removed".
func ExtendServiceComparison(prev *DeployPreview, resolved, running []ServiceSpec) {
	runningBy := map[string]ServiceSpec{}
	for _, r := range running {
		runningBy[r.Name] = r
	}
	flagged := map[string]bool{}
	for _, ch := range prev.Changes {
		flagged[ch.Service] = true
	}

	for _, want := range resolved {
		if flagged[want.Name] || !want.Detailed {
			continue
		}
		have, ok := runningBy[want.Name]
		if !ok || !have.Detailed {
			continue
		}
		before := len(prev.Changes)

		if d := envDiff(want.Env, have.Env); d != "" {
			prev.Changes = append(prev.Changes, ServiceChange{
				Service: want.Name, Kind: "env", Detail: d, Existing: true, Recreates: true,
			})
		}
		if len(want.Ports) > 0 && !portsEqual(want.Ports, have.Ports) {
			prev.Changes = append(prev.Changes, ServiceChange{
				Service: want.Name, Kind: "ports", Existing: true, Recreates: true,
				From: portsString(have.Ports), To: portsString(want.Ports),
			})
		}
		if len(want.Volumes) > 0 && !volumesEqual(want.Volumes, have.Volumes) {
			prev.Changes = append(prev.Changes, ServiceChange{
				Service: want.Name, Kind: "volumes", Existing: true, Recreates: true,
				From: volumesString(have.Volumes), To: volumesString(want.Volumes),
			})
		}
		if len(want.Networks) > 0 && !stringSlicesEqual(want.Networks, have.Networks) {
			prev.Changes = append(prev.Changes, ServiceChange{
				Service: want.Name, Kind: "networks", Existing: true, Recreates: true,
				From: strings.Join(have.Networks, ", "), To: strings.Join(want.Networks, ", "),
			})
		}
		if want.Restart != "" && want.Restart != have.Restart {
			prev.Changes = append(prev.Changes, ServiceChange{
				Service: want.Name, Kind: "restart", Existing: true, Recreates: true,
				From: have.Restart, To: want.Restart,
			})
		}
		if d := resourcesDiff(want, have); d != "" {
			prev.Changes = append(prev.Changes, ServiceChange{
				Service: want.Name, Kind: "resources", Detail: d, Existing: true, Recreates: true,
			})
		}
		if d := healthcheckDiff(want.Healthcheck, have.Healthcheck); d != "" {
			prev.Changes = append(prev.Changes, ServiceChange{
				Service: want.Name, Kind: "healthcheck", Detail: d, Existing: true, Recreates: true,
			})
		}

		if len(prev.Changes) > before {
			prev.Unchanged--
		}
	}

	sort.Slice(prev.Changes, func(i, j int) bool {
		if prev.Changes[i].Kind != prev.Changes[j].Kind {
			return prev.Changes[i].Kind < prev.Changes[j].Kind
		}
		return prev.Changes[i].Service < prev.Changes[j].Service
	})
}

// envDiff reports keys the compose file declares that are missing or
// different on the running side. Never reports a key the file is silent on,
// even if the container has it — see ExtendServiceComparison's doc comment.
// Only key names are reported, never values (compose env can carry secrets).
func envDiff(want, have map[string]string) string {
	var added, changed []string
	for k, wv := range want {
		hv, ok := have[k]
		if !ok {
			added = append(added, k)
		} else if hv != wv {
			changed = append(changed, k)
		}
	}
	if len(added) == 0 && len(changed) == 0 {
		return ""
	}
	sort.Strings(added)
	sort.Strings(changed)
	var parts []string
	if len(added) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing (%s)", len(added), strings.Join(added, ", ")))
	}
	if len(changed) > 0 {
		parts = append(parts, fmt.Sprintf("%d changed (%s)", len(changed), strings.Join(changed, ", ")))
	}
	return strings.Join(parts, ", ")
}

func portsEqual(a, b []ServicePort) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func volumesEqual(a, b []VolumeSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func portsString(p []ServicePort) string {
	if len(p) == 0 {
		return "none"
	}
	parts := make([]string, len(p))
	for i, x := range p {
		parts[i] = fmt.Sprintf("%s:%d/%s", x.Published, x.Target, x.Protocol)
	}
	return strings.Join(parts, ", ")
}

func volumesString(v []VolumeSpec) string {
	if len(v) == 0 {
		return "none"
	}
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = x.Target
	}
	return strings.Join(parts, ", ")
}

// resourcesDiff compares cpu/memory limits, but only when BOTH sides have one
// set — compose being silent on a limit doesn't mean "no limit", it means
// this field isn't managed, so nothing to compare (same stance as envDiff).
func resourcesDiff(want, have ServiceSpec) string {
	var parts []string
	if want.CPULimit > 0 && have.CPULimit > 0 && want.CPULimit != have.CPULimit {
		parts = append(parts, fmt.Sprintf("cpu %.2f → %.2f", have.CPULimit, want.CPULimit))
	}
	if want.MemoryLimit > 0 && have.MemoryLimit > 0 && want.MemoryLimit != have.MemoryLimit {
		parts = append(parts, fmt.Sprintf("memory %s → %s", humanBytes(have.MemoryLimit), humanBytes(want.MemoryLimit)))
	}
	return strings.Join(parts, ", ")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// healthcheckDiff only compares when compose actually declares a healthcheck
// (want != nil) — an image-baked HEALTHCHECK compose says nothing about is
// exactly the "field compose doesn't manage" case, not a difference to flag.
func healthcheckDiff(want, have *HealthcheckSpec) string {
	if want == nil {
		return ""
	}
	if have == nil {
		return "declared in compose but not currently running any healthcheck"
	}
	if !stringSlicesEqual(want.Test, have.Test) {
		return "test command changed"
	}
	if want.Interval > 0 && want.Interval != have.Interval {
		return fmt.Sprintf("interval %s → %s", have.Interval, want.Interval)
	}
	if want.Timeout > 0 && want.Timeout != have.Timeout {
		return fmt.Sprintf("timeout %s → %s", have.Timeout, want.Timeout)
	}
	if want.Retries > 0 && want.Retries != have.Retries {
		return fmt.Sprintf("retries %d → %d", have.Retries, want.Retries)
	}
	return ""
}

// RunningServices reduces a stack's containers to one entry per compose service.
func RunningServices(st *Stack) []ServiceSpec {
	seen := map[string]bool{}
	out := []ServiceSpec{}
	for _, c := range st.Containers {
		name := c.Service
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, ServiceSpec{Name: name, Image: c.Image})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
