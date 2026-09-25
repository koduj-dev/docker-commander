package store

import (
	"testing"
	"time"
)

var sumT0 = time.Date(2026, 9, 25, 13, 13, 58, 0, time.UTC)

// ev inserts one event at sumT0+offset. duration is the engine's own
// "seconds since the incident started" (0 for a firing).
func ev(t *testing.T, st *Store, id int64, rule int64, container, kind string, offset time.Duration, duration int) {
	t.Helper()
	mustExec(t, st, `INSERT INTO alert_events (id, rule_id, rule_name, container_id, host_id, kind, duration_sec, created_at)
		VALUES (?, ?, 'r', ?, 0, ?, ?, ?)`, id, rule, container, kind, duration, sumT0.Add(offset).Format(time.RFC3339))
}

func summaries(t *testing.T, st *Store) map[int64]AlertEvent {
	t.Helper()
	evs, _, err := st.ListAlertEvents(t.Context(), AlertQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]AlertEvent{}
	for _, e := range evs {
		out[e.ID] = e
	}
	return out
}

// The feed hides repeats by default, so the row that OPENED a condition has to
// say that it is still going on and how often it repeated.
func TestFiringRowSummarisesItsRepeats(t *testing.T) {
	st, _ := retStore(t)
	ev(t, st, 1, 7, "c1", "firing", 0, 0)
	for i := 1; i <= 3; i++ {
		ev(t, st, int64(1+i), 7, "c1", "repeat", time.Duration(i)*time.Minute, i*60)
	}
	ev(t, st, 10, 7, "c2", "repeat", time.Minute, 60) // another container: never counted

	got := summaries(t, st)
	f := got[1]
	if f.Repeats != 3 || !f.Ongoing {
		t.Errorf("firing row: repeats=%d ongoing=%v, want 3 and ongoing", f.Repeats, f.Ongoing)
	}
	if f.LastRepeatAt == nil || !f.LastRepeatAt.Equal(sumT0.Add(3*time.Minute)) {
		t.Errorf("lastRepeatAt = %v, want the newest repeat", f.LastRepeatAt)
	}
	if r := got[2]; r.Repeats != 0 || r.Ongoing || r.LastRepeatAt != nil {
		t.Errorf("a repeat row carries no summary of its own: %+v", r)
	}
}

func TestResolvedConditionIsNotOngoingAndTheNextOneStartsFresh(t *testing.T) {
	st, _ := retStore(t)
	ev(t, st, 1, 7, "c1", "firing", 0, 0)
	ev(t, st, 2, 7, "c1", "repeat", time.Minute, 60)
	ev(t, st, 3, 7, "c1", "repeat", 2*time.Minute, 120)
	ev(t, st, 4, 7, "c1", "resolved", 3*time.Minute, 180)
	// A second incident, same rule and container.
	ev(t, st, 5, 7, "c1", "firing", time.Hour, 0)
	ev(t, st, 6, 7, "c1", "repeat", time.Hour+time.Minute, 60)

	got := summaries(t, st)
	if f := got[1]; f.Repeats != 2 || f.Ongoing {
		t.Errorf("first incident: repeats=%d ongoing=%v, want 2 and over", f.Repeats, f.Ongoing)
	}
	if f := got[5]; f.Repeats != 1 || !f.Ongoing {
		t.Errorf("second incident must not inherit the first one's repeats: repeats=%d ongoing=%v", f.Repeats, f.Ongoing)
	}
}

// Rule A fires, rule B (higher severity, same metric) takes over, B repeats and
// B resolves. Rule A's firing row must not stay "ongoing" forever just because
// its own rule never got a resolved event.
func TestEscalationHandsOverAndTheResolveEndsTheWholeIncident(t *testing.T) {
	st, _ := retStore(t)
	ev(t, st, 1, 1, "c1", "firing", 0, 0)                 // rule A
	ev(t, st, 2, 2, "c1", "escalated", time.Minute, 60)   // rule B, same incident
	ev(t, st, 3, 2, "c1", "repeat", 2*time.Minute, 120)   // B repeats
	ev(t, st, 4, 2, "c1", "resolved", 3*time.Minute, 180) // B resolves; incident started at t0

	got := summaries(t, st)
	if a := got[1]; a.Ongoing {
		t.Errorf("rule A's firing row belongs to a resolved incident: %+v", a)
	}
	if b := got[2]; b.Repeats != 1 || b.Ongoing {
		t.Errorf("escalated row: repeats=%d ongoing=%v, want 1 and over", b.Repeats, b.Ongoing)
	}
	// A different incident on the same container must not be ended by it.
	ev(t, st, 5, 1, "c1", "firing", time.Hour, 0)
	if a := summaries(t, st)[5]; !a.Ongoing {
		t.Error("an unrelated, later incident is still ongoing")
	}
}
