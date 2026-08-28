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
	// Ignored marks a change a human has reviewed and deliberately accepted as
	// ongoing drift (see MarkIgnoredChanges) — still reported, so it stays
	// visible and reversible, but excluded from ActiveChanges.
	Ignored bool `json:"ignored"`
}

// MarkIgnoredChanges flags each change matching a (service, kind) a caller
// has recorded as reviewed drift (store.ProjectDriftIgnore) — it never
// removes a change from the list, only marks it, so an ignored drift stays
// visible (and un-ignorable) rather than silently disappearing.
func MarkIgnoredChanges(changes []ServiceChange, ignored map[[2]string]bool) {
	for i := range changes {
		if ignored[[2]string{changes[i].Service, changes[i].Kind}] {
			changes[i].Ignored = true
		}
	}
}

// ActiveChanges counts changes NOT marked Ignored — what should actually
// read as "still needs attention" after reviewed drift is excluded.
func ActiveChanges(changes []ServiceChange) int {
	n := 0
	for _, c := range changes {
		if !c.Ignored {
			n++
		}
	}
	return n
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
			Detail: svc.Image + " now resolves to a different image digest; a deploy would recreate it",
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
// different on the running side, with their values. Never reports a key the
// file is silent on, even if the container has it — see
// ExtendServiceComparison's doc comment.
//
// Values are shown, not redacted: the compose file's raw env values are
// already visible in the same project — the file editor right next to this
// preview, and the existing "Resolved" config preview — at the exact same
// permission level as this screen, so hiding them here protects nothing
// while making the one place meant to answer "what actually changed"
// useless for the question it exists to answer. If a dedicated secrets
// store ships (NEXT.md), redaction belongs there, keyed to what's actually
// a secret — not applied blanket to every env var everywhere it's shown.
func envDiff(want, have map[string]string) string {
	var added, changed []string
	for k, wv := range want {
		hv, ok := have[k]
		if !ok {
			added = append(added, fmt.Sprintf("%s=%s", k, wv))
		} else if hv != wv {
			changed = append(changed, fmt.Sprintf("%s: %q → %q", k, hv, wv))
		}
	}
	if len(added) == 0 && len(changed) == 0 {
		return ""
	}
	sort.Strings(added)
	sort.Strings(changed)
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "missing "+strings.Join(added, ", "))
	}
	if len(changed) > 0 {
		parts = append(parts, "changed "+strings.Join(changed, ", "))
	}
	return strings.Join(parts, "; ")
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

// volumesString renders source:target, not target alone — what actually
// differs between two otherwise-similar mounts is almost always the source
// (a different bind path, a different named volume), and a target-only
// rendering would show the identical string on both sides of a real change.
func volumesString(v []VolumeSpec) string {
	if len(v) == 0 {
		return "none"
	}
	parts := make([]string, len(v))
	for i, x := range v {
		src := x.Source
		if src == "" {
			src = x.Type
		}
		parts[i] = src + ":" + x.Target
	}
	return strings.Join(parts, ", ")
}

// resourcesDiff compares cpu/memory limits, gated only on the COMPOSE side
// declaring one (want > 0) — unlike env/healthcheck, "no limit" (0) on the
// running side is never ambiguous (it's Docker's own well-defined default,
// not "some value compose doesn't know about"), so a running container with
// no limit at all is a real, reportable difference from a compose file that
// now wants one. Compose staying silent (want == 0) is still never compared,
// same stance as envDiff/healthcheckDiff.
func resourcesDiff(want, have ServiceSpec) string {
	var parts []string
	if want.CPULimit > 0 && want.CPULimit != have.CPULimit {
		parts = append(parts, fmt.Sprintf("cpu %s → %.2f", cpuLabel(have.CPULimit), want.CPULimit))
	}
	if want.MemoryLimit > 0 && want.MemoryLimit != have.MemoryLimit {
		parts = append(parts, fmt.Sprintf("memory %s → %s", memLabel(have.MemoryLimit), humanBytes(want.MemoryLimit)))
	}
	return strings.Join(parts, ", ")
}

func cpuLabel(v float64) string {
	if v <= 0 {
		return "none"
	}
	return fmt.Sprintf("%.2f", v)
}

func memLabel(v int64) string {
	if v <= 0 {
		return "none"
	}
	return humanBytes(v)
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
