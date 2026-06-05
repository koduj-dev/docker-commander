package docker

import (
	"context"
	"fmt"
	"sort"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

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
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Image   string `json:"image"`
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
			State: c.State, Status: c.Status, Image: c.Image,
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
