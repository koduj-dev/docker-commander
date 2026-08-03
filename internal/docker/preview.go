package docker

import (
	"encoding/json"
	"sort"
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

// ServiceSpec is one service as compose resolves it.
type ServiceSpec struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// ServiceChange is one difference between the resolved config and reality.
type ServiceChange struct {
	Service string `json:"service"`
	// Kind is "added" (would be created), "removed" (running but no longer in the
	// file) or "image" (same service, different image).
	Kind     string `json:"kind"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Existing bool   `json:"existing"`
}

// DeployPreview is what a deploy would change.
type DeployPreview struct {
	Services  []ServiceSpec   `json:"services"`
	Running   []ServiceSpec   `json:"running"`
	Changes   []ServiceChange `json:"changes"`
	Unchanged int             `json:"unchanged"`
}

// ParseComposeServices pulls the service list out of `docker compose config
// --format json`.
func ParseComposeServices(configJSON []byte) ([]ServiceSpec, error) {
	var cfg struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	out := make([]ServiceSpec, 0, len(cfg.Services))
	for name, svc := range cfg.Services {
		out = append(out, ServiceSpec{Name: name, Image: svc.Image})
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
				Detail: "running a different image; a deploy would recreate it", Existing: true,
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
