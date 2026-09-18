package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// domainLabelRE matches one DNS label: lowercase letters/digits/hyphens, no
// leading/trailing hyphen, 1-63 octets (the {0,61} bound plus the two
// required boundary characters caps the whole label at 63).
var domainLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// tldLabelRE is the final label's stricter shape: either purely alphabetic
// (an ordinary TLD) or a punycode ACE label ("xn--…", a real, ACME-usable
// FQDN for an internationalized domain, e.g. "app.xn--p1ai") — either way,
// never all-digits, so a bare IP literal (whose last "label" is all digits)
// still never validates as an FQDN.
var tldLabelRE = regexp.MustCompile(`^([a-z]{2,63}|xn--[a-z0-9-]{1,59})$`)

// validFQDN reports whether s is a syntactically valid, fully-qualified
// domain name: at least two labels, each obeying domainLabelRE, a total
// length of at most 253 octets (the real DNS name-length ceiling — RFC 1035
// — that a single label-level regex can't express), and an alphabetic final
// (TLD) label. Deliberately rejects a wildcard ("*.example.com") — the
// (future) embedded proxy's ACME issuance is tls-alpn-01 per exact hostname,
// the same constraint autocert.HostWhitelist already imposes on Docker
// Commander's own admin domain(s).
func validFQDN(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !domainLabelRE.MatchString(l) {
			return false
		}
	}
	return tldLabelRE.MatchString(labels[len(labels)-1])
}

// validDomainTLSModes is the only value Phase 1 accepts. "none" is a
// reserved-but-unimplemented value: see NEXT.md/the design doc for why it's
// deferred rather than allowed silently.
var validDomainTLSModes = map[string]bool{"acme": true}

// handleListDomainMappings lists a project's domain mappings. RBAC and
// project resolution are handled by loadProject, exactly like every other
// /projects/{id}/... route.
func (s *Server) handleListDomainMappings(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	mappings, err := s.store.ListDomainMappings(r.Context(), p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list domain mappings")
		return
	}
	writeJSON(w, http.StatusOK, mappings)
}

type domainMappingBody struct {
	Domain     string `json:"domain"`
	Service    string `json:"service"`
	TargetPort int    `json:"targetPort"`
	TLSMode    string `json:"tlsMode"`
}

// handleCreateDomainMapping records a new domain -> service:port mapping.
// This only stores intent — see internal/store/domain_mappings.go — nothing
// proxies traffic for it yet.
func (s *Server) handleCreateDomainMapping(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r) // POST is a write request; loadProject requires the write grant
	if !ok {
		return
	}
	var b domainMappingBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	b.Domain = strings.ToLower(strings.TrimSpace(b.Domain))
	if msg, ok := s.validateDomainMapping(r, p, b); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	id, err := s.store.CreateDomainMapping(r.Context(), p.ID, b.Domain, b.Service, b.TargetPort, b.TLSMode, currentUsername(r))
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "this domain is already mapped")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create domain mapping")
		return
	}
	s.audit(r, "project.domain.create", p.Slug, b.Domain)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// handleUpdateDomainMapping changes a mapping's target service/port/TLS
// mode. The domain itself is immutable — delete and recreate to repoint a
// hostname.
func (s *Server) handleUpdateDomainMapping(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "domainID"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid domain mapping id")
		return
	}
	var b domainMappingBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	// The domain field isn't updatable; validate everything else the same way
	// creation does; pass Domain through so validateDomainMapping still runs
	// its (non-domain) checks without needing a second, near-duplicate set.
	current, err := s.store.ListDomainMappings(r.Context(), p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load domain mapping")
		return
	}
	var existing *store.DomainMapping
	for i := range current {
		if current[i].ID == id {
			existing = &current[i]
			break
		}
	}
	if existing == nil {
		writeErr(w, http.StatusNotFound, "domain mapping not found")
		return
	}
	// A caller asking to change the domain itself must be told no, not have
	// the request silently succeed against the old hostname — 200 with the
	// request's own intent quietly discarded would read as "renamed" when
	// nothing moved.
	requestedDomain := strings.ToLower(strings.TrimSpace(b.Domain))
	if requestedDomain != "" && requestedDomain != existing.Domain {
		writeErr(w, http.StatusBadRequest, "domain is immutable; delete and recreate the mapping to repoint a hostname")
		return
	}
	b.Domain = existing.Domain
	if msg, ok := s.validateDomainMapping(r, p, b); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	err = s.store.UpdateDomainMapping(r.Context(), p.ID, id, b.Service, b.TargetPort, b.TLSMode)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "domain mapping not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update domain mapping")
		return
	}
	s.audit(r, "project.domain.update", p.Slug, existing.Domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteDomainMapping removes one domain mapping from a project.
func (s *Server) handleDeleteDomainMapping(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "domainID"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid domain mapping id")
		return
	}
	// Look the domain up before deleting so the audit detail is meaningful
	// (the id alone tells an operator nothing later).
	domain := ""
	if list, lerr := s.store.ListDomainMappings(r.Context(), p.ID); lerr == nil {
		for _, m := range list {
			if m.ID == id {
				domain = m.Domain
				break
			}
		}
	}
	err = s.store.DeleteDomainMapping(r.Context(), p.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "domain mapping not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete domain mapping")
		return
	}
	s.audit(r, "project.domain.delete", p.Slug, domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// validateDomainMapping checks a mapping body against everything Phase 1 can
// verify without a live listener: domain syntax, no collision with Docker
// Commander's own configured admin domain(s), a valid TLS mode, a positive
// target port, and — best-effort, only when the compose CLI is actually
// available — that Service names a real service in the project's current
// compose config. Returns (errorMessage, false) on the first failure.
func (s *Server) validateDomainMapping(r *http.Request, p *store.Project, b domainMappingBody) (string, bool) {
	if !validFQDN(b.Domain) {
		return "domain must be a valid FQDN (e.g. app.example.com); wildcards and IP literals aren't accepted", false
	}
	for _, own := range s.cfg.ACMEDomains {
		if strings.EqualFold(own, b.Domain) {
			return "this domain is already Docker Commander's own admin hostname", false
		}
	}
	if b.Service == "" {
		return "service is required", false
	}
	if b.TargetPort < 1 || b.TargetPort > 65535 {
		return "targetPort must be between 1 and 65535", false
	}
	if !validDomainTLSModes[b.TLSMode] {
		return "tlsMode must be one of: acme", false
	}
	services, err := s.resolvedComposeServices(r.Context(), p)
	if err != nil {
		return "", true // best-effort: can't verify the service exists, don't block on it
	}
	for _, svc := range services {
		if svc.Name == b.Service {
			return "", true
		}
	}
	return "service \"" + b.Service + "\" is not defined in this project's compose file", false
}

// resolvedComposeServices resolves every service the project's compose file
// declares, WITH EVERY PROFILE ENABLED — not just the default (no-profile)
// set. A plain `compose config` (nil profiles) silently omits a service
// gated behind an inactive profile (see ComposeConfigJSONFiles's own doc
// comment), which would otherwise make a perfectly valid `profiles: [admin]`
// service unmappable: rejected by this validation and absent from the
// frontend's service picker alike. Returns an error (not a partial result)
// when the compose CLI is unavailable or the file doesn't parse, so callers
// can decide whether that's fatal or best-effort-skippable.
func (s *Server) resolvedComposeServices(ctx context.Context, p *store.Project) ([]docker.ServiceSpec, error) {
	if !docker.ComposeAvailable(ctx) {
		return nil, errors.New("the `docker compose` CLI is not available on the host running Docker Commander")
	}
	dir := s.projectRoot(p.ID)
	_, masked, _, err := s.projectSecretEnvs(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	profiles, err := docker.ComposeProfilesEnv(ctx, dir, p.Slug, masked)
	if err != nil {
		// No profiles to enumerate is not fatal on its own — a compose file
		// with none declared, or a transient CLI hiccup — fall through and
		// resolve with the default (no-profile) set rather than failing outright.
		profiles = nil
	}
	cfgJSON, err := docker.ComposeConfigJSONFiles(ctx, dir, p.Slug, profiles, masked, nil)
	if err != nil {
		return nil, err
	}
	return docker.ParseComposeServices(cfgJSON)
}

// handleListDomainMappingServices returns the project's compose service
// names, every profile enabled, for the "Domains" panel's service picker —
// the exact same catalog handleCreateDomainMapping/handleUpdateDomainMapping
// validate Service against, so the picker never offers something the server
// would then reject.
func (s *Server) handleListDomainMappingServices(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	services, err := s.resolvedComposeServices(r.Context(), p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"services": []string{}, "error": err.Error()})
		return
	}
	names := make([]string, len(services))
	for i, svc := range services {
		names[i] = svc.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": names})
}
