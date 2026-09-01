package api

import (
	"context"
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

// evaluateDeployPolicy resolves the EXACT compose model ComposeUpFiles would
// deploy — same profiles, env, and file list, including the COMPOSE_PROFILES
// neutralization ComposeUpFiles applies (see ComposeConfigJSONFiles) — and
// evaluates it against the configured policy rules. It is the single policy
// gate shared by every deploy path (REST deploy, MCP deploy, revision
// restore) so none of them can drift from what another one enforces.
//
// When every rule is off (the default — see store.PolicyRuleModes), this
// returns immediately without ever calling compose, so an operator who never
// touches policy settings pays zero extra latency or subprocess cost on
// every deploy.
//
// A PolicyRuleModes, ComposeConfigJSONFiles or EvaluatePolicy failure here is
// logged and treated as "proceed" (both return slices nil) — policy
// evaluation is advisory tooling on top of a deploy that `up` will
// independently validate anyway; a broken policy check must never itself
// become the reason a legitimate deploy is blocked.
func (s *Server) evaluateDeployPolicy(ctx context.Context, slug, dir string, profiles, env, files []string) (blocked, warned []docker.PolicyViolation) {
	modes, err := s.store.PolicyRuleModes(ctx)
	if err != nil {
		log.Printf("policy check: load policy rules for %q: %v", slug, err)
		return nil, nil
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
		return nil, nil
	}

	configJSON, err := docker.ComposeConfigJSONFiles(ctx, dir, slug, profiles, env, files)
	if err != nil {
		log.Printf("policy check: resolve compose config for %q: %v", slug, err)
		return nil, nil
	}
	violations, err := docker.EvaluatePolicy(configJSON, ruleModes)
	if err != nil {
		log.Printf("policy check: evaluate policy for %q: %v", slug, err)
		return nil, nil
	}

	for _, v := range violations {
		switch v.Mode {
		case docker.ModeBlock:
			blocked = append(blocked, v)
		case docker.ModeWarn:
			warned = append(warned, v)
		}
	}
	return blocked, warned
}

// policyCheckOrRefuse is evaluateDeployPolicy for an HTTP deploy-shaped
// caller (REST project deploy, revision restore): it audits under the
// caller's action family (kindDeploy or kindRestore — the audit action
// strings are written as literals below, not assembled at runtime, so the
// audit-doc consistency tests in auditdocs_test.go can find them) and
// returns (response, true) when the deploy must be refused — a block-mode
// violation (absolute, no per-deploy override) or an un-confirmed warn-mode
// one — in which case the caller must write that response and return
// without deploying. (nil, false) means proceed.
const (
	policyKindDeploy  = "deploy"
	policyKindRestore = "restore"
)

func (s *Server) policyCheckOrRefuse(r *http.Request, p *store.Project, dir string, profiles, env, files []string, confirmed bool, kind string) (map[string]any, bool) {
	blocked, warned := s.evaluateDeployPolicy(r.Context(), p.Slug, dir, profiles, env, files)

	if len(blocked) > 0 {
		switch kind {
		case policyKindRestore:
			s.audit(r, "project.revision.restore.policy_block", p.Slug, policyViolationSummary(blocked))
		default:
			s.audit(r, "project.deploy.policy_block", p.Slug, policyViolationSummary(blocked))
		}
		return map[string]any{"ok": false, "policy": map[string]any{"blocked": blocked}}, true
	}
	if len(warned) > 0 && !confirmed {
		return map[string]any{"ok": false, "policy": map[string]any{"warnings": warned}, "needsConfirmation": true}, true
	}
	if len(warned) > 0 {
		switch kind {
		case policyKindRestore:
			s.audit(r, "project.revision.restore.policy_warn_ack", p.Slug, policyViolationSummary(warned))
		default:
			s.audit(r, "project.deploy.policy_warn_ack", p.Slug, policyViolationSummary(warned))
		}
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
