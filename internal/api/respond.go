package api

import (
	"encoding/json"
	"net/http"
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
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
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
