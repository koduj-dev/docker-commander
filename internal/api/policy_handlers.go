package api

import (
	"log"
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// handleGetPolicyRules returns every known policy rule id and its currently
// configured mode (off/warn/block), defaulting an unset rule to "off".
func (s *Server) handleGetPolicyRules(w http.ResponseWriter, r *http.Request) {
	modes, err := s.store.PolicyRuleModes(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load policy rules")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rules": store.PolicyRuleIDs,
		"modes": modes,
	})
}

// handleSetPolicyRules persists a mode per rule. Unknown rule ids or mode
// values are silently dropped by the store rather than rejecting the whole
// update, matching handleSetSettings' tolerance for a partial body.
func (s *Server) handleSetPolicyRules(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetPolicyRuleModes(r.Context(), body); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save policy rules")
		return
	}
	s.audit(r, "policy.rules.update", "", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// policyCheckOrRefuse evaluates the project's compose config against the
// configured policy rules before a deploy is allowed to proceed. It returns
// (response, true) when the deploy must be refused — a block-mode violation
// (absolute, no per-deploy override) or an un-confirmed warn-mode one — in
// which case the caller must write that response and return without
// deploying. (nil, false) means proceed.
//
// When every rule is off (the default — see store.PolicyRuleModes), this
// skips straight to (nil, false) without ever calling ComposeConfigJSON, so
// an operator who never touches policy settings pays zero extra latency or
// subprocess cost on every deploy.
//
// A ComposeConfigJSON or EvaluatePolicy failure here is logged and treated as
// "proceed" — policy evaluation is advisory tooling on top of a deploy that
// `up` will independently validate anyway; a broken policy check must never
// itself become the reason a legitimate deploy is blocked.
func (s *Server) policyCheckOrRefuse(r *http.Request, p *store.Project, dir string, confirmed bool) (map[string]any, bool) {
	modes, err := s.store.PolicyRuleModes(r.Context())
	if err != nil {
		log.Printf("project deploy: load policy rules for %q: %v", p.Slug, err)
		return nil, false
	}
	anyEnabled := false
	ruleModes := make(map[docker.PolicyRuleID]docker.PolicyMode, len(modes))
	for id, mode := range modes {
		ruleModes[docker.PolicyRuleID(id)] = docker.PolicyMode(mode)
		if mode != string(docker.ModeOff) {
			anyEnabled = true
		}
	}
	if !anyEnabled {
		return nil, false
	}

	configJSON, err := docker.ComposeConfigJSON(r.Context(), dir, p.Slug)
	if err != nil {
		log.Printf("project deploy: resolve compose config for policy check on %q: %v", p.Slug, err)
		return nil, false
	}
	violations, err := docker.EvaluatePolicy(configJSON, ruleModes)
	if err != nil {
		log.Printf("project deploy: evaluate policy for %q: %v", p.Slug, err)
		return nil, false
	}

	var blocked, warned []docker.PolicyViolation
	for _, v := range violations {
		switch v.Mode {
		case docker.ModeBlock:
			blocked = append(blocked, v)
		case docker.ModeWarn:
			warned = append(warned, v)
		}
	}

	if len(blocked) > 0 {
		s.audit(r, "project.deploy.policy_block", p.Slug, policyViolationSummary(blocked))
		return map[string]any{"ok": false, "policy": map[string]any{"blocked": blocked}}, true
	}
	if len(warned) > 0 && !confirmed {
		return map[string]any{"ok": false, "policy": map[string]any{"warnings": warned}, "needsConfirmation": true}, true
	}
	if len(warned) > 0 {
		s.audit(r, "project.deploy.policy_warn_ack", p.Slug, policyViolationSummary(warned))
	}
	return nil, false
}

// policyViolationSummary renders violations as "rule:service, rule:service"
// for the audit log's free-text detail column.
func policyViolationSummary(violations []docker.PolicyViolation) string {
	parts := make([]string, len(violations))
	for i, v := range violations {
		parts[i] = string(v.Rule) + ":" + v.Service
	}
	return strings.Join(parts, ", ")
}
