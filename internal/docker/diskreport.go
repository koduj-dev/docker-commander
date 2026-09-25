package docker

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"
)

const composeProjectLabel = "com.docker.compose.project"

// SizeUnknown marks a size the daemon did not calculate (a volume from a
// non-local driver, an image whose shared size was not computed). It is
// reported as-is rather than as 0, so the UI can say "unknown" instead of
// presenting a missing value as an empty object.
const SizeUnknown int64 = -1

// DiskImage is one image's footprint. Size is the whole image including every
// layer; Unique is what deleting it would actually free (Size minus layers
// other images share). Summing Size across images overcounts, which is why the
// report carries no image total.
type DiskImage struct {
	ID         string   `json:"id"`
	Tags       []string `json:"tags"`
	Size       int64    `json:"size"`
	Unique     int64    `json:"unique"`     // SizeUnknown when the daemon did not compute shared size
	Containers int64    `json:"containers"` // containers using it; SizeUnknown if not computed
}

// DiskContainer is a container's own disk use: the writable layer (SizeRw) on
// top of its image, and the total including the image (SizeRootFs).
type DiskContainer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Project  string `json:"project,omitempty"` // compose project, when there is one
	State    string `json:"state"`
	SizeRw   int64  `json:"sizeRw"`
	SizeRoot int64  `json:"sizeRoot"`
}

// DiskVolume is a volume's size (SizeUnknown for non-local drivers) and how many
// containers reference it.
type DiskVolume struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Project  string `json:"project,omitempty"`
	Size     int64  `json:"size"`
	RefCount int64  `json:"refCount"` // SizeUnknown if not available
}

// DiskBuildCache is the builder cache in aggregate — individual records are
// not something an operator acts on.
type DiskBuildCache struct {
	Count       int   `json:"count"`
	Size        int64 `json:"size"`
	Reclaimable int64 `json:"reclaimable"` // records not in use
}

// DiskReclaimable is what could be freed, per category, as Docker itself
// computes it (the RECLAIMABLE column of `docker system df`): images no
// container uses (their unique layers), stopped containers' writable layers,
// volumes no container references, and build cache not in use. For images it is
// a lower bound — layers shared only among several unused images are counted in
// none of them, though pruning all of them frees those too.
type DiskReclaimable struct {
	Images     int64 `json:"images"`
	Containers int64 `json:"containers"`
	Volumes    int64 `json:"volumes"`
	BuildCache int64 `json:"buildCache"`
	Total      int64 `json:"total"`
}

// DiskReport is `docker system df -v` with every object kept, sorted by size
// (largest first, unknown last) so the UI can answer "what takes the most
// space" directly.
type DiskReport struct {
	GeneratedAt int64           `json:"generatedAt"` // unix seconds
	Images      []DiskImage     `json:"images"`
	Containers  []DiskContainer `json:"containers"`
	Volumes     []DiskVolume    `json:"volumes"`
	BuildCache  DiskBuildCache  `json:"buildCache"`
	Reclaimable DiskReclaimable `json:"reclaimable"`
}

func sizeKey(n int64) int64 {
	if n < 0 {
		return -1 // unknown sorts after every real size, including 0
	}
	return n
}

func buildDiskReport(du client.DiskUsageResult, now time.Time) *DiskReport {
	out := &DiskReport{
		GeneratedAt: now.Unix(),
		Images:      []DiskImage{},
		Containers:  []DiskContainer{},
		Volumes:     []DiskVolume{},
	}
	for _, im := range du.Images.Items {
		unique := SizeUnknown
		if im.SharedSize >= 0 && im.Size >= im.SharedSize {
			unique = im.Size - im.SharedSize
		}
		out.Images = append(out.Images, DiskImage{ID: im.ID, Tags: im.RepoTags, Size: im.Size, Unique: unique, Containers: im.Containers})
	}
	for _, c := range du.Containers.Items {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out.Containers = append(out.Containers, DiskContainer{
			ID: c.ID, Name: name, Project: c.Labels[composeProjectLabel], State: string(c.State),
			SizeRw: c.SizeRw, SizeRoot: c.SizeRootFs,
		})
	}
	for _, v := range du.Volumes.Items {
		dv := DiskVolume{Name: v.Name, Driver: v.Driver, Project: v.Labels[composeProjectLabel], Size: SizeUnknown, RefCount: SizeUnknown}
		if v.UsageData != nil {
			dv.Size, dv.RefCount = v.UsageData.Size, v.UsageData.RefCount
		}
		out.Volumes = append(out.Volumes, dv)
	}
	// Use the client's aggregates, not a sum over Items: a record marked Shared
	// is deliberately left out of TotalSize and Reclaimable (its layers are
	// still referenced elsewhere), so summing every record would overstate both
	// and promise space a prune won't free.
	out.BuildCache = DiskBuildCache{
		Count:       int(du.BuildCache.TotalCount),
		Size:        du.BuildCache.TotalSize,
		Reclaimable: du.BuildCache.Reclaimable,
	}
	out.Reclaimable = DiskReclaimable{
		Images:     du.Images.Reclaimable,
		Containers: du.Containers.Reclaimable,
		Volumes:    du.Volumes.Reclaimable,
		BuildCache: du.BuildCache.Reclaimable,
	}
	out.Reclaimable.Total = out.Reclaimable.Images + out.Reclaimable.Containers + out.Reclaimable.Volumes + out.Reclaimable.BuildCache
	// Ties fall back to name/id so the order is stable between refreshes.
	sort.SliceStable(out.Images, func(i, j int) bool {
		a, b := out.Images[i], out.Images[j]
		if sizeKey(a.Unique) != sizeKey(b.Unique) {
			return sizeKey(a.Unique) > sizeKey(b.Unique)
		}
		return a.ID < b.ID
	})
	sort.SliceStable(out.Containers, func(i, j int) bool {
		a, b := out.Containers[i], out.Containers[j]
		if sizeKey(a.SizeRw) != sizeKey(b.SizeRw) {
			return sizeKey(a.SizeRw) > sizeKey(b.SizeRw)
		}
		return a.Name < b.Name
	})
	sort.SliceStable(out.Volumes, func(i, j int) bool {
		a, b := out.Volumes[i], out.Volumes[j]
		if sizeKey(a.Size) != sizeKey(b.Size) {
			return sizeKey(a.Size) > sizeKey(b.Size)
		}
		return a.Name < b.Name
	})
	return out
}

// DiskReport returns the per-object disk report for a host. `system df -v` is
// expensive on a busy daemon (it walks every volume and container), so results
// are cached per host: a normal call reuses a report up to diskReportTTL old,
// and force (a user pressing Refresh) recomputes unless the cached one is
// under diskReportMinAge old — a floor that keeps a stuck client or a
// double-click from hammering the daemon. Concurrent calls for one host wait
// for the same computation instead of each running their own.
func (m *Manager) DiskReport(ctx context.Context, hostID int64, force bool) (*DiskReport, error) {
	return m.diskCache.get(hostID, force, func() (*DiskReport, error) {
		cli, err := m.Client(ctx, hostID)
		if err != nil {
			return nil, err
		}
		du, err := cli.DiskUsage(ctx, client.DiskUsageOptions{
			Containers: true, Images: true, Volumes: true, BuildCache: true, Verbose: true,
		})
		if err != nil {
			return nil, err
		}
		return buildDiskReport(du, time.Now()), nil
	})
}

const (
	diskReportTTL    = time.Minute
	diskReportMinAge = 5 * time.Second
)

type diskEntry struct {
	mu     sync.Mutex // held while computing, so concurrent callers coalesce
	report *DiskReport
	at     time.Time
}

type diskReportCache struct {
	mu     sync.Mutex
	hosts  map[int64]*diskEntry
	now    func() time.Time
	ttl    time.Duration
	minAge time.Duration
}

func newDiskReportCache() *diskReportCache {
	return &diskReportCache{hosts: map[int64]*diskEntry{}, now: time.Now, ttl: diskReportTTL, minAge: diskReportMinAge}
}

func (c *diskReportCache) get(hostID int64, force bool, compute func() (*DiskReport, error)) (*DiskReport, error) {
	c.mu.Lock()
	e := c.hosts[hostID]
	if e == nil {
		e = &diskEntry{}
		c.hosts[hostID] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.report != nil {
		age := c.now().Sub(e.at)
		if age < c.minAge || (!force && age < c.ttl) {
			return e.report, nil
		}
	}
	r, err := compute()
	if err != nil {
		return nil, err
	}
	e.report, e.at = r, c.now()
	return r, nil
}
