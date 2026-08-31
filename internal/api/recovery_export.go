package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/koduj-dev/docker-commander/internal/passphrase"
)

// recoveryPassphraseHeader carries the bundle passphrase out of band from the
// request body/query string — a passphrase belongs in neither: query strings
// end up in access logs and proxies, and this endpoint's body is otherwise
// plain JSON options.
const recoveryPassphraseHeader = "X-Recovery-Passphrase"

// handleExportRecoveryBundle builds and streams a portable recovery bundle.
// Body: {"includeSecrets": bool, "projectIds": [int64]?}. includeSecrets and
// the bundle's own contents are always derived server-side from this
// request's own decoded body — never trust a client-echoed flag anywhere in
// the import path, which is what would let a client turn a
// secrets-excluded export into a secrets-included one after the fact.
func (s *Server) handleExportRecoveryBundle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IncludeSecrets bool    `json:"includeSecrets"`
		ProjectIDs     []int64 `json:"projectIds"`
	}
	_ = decodeJSON(r, &body) // an empty/absent body is a valid "export everything, no secrets"

	manifest, err := s.buildRecoveryManifest(r.Context(), body.IncludeSecrets, body.ProjectIDs, currentUsername(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mw, err := zw.Create("manifest.json")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := json.NewEncoder(mw).Encode(manifest); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, pm := range manifest.Projects {
		id, ok := s.projectIDBySlug(r.Context(), pm.Slug)
		if !ok {
			continue
		}
		if _, err := writeDirToZip(zw, s.projectRoot(id), "projects/"+pm.Slug+"/"); err != nil {
			writeErr(w, http.StatusInternalServerError, "packing project "+pm.Slug+": "+err.Error())
			return
		}
	}
	if err := zw.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	pass := r.Header.Get(recoveryPassphraseHeader)
	filename := "docker-commander-recovery-" + time.Now().UTC().Format("2006-01-02") + ".dcbundle"
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")

	var werr error
	if pass == "" {
		werr = passphrase.WritePlainTo(w, recoveryMagic, bytes.NewReader(buf.Bytes()))
	} else {
		werr = passphrase.SealTo(w, recoveryMagic, buf.Bytes(), pass)
	}
	if werr != nil {
		// Headers (and likely some body bytes) are already sent, so nothing
		// better can be done here than giving up — the client just gets a
		// truncated/corrupt download rather than a clean error response.
		return
	}

	detail := "secrets=no"
	if body.IncludeSecrets {
		detail = "secrets=yes"
	}
	s.audit(r, "recovery.export", strconv.Itoa(len(manifest.Projects)), detail)
}

// projectIDBySlug resolves a project's on-disk id from its slug, since the
// manifest (and the bundle's zip entry names) name projects by slug, not id.
func (s *Server) projectIDBySlug(ctx context.Context, slug string) (int64, bool) {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return 0, false
	}
	for _, p := range projects {
		if p.Slug == slug {
			return p.ID, true
		}
	}
	return 0, false
}

// buildRecoveryManifest assembles everything the bundle carries except the
// project files themselves (those are streamed straight into the zip by the
// caller, per project, to avoid holding every project's bytes twice).
func (s *Server) buildRecoveryManifest(ctx context.Context, includeSecrets bool, projectIDs []int64, exportedBy string) (*recoveryManifest, error) {
	hosts, hostNames, err := s.recoveryHosts(ctx, includeSecrets)
	if err != nil {
		return nil, err
	}
	registries, err := s.recoveryRegistries(ctx, includeSecrets)
	if err != nil {
		return nil, err
	}
	alertRules, webhooks, err := s.recoveryAlertRulesAndWebhooks(ctx, includeSecrets)
	if err != nil {
		return nil, err
	}
	settings, err := s.recoverySettings(ctx, includeSecrets)
	if err != nil {
		return nil, err
	}
	projects, err := s.recoveryProjects(ctx, projectIDs, hostNames)
	if err != nil {
		return nil, err
	}

	var networks, volumes []string
	if list, nerr := s.docker.ListNetworks(ctx, 0); nerr == nil {
		for _, n := range list {
			networks = append(networks, n.Name)
		}
	}
	if list, verr := s.docker.ListVolumes(ctx, 0); verr == nil {
		for _, v := range list {
			volumes = append(volumes, v.Name)
		}
	}

	return &recoveryManifest{
		Version:          recoveryBundleVersion,
		ExportedAt:       time.Now().UTC().Format(time.RFC3339),
		ExportedBy:       exportedBy,
		IncludesSecrets:  includeSecrets,
		Hosts:            hosts,
		Registries:       registries,
		AlertRules:       alertRules,
		Webhooks:         webhooks,
		Settings:         settings,
		Projects:         projects,
		NetworkInventory: networks,
		VolumeInventory:  volumes,
	}, nil
}

// recoveryHosts returns every non-local host as a recoveryHost, plus a
// name-by-id map for ALL hosts (including local) so callers can resolve a
// project's HostID to a name regardless of kind.
func (s *Server) recoveryHosts(ctx context.Context, includeSecrets bool) ([]recoveryHost, map[int64]string, error) {
	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	names := make(map[int64]string, len(hosts))
	out := make([]recoveryHost, 0, len(hosts))
	for _, h := range hosts {
		names[h.ID] = h.Name
		if h.Kind == "local" {
			// Every instance seeds its own local host (EnsureLocalHost); a
			// bundle carrying one would just collide with it on import.
			continue
		}
		rh := recoveryHost{
			Name: h.Name, Kind: h.Kind, Address: h.Address,
			TLSCA: h.TLSCA, TLSCert: h.TLSCert, HostKey: h.HostKey,
			AlertEmail: h.AlertEmail, Disabled: h.Disabled,
		}
		if includeSecrets {
			rh.TLSKey = h.TLSKey // store.ListHosts already returns this decrypted
		}
		out = append(out, rh)
	}
	return out, names, nil
}

func (s *Server) recoveryRegistries(ctx context.Context, includeSecrets bool) ([]recoveryRegistry, error) {
	regs, err := s.store.ListRegistries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]recoveryRegistry, 0, len(regs))
	for _, reg := range regs {
		rr := recoveryRegistry{Name: reg.Name, Address: reg.Address, Username: reg.Username}
		if includeSecrets {
			if auth, err := s.store.AuthByID(ctx, reg.ID); err == nil {
				rr.Password = auth.Password
			}
		}
		out = append(out, rr)
	}
	return out, nil
}

func (s *Server) recoveryAlertRulesAndWebhooks(ctx context.Context, includeSecrets bool) ([]portableRule, []recoveryWebhook, error) {
	rules, err := s.store.ListAlertRules(ctx)
	if err != nil {
		return nil, nil, err
	}
	hooks, err := s.store.ListWebhooks(ctx)
	if err != nil {
		return nil, nil, err
	}
	nameByID := make(map[int64]string, len(hooks))
	for _, h := range hooks {
		nameByID[h.ID] = h.Name
	}

	out := make([]portableRule, 0, len(rules))
	for _, rule := range rules {
		cfg := rule.Config
		if cfg == "" {
			cfg = "{}"
		}
		pr := portableRule{
			Name: rule.Name, Enabled: rule.Enabled, Type: rule.Type, Target: rule.Target,
			Config: []byte(cfg), Severity: rule.Severity, Email: rule.Email, CooldownSec: rule.CooldownSec,
		}
		if rule.WebhookID != nil {
			pr.Webhook = nameByID[*rule.WebhookID]
		}
		out = append(out, pr)
	}

	var webhooks []recoveryWebhook
	if includeSecrets {
		webhooks = make([]recoveryWebhook, 0, len(hooks))
		for _, h := range hooks {
			webhooks = append(webhooks, recoveryWebhook{
				Name: h.Name, URL: h.URL, Method: h.Method,
				Headers: h.Headers, BodyTemplate: h.BodyTemplate,
			})
		}
	}
	return out, webhooks, nil
}

func (s *Server) recoverySettings(ctx context.Context, includeSecrets bool) (recoverySettings, error) {
	disabled, err := s.store.DisabledSections(ctx)
	if err != nil {
		return recoverySettings{}, err
	}
	no2fa, err := s.store.LocalhostNo2FA(ctx)
	if err != nil {
		return recoverySettings{}, err
	}
	out := recoverySettings{DisabledSections: disabled, LocalhostNo2FA: no2fa}

	if smtp, err := s.store.GetSMTP(ctx); err == nil && smtp.Host != "" {
		rs := &recoverySMTP{Host: smtp.Host, Port: smtp.Port, Username: smtp.Username, From: smtp.From, To: smtp.To, TLS: smtp.TLS}
		if includeSecrets {
			rs.Password = smtp.Password
		}
		out.SMTP = rs
	}
	if ldap, err := s.store.GetLDAP(ctx); err == nil && ldap.URL != "" {
		rl := &recoveryLDAPConf{
			Enabled: ldap.Enabled, URL: ldap.URL, StartTLS: ldap.StartTLS, BindDN: ldap.BindDN,
			UserBaseDN: ldap.UserBaseDN, UserFilter: ldap.UserFilter, AdminGroupDN: ldap.AdminGroupDN,
		}
		if includeSecrets {
			rl.BindPassword = ldap.BindPassword
		}
		out.LDAP = rl
	}
	return out, nil
}

func (s *Server) recoveryProjects(ctx context.Context, projectIDs []int64, hostNames map[int64]string) ([]recoveryProjectMeta, error) {
	all, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var want map[int64]bool
	if len(projectIDs) > 0 {
		want = make(map[int64]bool, len(projectIDs))
		for _, id := range projectIDs {
			want[id] = true
		}
	}
	out := make([]recoveryProjectMeta, 0, len(all))
	for i := range all {
		p := all[i]
		if want != nil && !want[p.ID] {
			continue
		}
		if len(out) >= maxBundleProjects {
			break
		}
		var images []recoveryProjectImage
		for _, img := range s.captureRevisionImages(ctx, &p) {
			images = append(images, recoveryProjectImage{Service: img.Service, Image: img.Image, Digest: img.Digest})
		}
		out = append(out, recoveryProjectMeta{
			Slug: p.Slug, Name: p.Name, ComposeFile: p.ComposeFile,
			HostName: hostNames[p.HostID], AllowRemoteHostPaths: p.AllowRemoteHostPaths,
			LastDeployedProfiles: p.LastDeployedProfiles, Images: images,
		})
	}
	return out, nil
}
