package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func (s *Server) handleListParseRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListParseRules(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list parse rules")
		return
	}
	if rules == nil {
		rules = []store.ParseRule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleCreateParseRule(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string `json:"name"`
		Pattern string `json:"pattern"`
	}
	if err := decodeJSON(r, &b); err != nil || b.Name == "" || b.Pattern == "" {
		writeErr(w, http.StatusBadRequest, "name and pattern are required")
		return
	}
	// The pattern is applied client-side with JS RegExp (named groups use the
	// (?<name>…) syntax Go's regexp can't compile), so it is validated there.
	id, err := s.store.CreateParseRule(r.Context(), b.Name, b.Pattern)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create parse rule")
		return
	}
	s.audit(r, "parse_rule.create", b.Name, "")
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleDeleteParseRule(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.DeleteParseRule(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete parse rule")
		return
	}
	s.audit(r, "parse_rule.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
