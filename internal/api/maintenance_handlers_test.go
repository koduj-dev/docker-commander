package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func validRecurringBody() maintenanceWindowBody {
	return maintenanceWindowBody{
		Name: "w", Reason: "r", Recurring: true,
		Weekdays: []time.Weekday{time.Sunday}, TimeOfDay: "02:00", DurationMin: 60,
		StartsAt: time.Now(),
	}
}

func TestValidateMaintenanceWindowRejectsOutOfRangeWeekday(t *testing.T) {
	b := validRecurringBody()
	b.Weekdays = []time.Weekday{7}
	if err := validateMaintenanceWindow(b); err == nil {
		t.Fatal("a weekday of 7 (valid range is 0-6) should be rejected — it can never match a real date")
	}
}

func TestValidateMaintenanceWindowRejectsUnknownSeverity(t *testing.T) {
	b := validRecurringBody()
	b.Severities = []string{"urgent"}
	if err := validateMaintenanceWindow(b); err == nil {
		t.Fatal("an unknown severity should be rejected — it can never match a real event")
	}
}

func TestValidateMaintenanceWindowRejectsExcessiveRecurringDuration(t *testing.T) {
	b := validRecurringBody()
	b.DurationMin = 25 * 60 // > 24h
	if err := validateMaintenanceWindow(b); err == nil {
		t.Fatal("a recurring occurrence longer than 24h should be rejected — recurringActiveAt's lookback assumes this bound")
	}
}

func TestValidateMaintenanceWindowAcceptsExactly24Hours(t *testing.T) {
	b := validRecurringBody()
	b.DurationMin = 24 * 60
	if err := validateMaintenanceWindow(b); err != nil {
		t.Fatalf("exactly 24h should be accepted (the documented bound), got %v", err)
	}
}

// newMaintenanceServer builds an unrestricted (admin) server + store, for
// tests that aren't specifically about host scoping.
func newMaintenanceServer(t *testing.T) (*Server, *store.Store, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	uid, err := st.CreateUser(t.Context(), &store.User{Username: "root", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: config.Config{}, store: st}, st, uid
}

// TestCreateMaintenanceWindowRecurringWithoutSeriesEndSucceeds is the fix for
// a real bug: the browser previously sent `endsAt: ""` for an indefinite
// recurring series, and time.Time's JSON decoder rejects an empty string
// (only a missing key or literal null leaves it at its zero value). The
// fixed client omits the key entirely — this proves the server accepts that.
func TestCreateMaintenanceWindowRecurringWithoutSeriesEndSucceeds(t *testing.T) {
	srv, st, uid := newMaintenanceServer(t)
	body := `{"name":"w","reason":"r","hostIds":[],"project":"","container":"","ruleId":null,"severities":[],` +
		`"recurring":true,"startsAt":"` + time.Now().Format(time.RFC3339) + `",` +
		`"weekdays":[0],"timeOfDay":"02:00","durationMin":60,"timezone":""}`
	r := httptest.NewRequest("POST", "/api/maintenance-windows", strings.NewReader(body)).WithContext(ctxAs(uid, "admin"))
	w := httptest.NewRecorder()
	srv.handleCreateMaintenanceWindow(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("indefinite recurring window (no endsAt key): want 200, got %d (%s)", w.Code, w.Body)
	}

	windows, err := st.ListMaintenanceWindows(t.Context())
	if err != nil || len(windows) != 1 {
		t.Fatalf("expected one window, got %d err=%v", len(windows), err)
	}
	if !windows[0].EndsAt.IsZero() {
		t.Errorf("an omitted endsAt should leave the series open-ended (zero time), got %v", windows[0].EndsAt)
	}
}

// TestListMaintenanceWindowsOmitsEndsAtForAnOpenEndedSeries is the response
// side of the same bug: a zero EndsAt must not serialize as Go's
// "0001-01-01T00:00:00Z" sentinel, which the UI would show as a real
// (bogus) series end date.
func TestListMaintenanceWindowsOmitsEndsAtForAnOpenEndedSeries(t *testing.T) {
	srv, st, uid := newMaintenanceServer(t)
	if _, err := st.CreateMaintenanceWindow(t.Context(), &store.MaintenanceWindow{
		Name: "open-ended", Reason: "r", AuthorID: uid, Recurring: true,
		Weekdays: []time.Weekday{time.Sunday}, TimeOfDay: "02:00", DurationMin: 60,
		StartsAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/maintenance-windows", nil).WithContext(ctxAs(uid, "admin"))
	w := httptest.NewRecorder()
	srv.handleListMaintenanceWindows(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "0001-01-01") {
		t.Errorf("an open-ended series' endsAt should be omitted, not the zero-time sentinel: %s", w.Body.String())
	}
}

// TestListMaintenanceWindowsSerializesEmptyScopeAsArraysNotNull is the fix
// for a real bug: unmarshalIDs/unmarshalSections return a nil slice for an
// unset scope, and a nil Go slice marshals to JSON null rather than [].
// The frontend type promises an array and calls .length on it
// unconditionally, so a null here crashed the whole Maintenance tab — and
// an unrestricted scope is the COMMON case, not a corner one: every
// auto-silence-after-deploy window has no severity restriction at all.
// This asserts on the literal response bytes, not a decoded Go struct,
// because decoding null back into a Go slice silently produces nil again
// and would have hidden the bug.
func TestListMaintenanceWindowsSerializesEmptyScopeAsArraysNotNull(t *testing.T) {
	srv, st, uid := newMaintenanceServer(t)
	if _, err := st.CreateMaintenanceWindow(t.Context(), &store.MaintenanceWindow{
		Name: "auto: shop deploy", Reason: "automatic grace period after a deploy", AuthorID: uid,
		HostIDs:  []int64{0}, // scoped by host, but NOT by severity — the auto-silence shape
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/maintenance-windows", nil).WithContext(ctxAs(uid, "admin"))
	w := httptest.NewRecorder()
	srv.handleListMaintenanceWindows(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, `"severities":null`) {
		t.Errorf("SECURITY-ADJACENT (crashes the UI): severities serialized as null, not []: %s", body)
	}
	if !strings.Contains(body, `"severities":[]`) {
		t.Errorf("expected an explicit empty array for severities: %s", body)
	}
}

// TestCreateMaintenanceWindowNormalizesTheLocalHostAlias is the REST half of
// the local-host normalization fix: a host-restricted grant that reaches the
// local daemon must be able to scope a window using either the 0 alias OR
// the local host's own real seeded-row id, and both must end up stored the
// same (canonical) way.
func TestCreateMaintenanceWindowNormalizesTheLocalHostAlias(t *testing.T) {
	srv, st, uid := newMaintenanceServer(t)
	ctx := t.Context()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	hosts, err := st.ListHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("expected the seeded local host, got %d err=%v", len(hosts), err)
	}
	realLocalID := hosts[0].ID

	body := `{"name":"w","reason":"r","hostIds":[` + itoa(realLocalID) + `],"project":"","container":"","ruleId":null,` +
		`"severities":[],"recurring":false,"startsAt":"` + time.Now().Format(time.RFC3339) +
		`","endsAt":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`
	r := httptest.NewRequest("POST", "/api/maintenance-windows", strings.NewReader(body)).WithContext(ctxAs(uid, "admin"))
	w := httptest.NewRecorder()
	srv.handleCreateMaintenanceWindow(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("create with the real local host id: want 200, got %d (%s)", w.Code, w.Body)
	}

	windows, err := st.ListMaintenanceWindows(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("expected one window, got %d err=%v", len(windows), err)
	}
	if len(windows[0].HostIDs) != 1 || windows[0].HostIDs[0] != 0 {
		t.Errorf("the real local host id should be normalized to the 0 alias when stored, got %v", windows[0].HostIDs)
	}
}
