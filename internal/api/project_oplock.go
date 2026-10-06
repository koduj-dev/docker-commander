package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// One operation at a time per project, for everything that changes the
// project's files or what is deployed from them: editor writes, uploads,
// deletes, settings, deploy, down/restart, restore, and deleting the project.
//
// Without it a restore, which snapshots the folder, swaps it in and deletes the
// old one, raced an editor save made in between: the save answered 200 and was
// then thrown away with the old folder. A second operation is refused with 409
// and told what is running, rather than queued: a deploy can take minutes, and
// a save that waits that long looks like a hang.
type projectOp struct {
	mu   sync.Mutex
	held bool
	what string
}

var projectOps sync.Map // map[int64]*projectOp

// beginProjectOp claims the project for one operation. It returns the release
// func, or, when another operation holds it, nil and what that one is.
func beginProjectOp(projectID int64, what string) (release func(), busy string) {
	v, _ := projectOps.LoadOrStore(projectID, &projectOp{})
	op := v.(*projectOp)
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.held {
		return nil, op.what
	}
	op.held, op.what = true, what
	return func() {
		op.mu.Lock()
		op.held, op.what = false, ""
		op.mu.Unlock()
	}, ""
}

// errBusy marks the refusal of an operation that found its project busy.
var errBusy = errors.New("this project is busy")

// errProjectBusy is the refusal for an operation that found the project busy.
func errProjectBusy(busy string) error {
	return fmt.Errorf("%w (%s is running); try again when it finishes", errBusy, busy)
}

// beginStackOp claims the project that deploys a stack, when one does: a
// stack's start/stop/restart/remove act on that project's containers, so they
// wait their turn like the project's own operations. A stack no project owns
// needs nothing and gets a no-op release. Not being able to tell is a refusal,
// not a pass.
func (s *Server) beginStackOp(ctx context.Context, hostID int64, stack, what string) (release func(), err error) {
	p, err := s.stackProject(ctx, hostID, stack)
	if err != nil {
		return nil, fmt.Errorf("could not check whether a project owns this stack: %w", err)
	}
	if p == nil {
		return func() {}, nil
	}
	release, busy := beginProjectOp(p.ID, what)
	if release == nil {
		return nil, errProjectBusy(busy)
	}
	return release, nil
}

// projectOpOrConflict is beginProjectOp for a handler: it answers 409 itself
// when the project is busy.
func projectOpOrConflict(w http.ResponseWriter, projectID int64, what string) (release func(), ok bool) {
	release, busy := beginProjectOp(projectID, what)
	if release == nil {
		writeErr(w, http.StatusConflict, errProjectBusy(busy).Error())
		return nil, false
	}
	return release, true
}
