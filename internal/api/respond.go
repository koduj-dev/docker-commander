package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// writeJSON serialises v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeErr sends a JSON error envelope: {"error": "..."}.
//
// The package prefix is stripped on the way out. Go convention puts one on every
// error ("auth: this account already has the maximum number of authenticators"),
// which is right for a log and wrong on a screen — the sentence is written for the
// person reading it, and "auth:" is an implementation detail leaking into the UI.
// It showed up as `auth: passkeys need HTTPS (or localhost)` under a greyed-out
// button, which is where it was noticed.
//
// An allowlist rather than "anything before the first colon": messages legitimately
// contain colons ("cannot reach host: connection refused"), and eating half of one
// of those would be worse than the prefix.
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": stripPackagePrefix(msg)})
}

// ourPackagePrefixes are the ones this app puts on its own errors.
var ourPackagePrefixes = []string{"auth: ", "store: ", "docker: ", "monitor: "}

func stripPackagePrefix(msg string) string {
	for _, p := range ourPackagePrefixes {
		if after, ok := strings.CutPrefix(msg, p); ok {
			return after
		}
	}
	return msg
}

// maxRequestBody caps a request body. Named because a few endpoints hand the body
// to a library that reads it itself and so have to apply the cap by hand.
const maxRequestBody = 1 << 20

// decodeJSON reads a JSON request body into v, capping the size to guard
// against oversized payloads.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
