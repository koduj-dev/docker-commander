package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDiagnosticsSection_Routing confirms /diagnostics/... is gated by the
// "diagnostics" section, and that the run endpoint is a write (POST) — a
// read-only account must not be able to trigger it, matching how /stats/ports
// (an equally "active" action against a host) is write-gated.
func TestDiagnosticsSection_Routing(t *testing.T) {
	if got := sectionForPath("/api/diagnostics/run"); got != "diagnostics" {
		t.Errorf(`sectionForPath("/api/diagnostics/run") = %q, want "diagnostics"`, got)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/diagnostics/run", nil)
	if !isWriteRequest(req) {
		t.Error("POST /api/diagnostics/run should be treated as a write")
	}
}

// PENTEST: holding an unrelated section (even one scoped to the right host)
// must not be enough to reach /diagnostics/run — it needs its OWN "diagnostics"
// grant. Without the sectionForPath mapping, this route falls through to
// "ungated", which only checks host-reachability across ANY section a user
// holds — so a "containers"-only user could otherwise run diagnostics on any
// host they can see containers on.
func TestPentestDiagnosticsSection_UnrelatedSectionDenied(t *testing.T) {
	srv, _, u := scopedFixture(t, "containers", 7)

	reached := false
	h := srv.permissions(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(200)
	}))

	r := httptest.NewRequest(http.MethodPost, "/api/diagnostics/run?host=7", nil).WithContext(ctxAs(u.ID, "user"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("SECURITY: a user with only \"containers\" reached diagnostics: %d %s", w.Code, w.Body.String())
	}
	if reached {
		t.Error("SECURITY: handler was reached without a \"diagnostics\" grant")
	}
}

// PENTEST: a role granting "diagnostics" but scoped to a different host must
// not reach /diagnostics/run for an out-of-scope host — the standard host-scope
// invariant (see host_scope_pentest_test.go), asserted for this new route so a
// future refactor of sectionForPath can't silently drop the gate for it.
func TestPentestDiagnosticsSection_OutOfScopeHostDenied(t *testing.T) {
	srv, _, u := scopedFixture(t, "diagnostics", 7)

	reached := false
	h := srv.permissions(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(200)
	}))

	for _, tc := range []struct {
		url  string
		want int
	}{
		{"/api/diagnostics/run?host=7", 200},
		{"/api/diagnostics/run?host=8", 403},
	} {
		reached = false
		r := httptest.NewRequest(http.MethodPost, tc.url, nil).WithContext(ctxAs(u.ID, "user"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.url, w.Code, tc.want)
		}
		if tc.want != 200 && reached {
			t.Errorf("SECURITY: %s reached the handler despite the %d", tc.url, tc.want)
		}
	}
}
