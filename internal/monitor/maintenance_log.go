package monitor

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// maintenanceLogInterval is how often the set of active maintenance windows is
// compared with the previous one. A window is only ever "active" by clock
// (a recurring one starts without anything happening), so the transitions can
// only be seen by looking.
const maintenanceLogInterval = 30 * time.Second

// activeWindow is what the last pass knew about a window that was active.
type activeWindow struct {
	name  string
	start time.Time
	end   time.Time
}

func (m *Monitor) maintenanceLogLoop(ctx context.Context) {
	t := time.NewTicker(maintenanceLogInterval)
	defer t.Stop()
	var known map[int64]activeWindow
	first := true
	for {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		windows, err := m.store.ListMaintenanceWindows(wctx)
		cancel()
		if err != nil {
			log.Printf("monitor: list maintenance windows: %v", err)
		} else {
			known = logMaintenanceTransitions(known, windows, time.Now(), first)
			first = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Times are logged in the server's local zone, matching the log line's own timestamp.
//
// logMaintenanceTransitions writes a line for every window that became active
// or stopped being active since prev, and returns the new active set. On the
// first pass (startup) a window that is already running is reported as such,
// with its real start, rather than as a fresh start. Written to the process log
// — journal / syslog — because alerts are silenced silently otherwise.
func logMaintenanceTransitions(prev map[int64]activeWindow, windows []store.MaintenanceWindow, now time.Time, first bool) map[int64]activeWindow {
	cur := make(map[int64]activeWindow)
	byID := make(map[int64]store.MaintenanceWindow, len(windows))
	for _, w := range windows {
		byID[w.ID] = w
		if start, end, ok := w.ActiveSpan(now); ok {
			cur[w.ID] = activeWindow{name: w.Name, start: start, end: end}
		}
	}
	// Same window AND same occurrence. Keying on the id alone missed the boundary
	// of back-to-back occurrences of one recurring window (a daily 24 h window is
	// allowed): the id never leaves the set, so no end/start pair was logged.
	same := func(a, b activeWindow) bool { return a.start.Equal(b.start) && a.end.Equal(b.end) }
	for id, a := range cur {
		if p, was := prev[id]; was && same(p, a) {
			continue
		}
		w := byID[id]
		if first {
			log.Printf("maintenance window active id=%d name=%q scope=%q since=%s until=%s remaining=%s — matching alerts are recorded but not delivered",
				id, a.name, windowScope(w), a.start.Local().Format(time.RFC3339), a.end.Local().Format(time.RFC3339), a.end.Sub(now).Round(time.Second))
		} else {
			log.Printf("maintenance window started id=%d name=%q scope=%q until=%s duration=%s — matching alerts are recorded but not delivered",
				id, a.name, windowScope(w), a.end.Local().Format(time.RFC3339), a.end.Sub(a.start).Round(time.Second))
		}
	}
	for id, a := range prev {
		if c, still := cur[id]; still && same(c, a) {
			continue
		}
		reason := "ended"
		switch w, exists := byID[id]; {
		case !exists:
			reason = "removed"
		case w.Ended:
			reason = "ended early"
		}
		log.Printf("maintenance window %s id=%d name=%q after=%s — alert delivery resumes", reason, id, a.name, now.Sub(a.start).Round(time.Second))
	}
	return cur
}

// windowScope renders what a window covers, "everything" when it has no filter.
func windowScope(w store.MaintenanceWindow) string {
	var parts []string
	if len(w.HostIDs) > 0 {
		hs := make([]string, len(w.HostIDs))
		for i, h := range w.HostIDs {
			hs[i] = fmt.Sprintf("host#%d", h)
		}
		parts = append(parts, strings.Join(hs, ","))
	}
	if w.Project != "" {
		parts = append(parts, "project~"+w.Project)
	}
	if w.Container != "" {
		parts = append(parts, "container~"+w.Container)
	}
	if w.RuleID != nil {
		parts = append(parts, fmt.Sprintf("rule#%d", *w.RuleID))
	}
	if len(w.Severities) > 0 {
		parts = append(parts, strings.Join(w.Severities, "/"))
	}
	if len(parts) == 0 {
		return "everything"
	}
	return strings.Join(parts, " ")
}
