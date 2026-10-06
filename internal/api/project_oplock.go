package api

import (
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

// errProjectBusy is the refusal for an operation that found the project busy.
func errProjectBusy(busy string) error {
	return fmt.Errorf("this project is busy (%s is running); try again when it finishes", busy)
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
