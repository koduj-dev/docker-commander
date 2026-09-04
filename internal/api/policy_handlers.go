package api

import (
	"context"
	"fmt"
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
// A failure here fails CLOSED whenever a block-mode rule could have applied:
// if PolicyRuleModes itself errors, the mode of every rule is unknown, so it
// is treated as if a block-mode rule were active; once modes are known, a
// ComposeConfigJSONFiles or EvaluatePolicy failure only fails closed when at
// least one rule is actually in block mode. When every active rule is
// warn-only, the same failure still logs and returns err == nil (proceed) —
// a broken policy check must never itself block a deploy that carries no
// possibility of a hard block, but it must never silently wave through one
// that does.
func (s *Server) evaluateDeployPolicy(ctx context.Context, slug, dir string, profiles, env, files []string) (blocked, warned []docker.PolicyViolation, err error) {
	modes, err := s.store.PolicyRuleModes(ctx)
	if err != nil {
		log.Printf("policy check: load policy rules for %q: %v", slug, err)
		return nil, nil, fmt.Errorf("policy check: could not load policy rules: %w", err)
	}
	anyEnabled := false
	anyBlock := false
	ruleModes := make(map[docker.PolicyRuleID]docker.PolicyMode, len(modes))
	for id, mode := range modes {
		ruleModes[docker.PolicyRuleID(id)] = docker.PolicyMode(mode)
		if mode != string(docker.ModeOff) {
			anyEnabled = true
		}
		if mode == string(docker.ModeBlock) {
			anyBlock = true
		}
	}
	if !anyEnabled {
		return nil, nil, nil
	}

	configJSON, cerr := docker.ComposeConfigJSONFiles(ctx, dir, slug, profiles, env, files)
	if cerr != nil {
		log.Printf("policy check: resolve compose config for %q: %v", slug, cerr)
		if anyBlock {
			return nil, nil, fmt.Errorf("policy check: could not resolve compose config: %w", cerr)
		}
		return nil, nil, nil
	}
	violations, eerr := docker.EvaluatePolicy(configJSON, ruleModes)
	if eerr != nil {
		log.Printf("policy check: evaluate policy for %q: %v", slug, eerr)
		if anyBlock {
			return nil, nil, fmt.Errorf("policy check: could not evaluate policy: %w", eerr)
		}
		return nil, nil, nil
	}

	for _, v := range violations {
		switch v.Mode {
		case docker.ModeBlock:
			blocked = append(blocked, v)
		case docker.ModeWarn:
			warned = append(warned, v)
		}
	}
	return blocked, warned, nil
}

// policyCheckOrRefuse is evaluateDeployPolicy for an HTTP deploy-shaped
// caller (REST project deploy, revision restore): it audits under the
// caller's action family (kindDeploy or kindRestore — the audit action
// strings are written as literals below, not assembled at runtime, so the
// audit-doc consistency tests in auditdocs_test.go can find them) and
// returns (response, true) when the deploy must be refused — a block-mode
// violation (absolute, no per-deploy override), an un-confirmed warn-mode
// one, or an indeterminate policy check that could have hidden a block-mode
// violation — in which case the caller must write that response and return
// without deploying. (nil, false) means proceed.
const (
	policyKindDeploy  = "deploy"
	policyKindRestore = "restore"
)

func (s *Server) policyCheckOrRefuse(r *http.Request, p *store.Project, dir string, profiles, env, files []string, confirmed bool, kind string) (map[string]any, bool) {
	blocked, warned, err := s.evaluateDeployPolicy(r.Context(), p.Slug, dir, profiles, env, files)
	if err != nil {
		switch kind {
		case policyKindRestore:
			s.audit(r, "project.revision.restore.policy_check_failed", p.Slug, err.Error())
		default:
			s.audit(r, "project.deploy.policy_check_failed", p.Slug, err.Error())
		}
		return map[string]any{"ok": false, "policy": map[string]any{"error": "policy check failed; refusing to deploy for safety"}}, true
	}

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
