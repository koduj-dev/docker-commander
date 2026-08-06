package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/auth"
)

// A Go error carries a package prefix so a log line says where it came from. On a
// screen it is noise, and it reached one: `auth: passkeys need HTTPS (or
// localhost)` appeared under the greyed-out "Add a passkey" button, which is how
// this was noticed at all — in a screenshot being added to the manual.
//
// Stripped by allowlist, not by "everything before the first colon": messages
// legitimately contain colons, and eating half of one is worse than the prefix.
func TestErrorMessagesLoseTheirPackagePrefix(t *testing.T) {
	for msg, want := range map[string]string{
		"auth: passkeys need HTTPS (or localhost)":              "passkeys need HTTPS (or localhost)",
		"store: that is the only second factor on this account": "that is the only second factor on this account",
		// Left alone: not one of ours, and the colon is part of the sentence.
		"cannot reach host: connection refused": "cannot reach host: connection refused",
		"invalid body":                          "invalid body",
		// Only a LEADING prefix goes; one in the middle is content.
		"could not read auth: token": "could not read auth: token",
	} {
		if got := stripPackagePrefix(msg); got != want {
			t.Errorf("stripPackagePrefix(%q) = %q, want %q", msg, got, want)
		}
	}
}

// …and it happens on the way out, so every handler gets it without remembering to.
func TestWriteErrStripsThePrefix(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusForbidden, auth.ErrPasskeyUnavailable.Error())

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(body["error"], "auth:") {
		t.Errorf("the package prefix reached the client: %q", body["error"])
	}
	if !strings.Contains(body["error"], "HTTPS") {
		t.Errorf("the message itself was lost: %q", body["error"])
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
