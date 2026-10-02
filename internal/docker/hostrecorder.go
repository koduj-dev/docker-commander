package docker

import (
	"context"
	"sync"
)

// HostRecorder notes which Docker host a request actually talked to, so the
// audit entry for it can say where the action happened.
//
// It is filled in by Manager.Client, the one place every daemon connection is
// made, after "no host given" has been resolved to the concrete default host.
// Recording it there, rather than at each of the many audit calls, means a new
// handler is covered without having to remember to pass the host along.
type HostRecorder struct {
	mu  sync.Mutex
	id  int64
	set bool
}

type hostRecorderKey struct{}

// WithHostRecorder returns ctx carrying a fresh recorder.
func WithHostRecorder(ctx context.Context) (context.Context, *HostRecorder) {
	rec := &HostRecorder{}
	return context.WithValue(ctx, hostRecorderKey{}, rec), rec
}

// HostRecorderFrom returns the recorder in ctx, or nil.
func HostRecorderFrom(ctx context.Context) *HostRecorder {
	rec, _ := ctx.Value(hostRecorderKey{}).(*HostRecorder)
	return rec
}

// Host returns the recorded host, and whether one was recorded.
func (r *HostRecorder) Host() (int64, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id, r.set
}

// RecordHost notes hostID in ctx's recorder, for a caller that knows the host an
// action is about without connecting to it (an authorisation check on it, say).
func RecordHost(ctx context.Context, hostID int64) { recordHost(ctx, hostID) }

// recordHost notes hostID in ctx's recorder. The first host wins: an action
// names one target, and a later lookup on the side (another host's status, say)
// must not relabel it.
func recordHost(ctx context.Context, hostID int64) {
	rec := HostRecorderFrom(ctx)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !rec.set {
		rec.id, rec.set = hostID, true
	}
}
