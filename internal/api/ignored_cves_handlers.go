package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// maxIgnoreCVEsPerRequest bounds one bulk-ignore submission — the UI selects
// from a single scan's results, which maxScanVulns already caps well under
// this, so the limit only ever bites a hand-crafted request.
const maxIgnoreCVEsPerRequest = 500

func (s *Server) handleListIgnoredCVEs(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListIgnoredCVEs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list ignored CVEs")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleIgnoreCVEs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []string `json:"ids"`
		Reason string   `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(body.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids is required")
		return
	}
	if len(body.IDs) > maxIgnoreCVEsPerRequest {
		writeErr(w, http.StatusBadRequest, "too many CVE ids in one request")
		return
	}
	if err := s.store.IgnoreCVEs(r.Context(), body.IDs, strings.TrimSpace(body.Reason), currentUsername(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save the ignore list")
		return
	}
	s.audit(r, "image.cve.ignore", strings.Join(body.IDs, ","), body.Reason)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUnignoreCVE(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.store.UnignoreCVE(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not remove the ignore entry")
		return
	}
	s.audit(r, "image.cve.unignore", id, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
