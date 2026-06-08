package api

import "testing"

func TestUpdateCheckerDisabled(t *testing.T) {
	u := newUpdateChecker("1.2.0", false)
	st := u.status(t.Context())
	if !st.Disabled {
		t.Error("disabled checker should report Disabled")
	}
	if st.UpdateAvailable {
		t.Error("disabled checker must never report an available update")
	}
	if st.Current != "1.2.0" {
		t.Errorf("Current = %q, want 1.2.0", st.Current)
	}
}
