package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/mount"
)

// The rest of what a deploy would change, beyond "which service, which
// image" (preview.go): environment, published ports, mounts, networks,
// restart policy, resource limits and healthcheck. Parsed once from the same
// `compose config --format json` preview.go already fetches (free — no extra
// call), and, for the running side, from a container inspect per service
// (one Docker API round trip each — not free, so this is opt-in via
// LiveServiceSpec rather than folded into the cheap RunningServices).

// ServicePort is one published port mapping.
type ServicePort struct {
	Target    int    `json:"target"`
	Published string `json:"published,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
}

// VolumeSpec is one mount. Source is the compose-declared volume name (not
// project-prefixed) for a named volume, or the absolute host path for a bind.
type VolumeSpec struct {
	Type   string `json:"type"`
	Source string `json:"source,omitempty"`
	Target string `json:"target"`
}

// HealthcheckSpec is a service's healthcheck, normalised to comparable units
// (compose gives durations as strings like "30s"; a live container gives
// nanoseconds — both land here as time.Duration).
type HealthcheckSpec struct {
	Test     []string      `json:"test,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
	Retries  int           `json:"retries,omitempty"`
}

func sortPorts(p []ServicePort) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Target != p[j].Target {
			return p[i].Target < p[j].Target
		}
		if p[i].Protocol != p[j].Protocol {
			return p[i].Protocol < p[j].Protocol
		}
		return p[i].Published < p[j].Published
	})
}

func sortVolumes(v []VolumeSpec) {
	sort.Slice(v, func(i, j int) bool { return v[i].Target < v[j].Target })
}

// flexString decodes a JSON string or number into a string. compose config
// has varied across versions on whether a published port is a string or a
// number; this accepts either rather than failing the whole preview on it.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		*f = flexString(t)
	case float64:
		*f = flexString(strconv.FormatFloat(t, 'f', -1, 64))
	case nil:
		*f = ""
	default:
		*f = flexString(fmt.Sprint(t))
	}
	return nil
}

// flexNumber decodes a JSON string or number into a float64, best-effort — an
// unparseable value is left at zero rather than failing the whole preview.
type flexNumber float64

func (f *flexNumber) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case float64:
		*f = flexNumber(t)
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			*f = flexNumber(n)
		}
	}
	return nil
}

// flexBytes decodes a byte count that may arrive as a plain number, a numeric
// string, or a size string with a b/k/m/g suffix (compose accepts all three
// for deploy.resources.limits.memory depending on version).
type flexBytes int64

func (f *flexBytes) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case float64:
		*f = flexBytes(int64(t))
	case string:
		if n, ok := parseByteSize(t); ok {
			*f = flexBytes(n)
		}
	}
	return nil
}

// parseByteSize parses a plain integer byte count or a size with a
// b/k/m/g(/kb/mb/gb) suffix (case-insensitive). Returns ok=false, leaving the
// caller's value untouched, on anything it doesn't recognise.
func parseByteSize(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{
		"": 1, "b": 1,
		"k": 1 << 10, "kb": 1 << 10,
		"m": 1 << 20, "mb": 1 << 20,
		"g": 1 << 30, "gb": 1 << 30,
	}
	m, ok := mult[strings.ToLower(strings.TrimSpace(s[i:]))]
	if !ok {
		return 0, false
	}
	return int64(n * m), true
}

// composeConfigDoc is the slice of `compose config --format json` this
// package actually reads. Everything else in the real output is ignored.
type composeConfigDoc struct {
	Networks map[string]struct {
		Name string `json:"name"`
	} `json:"networks"`
	Volumes map[string]struct {
		Name string `json:"name"`
	} `json:"volumes"`
	Services map[string]struct {
		Image       string            `json:"image"`
		Environment map[string]string `json:"environment"`
		Ports       []struct {
			Target    int        `json:"target"`
			Published flexString `json:"published"`
			Protocol  string     `json:"protocol"`
		} `json:"ports"`
		Volumes []struct {
			Type   string `json:"type"`
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"volumes"`
		Networks    map[string]json.RawMessage `json:"networks"`
		Restart     string                     `json:"restart"`
		Privileged  bool                       `json:"privileged"`
		NetworkMode string                     `json:"network_mode"`
		Pid         string                     `json:"pid"`
		Deploy      struct {
			Resources struct {
				Limits struct {
					CPUs   flexNumber `json:"cpus"`
					Memory flexBytes  `json:"memory"`
				} `json:"limits"`
			} `json:"resources"`
		} `json:"deploy"`
		Healthcheck *struct {
			Test     []string `json:"test"`
			Interval string   `json:"interval"`
			Timeout  string   `json:"timeout"`
			Retries  int      `json:"retries"`
		} `json:"healthcheck"`
	} `json:"services"`
}

// parseComposeConfigDoc decodes the doc once so both ParseComposeServices
// (preview.go) and any future caller needing the same fields don't each
// re-walk the raw JSON their own way.
func parseComposeConfigDoc(configJSON []byte) (composeConfigDoc, error) {
	var cfg composeConfigDoc
	err := json.Unmarshal(configJSON, &cfg)
	return cfg, err
}

// serviceSpecFromComposeDoc builds one service's full ServiceSpec, resolving
// its declared networks AND named volumes to their real (project-prefixed)
// Docker names via the doc's top-level networks/volumes sections — the names
// a running container's NetworkSettings.Networks / Mounts will actually use.
// Without this, a service's own compose entry names a volume "webdata" while
// the container it's actually mounted from is "myproject_webdata", and every
// comparison against the live state would read as "changed" for a volume
// that never changed at all.
func serviceSpecFromComposeDoc(cfg composeConfigDoc, name string) ServiceSpec {
	svc := cfg.Services[name]
	s := ServiceSpec{Name: name, Image: svc.Image, Detailed: true}
	if len(svc.Environment) > 0 {
		s.Env = svc.Environment
	}
	for _, p := range svc.Ports {
		s.Ports = append(s.Ports, ServicePort{Target: p.Target, Published: string(p.Published), Protocol: p.Protocol})
	}
	sortPorts(s.Ports)
	for _, v := range svc.Volumes {
		src := v.Source
		if v.Type == "volume" {
			if vol, ok := cfg.Volumes[v.Source]; ok && vol.Name != "" {
				src = vol.Name
			}
		}
		s.Volumes = append(s.Volumes, VolumeSpec{Type: v.Type, Source: src, Target: v.Target})
	}
	sortVolumes(s.Volumes)
	for local := range svc.Networks {
		real := local
		if n, ok := cfg.Networks[local]; ok && n.Name != "" {
			real = n.Name
		}
		s.Networks = append(s.Networks, real)
	}
	sort.Strings(s.Networks)
	s.Restart = svc.Restart
	if cpus := float64(svc.Deploy.Resources.Limits.CPUs); cpus > 0 {
		s.CPULimit = cpus
	}
	if mem := int64(svc.Deploy.Resources.Limits.Memory); mem > 0 {
		s.MemoryLimit = mem
	}
	if svc.Healthcheck != nil && len(svc.Healthcheck.Test) > 0 {
		hc := &HealthcheckSpec{Test: svc.Healthcheck.Test, Retries: svc.Healthcheck.Retries}
		hc.Interval, _ = time.ParseDuration(svc.Healthcheck.Interval)
		hc.Timeout, _ = time.ParseDuration(svc.Healthcheck.Timeout)
		s.Healthcheck = hc
	}
	return s
}

// LiveServiceSpec inspects a running container and reports its actual
// config in the same shape as the resolved (compose) side, so the two can be
// compared field for field. containerID must belong to hostID.
func (m *Manager) LiveServiceSpec(ctx context.Context, hostID int64, containerID string) (ServiceSpec, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return ServiceSpec{}, err
	}
	c, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return ServiceSpec{}, err
	}
	s := ServiceSpec{Detailed: true}
	if c.Config != nil {
		if len(c.Config.Env) > 0 {
			s.Env = map[string]string{}
			for _, kv := range c.Config.Env {
				if i := strings.IndexByte(kv, '='); i >= 0 {
					s.Env[kv[:i]] = kv[i+1:]
				}
			}
		}
		if hc := c.Config.Healthcheck; hc != nil && len(hc.Test) > 0 {
			s.Healthcheck = &HealthcheckSpec{
				Test: hc.Test, Interval: hc.Interval, Timeout: hc.Timeout, Retries: hc.Retries,
			}
		}
	}
	if c.HostConfig != nil {
		s.Restart = string(c.HostConfig.RestartPolicy.Name)
		if c.HostConfig.NanoCPUs > 0 {
			s.CPULimit = float64(c.HostConfig.NanoCPUs) / 1e9
		}
		if c.HostConfig.Memory > 0 {
			s.MemoryLimit = c.HostConfig.Memory
		}
		for portProto, bindings := range c.HostConfig.PortBindings {
			target := portProto.Int()
			if target == 0 {
				continue
			}
			for _, b := range bindings {
				s.Ports = append(s.Ports, ServicePort{Target: target, Published: b.HostPort, Protocol: portProto.Proto()})
			}
		}
	}
	sortPorts(s.Ports)
	for _, mnt := range c.Mounts {
		src := mnt.Source
		if mnt.Type == mount.TypeVolume && mnt.Name != "" {
			src = mnt.Name
		}
		s.Volumes = append(s.Volumes, VolumeSpec{Type: string(mnt.Type), Source: src, Target: mnt.Destination})
	}
	sortVolumes(s.Volumes)
	if c.NetworkSettings != nil {
		for name := range c.NetworkSettings.Networks {
			s.Networks = append(s.Networks, name)
		}
	}
	sort.Strings(s.Networks)
	return s, nil
}

// LiveServices reduces a stack's containers to one full ServiceSpec per
// compose service (first container per service, same reduction as
// RunningServices) via a LiveServiceSpec inspect each — the expensive
// counterpart used when a caller wants the full plan/diff, not just the
// image comparison. Best-effort: a service whose inspect fails is left out
// entirely rather than failing the whole preview over one container.
func (m *Manager) LiveServices(ctx context.Context, hostID int64, containers []StackContainer) []ServiceSpec {
	seen := map[string]bool{}
	out := []ServiceSpec{}
	for _, c := range containers {
		if c.Service == "" || seen[c.Service] {
			continue
		}
		seen[c.Service] = true
		s, err := m.LiveServiceSpec(ctx, hostID, c.ID)
		if err != nil {
			continue
		}
		s.Name = c.Service
		s.Image = c.Image
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RebaseBindSources rewrites every bind-mount source under fromDir to the
// equivalent path under toDir. `docker compose config` resolves a relative
// bind mount (`./html:...`) to an ABSOLUTE path anchored to whatever
// directory it was run in — for a revision, that's a throwaway extraction
// directory, a different one on every call. Without rebasing, comparing a
// revision's specs against the live project directory (or against another
// revision's own throwaway directory) would report a "volumes" change for
// every relative bind mount, on every comparison, whether or not anything
// about it actually changed.
func RebaseBindSources(specs []ServiceSpec, fromDir, toDir string) {
	for i := range specs {
		for j := range specs[i].Volumes {
			v := &specs[i].Volumes[j]
			if v.Type != "bind" {
				continue
			}
			rel, err := filepath.Rel(fromDir, v.Source)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue // outside fromDir entirely — not a path this snapshot owns, leave it alone
			}
			v.Source = filepath.Join(toDir, rel)
		}
	}
}
