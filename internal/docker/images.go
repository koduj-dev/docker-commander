package docker

import (
	"context"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

// ImageSummary is a compact view of a local image for the Images page.
type ImageSummary struct {
	ID          string   `json:"id"`
	RepoTags    []string `json:"repoTags"`
	RepoDigests []string `json:"repoDigests"`
	Size        int64    `json:"size"`
	Created     int64    `json:"created"` // unix seconds
	Dangling    bool     `json:"dangling"`
	InUse       bool     `json:"inUse"` // referenced by an existing container
}

// PullProgress is one progress update from an image pull, forwarded to the UI.
type PullProgress struct {
	Status  string `json:"status,omitempty"`
	ID      string `json:"id,omitempty"`
	Current int64  `json:"current,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

// ImagePruneResult reports what a dangling-image prune removed.
type ImagePruneResult struct {
	Deleted        []string `json:"deleted"`
	SpaceReclaimed uint64   `json:"spaceReclaimed"`
}

// ListImages returns local images, flagging which are untagged ("dangling")
// and which are referenced by an existing container (so the UI can warn before
// removing one that is in use).
func (m *Manager) ListImages(ctx context.Context, hostID int64) ([]ImageSummary, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	imgs, err := cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, err
	}

	// Build the set of image IDs currently referenced by containers so we can
	// flag in-use images. A failure here is non-fatal — we just skip the flag.
	inUse := map[string]bool{}
	if ctrs, err := cli.ContainerList(ctx, container.ListOptions{All: true}); err == nil {
		for _, c := range ctrs {
			inUse[c.ImageID] = true
		}
	}

	out := make([]ImageSummary, 0, len(imgs))
	for _, im := range imgs {
		s := ImageSummary{
			ID:          im.ID,
			RepoTags:    im.RepoTags,
			RepoDigests: im.RepoDigests,
			Size:        im.Size,
			Created:     im.Created,
			Dangling:    isDangling(im.RepoTags),
			InUse:       inUse[im.ID],
		}
		out = append(out, s)
	}
	// Alphabetical by first real tag; untagged/dangling images sort last.
	sortKey := func(im ImageSummary) string {
		for _, t := range im.RepoTags {
			if t != "" && t != "<none>:<none>" {
				return strings.ToLower(t)
			}
		}
		return "\uffff" + im.ID
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ki, kj := sortKey(out[i]), sortKey(out[j]); ki != kj {
			return ki < kj
		}
		return out[i].ID < out[j].ID // deterministic tie-break on equal keys
	})
	return out, nil
}

// isDangling reports whether an image is untagged (no usable repo:tag).
func isDangling(repoTags []string) bool {
	if len(repoTags) == 0 {
		return true
	}
	for _, t := range repoTags {
		if t != "" && t != "<none>:<none>" {
			return false
		}
	}
	return true
}

// RemoveImage deletes an image by ID or reference. force allows removing an
// image that is tagged multiple times or referenced by stopped containers.
// It returns the list of untagged/deleted references for the UI.
func (m *Manager) RemoveImage(ctx context.Context, hostID int64, ref string, force bool) ([]string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	res, err := cli.ImageRemove(ctx, ref, image.RemoveOptions{Force: force, PruneChildren: true})
	if err != nil {
		return nil, err
	}
	changed := make([]string, 0, len(res))
	for _, r := range res {
		if r.Untagged != "" {
			changed = append(changed, "untagged "+r.Untagged)
		}
		if r.Deleted != "" {
			changed = append(changed, "deleted "+r.Deleted)
		}
	}
	return changed, nil
}

// PruneImages removes dangling images and reports what was reclaimed.
func (m *Manager) PruneImages(ctx context.Context, hostID int64) (*ImagePruneResult, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	rep, err := cli.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		return nil, err
	}
	res := &ImagePruneResult{SpaceReclaimed: rep.SpaceReclaimed}
	for _, d := range rep.ImagesDeleted {
		if d.Deleted != "" {
			res.Deleted = append(res.Deleted, d.Deleted)
		} else if d.Untagged != "" {
			res.Deleted = append(res.Deleted, d.Untagged)
		}
	}
	return res, nil
}

// PullImage pulls a reference and reports progress via onProgress until the
// stream completes. The Docker daemon emits newline-delimited JSON messages;
// we decode them and translate to PullProgress. A message carrying an error
// aborts the pull with that error.
func (m *Manager) PullImage(ctx context.Context, hostID int64, ref string, onProgress func(PullProgress)) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	// Attach stored credentials for the image's registry, if any, so private
	// images pull; otherwise the pull proceeds anonymously.
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{RegistryAuth: m.authForRef(ctx, ref)})
	if err != nil {
		return err
	}
	defer rc.Close()
	return streamJSONProgress(rc, onProgress)
}
