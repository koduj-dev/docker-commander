package docker

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func TestBuildDiskReport(t *testing.T) {
	du := client.DiskUsageResult{}
	du.Images.Items = []image.Summary{
		{ID: "small", RepoTags: []string{"a:1"}, Size: 100, SharedSize: 100, Containers: 1},
		{ID: "big", RepoTags: []string{"b:1"}, Size: 1000, SharedSize: 400, Containers: 0},
		{ID: "unk", Size: 5000, SharedSize: -1, Containers: -1},
	}
	du.Containers.Items = []container.Summary{
		{ID: "c1", Names: []string{"/web"}, State: "running", SizeRw: 10, SizeRootFs: 110, Labels: map[string]string{composeProjectLabel: "shop"}},
		{ID: "c2", Names: []string{"/db"}, State: "exited", SizeRw: 900, SizeRootFs: 1900},
	}
	du.Volumes.Items = []volume.Volume{
		{Name: "data", Driver: "local", UsageData: &volume.UsageData{Size: 700, RefCount: 1}, Labels: map[string]string{composeProjectLabel: "shop"}},
		{Name: "remote", Driver: "nfs"}, // no usage data at all
		{Name: "unmeasured", Driver: "x", UsageData: &volume.UsageData{Size: -1, RefCount: -1}},
	}
	// Aggregates as the client computes them: the Shared record (500) is in the
	// count but excluded from TotalSize and Reclaimable.
	du.BuildCache.Items = []build.CacheRecord{{Size: 30, InUse: true}, {Size: 70, InUse: false}, {Size: 500, InUse: false, Shared: true}}
	du.BuildCache.TotalCount, du.BuildCache.TotalSize, du.BuildCache.Reclaimable = 3, 100, 70
	// Docker's own reclaimable figures (unused images' unique size, stopped
	// containers' writable layers, ...), as the client normalises them.
	du.Images.Reclaimable, du.Containers.Reclaimable, du.Volumes.Reclaimable = 600, 900, 0

	r := buildDiskReport(du, time.Unix(1000, 0))

	// Images rank by UNIQUE size (what deleting frees), unknown last.
	if got := []string{r.Images[0].ID, r.Images[1].ID, r.Images[2].ID}; got[0] != "big" || got[1] != "small" || got[2] != "unk" {
		t.Errorf("image order = %v, want big, small, unk", got)
	}
	if r.Images[0].Unique != 600 || r.Images[1].Unique != 0 || r.Images[2].Unique != SizeUnknown {
		t.Errorf("unique sizes = %d/%d/%d", r.Images[0].Unique, r.Images[1].Unique, r.Images[2].Unique)
	}
	if r.Containers[0].Name != "db" || r.Containers[0].State != "exited" || r.Containers[1].Project != "shop" {
		t.Errorf("containers = %+v", r.Containers)
	}
	// An unknown volume size must stay unknown — never presented as 0 — and sort last.
	if r.Volumes[0].Name != "data" || r.Volumes[0].Project != "shop" {
		t.Errorf("volumes = %+v", r.Volumes)
	}
	for _, v := range r.Volumes[1:] {
		if v.Size != SizeUnknown {
			t.Errorf("volume %q size = %d, want unknown", v.Name, v.Size)
		}
	}
	if r.BuildCache.Count != 3 || r.BuildCache.Size != 100 || r.BuildCache.Reclaimable != 70 {
		t.Errorf("build cache = %+v, want the client aggregates (a Shared record must not count as reclaimable)", r.BuildCache)
	}
	if r.Reclaimable.Images != 600 || r.Reclaimable.Containers != 900 || r.Reclaimable.Volumes != 0 || r.Reclaimable.BuildCache != 70 || r.Reclaimable.Total != 1570 {
		t.Errorf("reclaimable = %+v, want the client's per-category figures and their sum", r.Reclaimable)
	}
	if r.GeneratedAt != 1000 {
		t.Errorf("generatedAt = %d", r.GeneratedAt)
	}
}

func TestDiskReportCache(t *testing.T) {
	now := time.Unix(0, 0)
	c := newDiskReportCache()
	c.now = func() time.Time { return now }
	var calls int32
	compute := func() (*DiskReport, error) { atomic.AddInt32(&calls, 1); return &DiskReport{}, nil }
	get := func(host int64, force bool) {
		t.Helper()
		if _, err := c.get(host, force, compute); err != nil {
			t.Fatal(err)
		}
	}

	get(1, false)
	get(1, false)
	if calls != 1 {
		t.Fatalf("second normal call should be served from cache, computed %d times", calls)
	}
	now = now.Add(2 * time.Second)
	get(1, true) // forced, but under the minimum age
	if calls != 1 {
		t.Errorf("a forced refresh under the minimum age must not recompute, computed %d times", calls)
	}
	now = now.Add(10 * time.Second)
	get(1, true) // forced and old enough
	if calls != 2 {
		t.Errorf("a forced refresh past the minimum age should recompute, computed %d times", calls)
	}
	now = now.Add(30 * time.Second)
	get(1, false) // 30s old: inside the TTL
	if calls != 2 {
		t.Errorf("a normal call inside the TTL should hit the cache, computed %d times", calls)
	}
	now = now.Add(31 * time.Second)
	get(1, false) // past the TTL
	if calls != 3 {
		t.Errorf("a normal call past the TTL should recompute, computed %d times", calls)
	}
	get(2, false) // another host has its own entry
	if calls != 4 {
		t.Errorf("hosts must not share a cache entry, computed %d times", calls)
	}
}

func TestDiskReportCacheDoesNotCacheErrorsAndCoalesces(t *testing.T) {
	c := newDiskReportCache()
	fail := true
	if _, err := c.get(1, false, func() (*DiskReport, error) {
		if fail {
			return nil, errors.New("daemon down")
		}
		return &DiskReport{}, nil
	}); err == nil {
		t.Fatal("expected the compute error")
	}
	fail = false
	var calls int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.get(1, false, func() (*DiskReport, error) {
				atomic.AddInt32(&calls, 1)
				time.Sleep(20 * time.Millisecond)
				return &DiskReport{}, nil
			})
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Errorf("concurrent callers should share one computation (and a prior error must not be cached), computed %d times", calls)
	}
}

// The dashboard's Build cache tile must use the client's aggregates too: a
// Shared record (its layers are referenced elsewhere) is not part of TotalSize.
func TestDiskTotalsBuildCacheExcludesSharedRecords(t *testing.T) {
	du := client.DiskUsageResult{}
	du.BuildCache.Items = []build.CacheRecord{{Size: 30}, {Size: 500, Shared: true}}
	du.BuildCache.TotalCount, du.BuildCache.TotalSize = 2, 30
	du.Volumes.Items = []volume.Volume{{Name: "v", UsageData: &volume.UsageData{Size: 7, RefCount: 1}}}
	got := diskTotals(du)
	if got.BuildCache.Size != 30 || got.BuildCache.Count != 2 {
		t.Errorf("build cache = %+v, want size 30 (shared record excluded), count 2", got.BuildCache)
	}
	if got.Volumes.Size != 7 || got.Volumes.Count != 1 {
		t.Errorf("volumes = %+v", got.Volumes)
	}
}

// The Images tile is the deduplicated total: two images sharing a 100-byte base
// occupy 130 bytes, not the 260 their Sizes add up to.
func TestDiskTotalsImagesCountSharedLayersOnce(t *testing.T) {
	du := client.DiskUsageResult{}
	du.Images.Items = []image.Summary{{Size: 130}, {Size: 130}}
	du.Images.TotalCount, du.Images.TotalSize = 2, 160
	got := diskTotals(du)
	if got.Images.Size != 160 || got.Images.Count != 2 {
		t.Errorf("images = %+v, want size 160 (layers counted once), count 2", got.Images)
	}
}
