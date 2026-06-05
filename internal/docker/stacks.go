package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// maxComposeBytes caps how much of a compose file we read/display.
const maxComposeBytes = 1 << 20 // 1 MiB

// Compose labels that `docker compose` stamps on the resources it creates. We
// read them to discover and group stacks — whether they were deployed by us or
// by the `docker compose` CLI on the host.
const (
	labelComposeProject     = "com.docker.compose.project"
	labelComposeService     = "com.docker.compose.service"
	labelComposeConfigFiles = "com.docker.compose.project.config_files"
	labelComposeWorkingDir  = "com.docker.compose.project.working_dir"
)

// StackContainer is one container belonging to a Compose stack.
type StackContainer struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Service string        `json:"service"`
	State   string        `json:"state"`
	Status  string        `json:"status"`
	Image   string        `json:"image"`
	Ports   []PortMapping `json:"ports,omitempty"`
}

// Stack is a group of containers sharing a Compose project label.
type Stack struct {
	Project    string           `json:"project"`
	ConfigFile string           `json:"configFile,omitempty"` // host path, from the label
	WorkingDir string           `json:"workingDir,omitempty"`
	Containers []StackContainer `json:"containers"`
	Running    int              `json:"running"`
}

// ListStacks groups the host's containers into Compose stacks by their
// `com.docker.compose.project` label. Containers without the label are ignored.
func (m *Manager) ListStacks(ctx context.Context, hostID int64) ([]Stack, error) {
	containers, err := m.ListContainers(ctx, hostID)
	if err != nil {
		return nil, err
	}

	byProject := map[string]*Stack{}
	for _, c := range containers {
		project := c.Labels[labelComposeProject]
		if project == "" {
			continue
		}
		st := byProject[project]
		if st == nil {
			st = &Stack{
				Project:    project,
				ConfigFile: c.Labels[labelComposeConfigFiles],
				WorkingDir: c.Labels[labelComposeWorkingDir],
			}
			byProject[project] = st
		}
		st.Containers = append(st.Containers, StackContainer{
			ID: c.ID, Name: c.Name, Service: c.Labels[labelComposeService],
			State: c.State, Status: c.Status, Image: c.Image, Ports: c.Ports,
		})
		if c.State == "running" {
			st.Running++
		}
	}

	out := make([]Stack, 0, len(byProject))
	for _, st := range byProject {
		sort.Slice(st.Containers, func(i, j int) bool {
			if st.Containers[i].Service != st.Containers[j].Service {
				return st.Containers[i].Service < st.Containers[j].Service
			}
			return st.Containers[i].Name < st.Containers[j].Name
		})
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

// StackAction applies a lifecycle action to every container in a stack:
// start / stop / restart, or remove (force-removes the containers and then the
// project's Compose networks, leaving named volumes intact — like
// `docker compose down`).
func (m *Manager) StackAction(ctx context.Context, hostID int64, project, action string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	containers, err := m.ListContainers(ctx, hostID)
	if err != nil {
		return err
	}
	var ids []string
	for _, c := range containers {
		if c.Labels[labelComposeProject] == project {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("no containers found for stack %q", project)
	}

	switch action {
	case "start", "stop", "restart":
		for _, id := range ids {
			if err := m.ContainerAction(ctx, hostID, id, action); err != nil {
				return err
			}
		}
		return nil

	case "remove":
		for _, id := range ids {
			if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
				return err
			}
		}
		// Remove the project's Compose networks (best-effort; an external or
		// still-referenced network is left alone). Named volumes are kept.
		nets, err := cli.NetworkList(ctx, network.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", labelComposeProject+"="+project)),
		})
		if err == nil {
			for _, n := range nets {
				_ = cli.NetworkRemove(ctx, n.ID)
			}
		}
		return nil

	default:
		return ErrUnknownAction
	}
}

// StackComposeFile best-effort reads and returns the stack's compose file. The
// file lives on the host (its path comes from the compose labels), so we read
// it directly for the local daemon or over SSH for ssh hosts. TCP hosts give us
// no filesystem access. Returns the resolved path and contents.
func (m *Manager) StackComposeFile(ctx context.Context, hostID int64, project string) (path, content string, err error) {
	stacks, err := m.ListStacks(ctx, hostID)
	if err != nil {
		return "", "", err
	}
	var st *Stack
	for i := range stacks {
		if stacks[i].Project == project {
			st = &stacks[i]
			break
		}
	}
	if st == nil {
		return "", "", fmt.Errorf("stack %q not found", project)
	}
	if st.ConfigFile == "" {
		return "", "", fmt.Errorf("this stack has no compose file recorded (its containers carry no config_files label)")
	}

	// The label may list several files (comma-separated); use the first.
	cf := strings.TrimSpace(strings.SplitN(st.ConfigFile, ",", 2)[0])

	// Candidate paths: absolute as-is, else joined with the working dir; plus a
	// working-dir + basename fallback for older compose label quirks.
	var candidates []string
	if filepath.IsAbs(cf) {
		candidates = append(candidates, cf)
	} else if st.WorkingDir != "" {
		candidates = append(candidates, filepath.Join(st.WorkingDir, cf))
	} else {
		candidates = append(candidates, cf)
	}
	if st.WorkingDir != "" {
		candidates = append(candidates, filepath.Join(st.WorkingDir, filepath.Base(cf)))
	}

	id, err := m.resolveHostID(ctx, hostID)
	if err != nil {
		return "", "", err
	}
	h, err := m.store.HostByID(ctx, id)
	if err != nil {
		return "", "", err
	}

	var lastErr error
	for _, p := range candidates {
		base := strings.ToLower(filepath.Base(p))
		if !strings.HasSuffix(base, ".yml") && !strings.HasSuffix(base, ".yaml") {
			lastErr = fmt.Errorf("refusing to read non-YAML path %q", p)
			continue
		}
		data, err := m.readHostFile(ctx, id, h, p)
		if err == nil {
			return p, data, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("could not resolve the compose file path")
	}
	return "", "", lastErr
}

// resolveHostID maps hostID <= 0 to the default local host.
func (m *Manager) resolveHostID(ctx context.Context, hostID int64) (int64, error) {
	if hostID > 0 {
		return hostID, nil
	}
	return m.defaultHostID(ctx)
}

// readHostFile reads a host-side file (capped) for the local daemon or over SSH.
func (m *Manager) readHostFile(ctx context.Context, hostID int64, h *store.Host, path string) (string, error) {
	switch h.Kind {
	case "local", "":
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxComposeBytes))
		if err != nil {
			return "", err
		}
		return string(data), nil

	case "ssh":
		cli, err := m.sshClientFor(hostID, h)
		if err != nil {
			return "", err
		}
		sess, err := cli.NewSession()
		if err != nil {
			return "", err
		}
		defer sess.Close()
		// head -c caps the size; the path is single-quote escaped.
		out, err := sess.Output(fmt.Sprintf("head -c %d -- %s", maxComposeBytes, shellQuote(path)))
		if err != nil {
			return "", fmt.Errorf("read over ssh failed (is the path readable by the ssh user?): %w", err)
		}
		return string(out), nil

	default:
		return "", fmt.Errorf("the compose file lives on the host and isn't reachable over a %s connection — open it on the host", h.Kind)
	}
}

// shellQuote single-quotes a string for safe use in a remote shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
