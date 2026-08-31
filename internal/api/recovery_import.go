package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/passphrase"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// parseRecoveryBundle validates and decodes an uploaded bundle: the
// passphrase envelope (internal/passphrase), then the zip inside it, then
// its manifest.json. It never writes anything — both inspect and import call
// it first, and only import goes on to touch the store/filesystem.
func parseRecoveryBundle(data []byte, pass string) (*recoveryManifest, *zip.Reader, error) {
	r := bytes.NewReader(data)
	encrypted, err := passphrase.ReadFlag(r, recoveryMagic)
	if err != nil {
		return nil, nil, errors.New("not a Docker Commander recovery bundle")
	}
	var zipBytes []byte
	if encrypted {
		plain, oerr := passphrase.OpenFrom(r, recoveryMagic, pass)
		if oerr != nil {
			return nil, nil, oerr // may be passphrase.ErrPassphraseRequired
		}
		zipBytes = plain
	} else {
		rest, rerr := readAllLimit(r, maxRecoveryUploadBytes)
		if rerr != nil {
			return nil, nil, rerr
		}
		zipBytes = rest
	}

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, nil, errors.New("not a valid bundle archive")
	}
	mf, err := zr.Open("manifest.json")
	if err != nil {
		return nil, nil, errors.New("bundle has no manifest")
	}
	defer mf.Close()

	var manifest recoveryManifest
	dec := json.NewDecoder(mf)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, nil, errors.New("invalid or unrecognised manifest")
	}
	if manifest.Version != recoveryBundleVersion {
		return nil, nil, fmt.Errorf("unsupported bundle version %d", manifest.Version)
	}
	if len(manifest.Projects) > maxBundleProjects {
		return nil, nil, fmt.Errorf("bundle carries too many projects (max %d)", maxBundleProjects)
	}
	return &manifest, zr, nil
}

// compatibilityReport is what handleInspectRecoveryBundle returns: what an
// import against the given target would find missing, without writing
// anything.
type compatibilityReport struct {
	MissingImages   []string `json:"missingImages"`
	MissingVolumes  []string `json:"missingVolumes"`
	UnknownHosts    []string `json:"unknownHosts"`
	SecretsExcluded bool     `json:"secretsExcluded"`
	Warnings        []string `json:"warnings"`
}

// buildCompatibilityReport checks a manifest against the target host's
// current state — every check here is read-only.
func buildCompatibilityReport(ctx context.Context, s *Server, m *recoveryManifest, hostID int64) compatibilityReport {
	rep := compatibilityReport{SecretsExcluded: !m.IncludesSecrets}

	knownHostNames := map[string]bool{}
	if hosts, err := s.store.ListHosts(ctx); err == nil {
		for _, h := range hosts {
			knownHostNames[h.Name] = true
		}
	}
	for _, h := range m.Hosts {
		knownHostNames[h.Name] = true // would exist after import
	}
	seenUnknown := map[string]bool{}
	for _, p := range m.Projects {
		if p.HostName != "" && !knownHostNames[p.HostName] && !seenUnknown[p.HostName] {
			seenUnknown[p.HostName] = true
			rep.UnknownHosts = append(rep.UnknownHosts, p.HostName)
		}
	}

	localImages := map[string]bool{}
	if images, err := s.docker.ListImages(ctx, hostID); err == nil {
		for _, img := range images {
			for _, t := range img.RepoTags {
				localImages[t] = true
			}
			for _, d := range img.RepoDigests {
				localImages[d] = true
			}
		}
	}
	seenMissingImage := map[string]bool{}
	for _, p := range m.Projects {
		for _, img := range p.Images {
			ref := img.Image
			if img.Digest != "" {
				if i := strings.IndexByte(ref, '@'); i >= 0 {
					ref = ref[:i]
				}
				ref = ref + "@" + img.Digest
			}
			if ref == "" || localImages[ref] || seenMissingImage[ref] {
				continue
			}
			seenMissingImage[ref] = true
			rep.MissingImages = append(rep.MissingImages, ref)
		}
	}

	localVolumes := map[string]bool{}
	if volumes, err := s.docker.ListVolumes(ctx, hostID); err == nil {
		for _, v := range volumes {
			localVolumes[v.Name] = true
		}
	}
	for _, name := range m.VolumeInventory {
		if !localVolumes[name] {
			rep.MissingVolumes = append(rep.MissingVolumes, name)
		}
	}

	if rep.SecretsExcluded && (len(m.Registries) > 0 || len(m.Hosts) > 0) {
		rep.Warnings = append(rep.Warnings, "this bundle excludes secrets — registry passwords and host TLS keys will need to be re-entered after restore")
	}
	return rep
}

// handleInspectRecoveryBundle uploads a bundle and reports its compatibility
// with the target host, without writing anything. Body: the raw bundle
// bytes; passphrase via the same header as export.
func (s *Server) handleInspectRecoveryBundle(w http.ResponseWriter, r *http.Request) {
	hostID, err := hostParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid host")
		return
	}
	data, err := readAllLimit(streamingBody(w, r), maxRecoveryUploadBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read upload")
		return
	}
	manifest, _, err := parseRecoveryBundle(data, r.Header.Get(recoveryPassphraseHeader))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rep := buildCompatibilityReport(r.Context(), s, manifest, hostID)
	s.audit(r, "recovery.inspect", strconv.Itoa(len(manifest.Projects)), "")
	writeJSON(w, http.StatusOK, map[string]any{
		"manifest": map[string]any{
			"version":         manifest.Version,
			"exportedAt":      manifest.ExportedAt,
			"exportedBy":      manifest.ExportedBy,
			"includesSecrets": manifest.IncludesSecrets,
			"hosts":           len(manifest.Hosts),
			"registries":      len(manifest.Registries),
			"alertRules":      len(manifest.AlertRules),
			"projects":        len(manifest.Projects),
		},
		"compatibility": rep,
	})
}

// importSummary reports what handleImportRecoveryBundle actually did.
type importSummary struct {
	HostsCreated      int `json:"hostsCreated"`
	RegistriesCreated int `json:"registriesCreated"`
	WebhooksCreated   int `json:"webhooksCreated"`
	AlertRulesCreated int `json:"alertRulesCreated"`
	ProjectsCreated   int `json:"projectsCreated"`
}

// handleImportRecoveryBundle applies a bundle: hosts/registries/webhooks/
// alert rules/projects are created new or skipped by name/slug collision —
// never overwritten (the same rule alert_portability.go already applies to
// its own, narrower import). Instance-wide settings are only applied when
// applySettings=true is explicitly passed; the default is to leave a live
// instance's settings alone even if the bundle carries them.
func (s *Server) handleImportRecoveryBundle(w http.ResponseWriter, r *http.Request) {
	hostID, err := hostParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid host")
		return
	}
	applySettings := strings.EqualFold(r.URL.Query().Get("applySettings"), "true")

	data, err := readAllLimit(streamingBody(w, r), maxRecoveryUploadBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read upload")
		return
	}
	manifest, zr, err := parseRecoveryBundle(data, r.Header.Get(recoveryPassphraseHeader))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	summary, warnings, err := s.applyRecoveryBundle(r.Context(), manifest, zr, hostID, applySettings, currentUsername(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "recovery.import", strconv.Itoa(summary.ProjectsCreated), fmt.Sprintf("hosts=%d registries=%d rules=%d", summary.HostsCreated, summary.RegistriesCreated, summary.AlertRulesCreated))
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "warnings": warnings})
}

func (s *Server) applyRecoveryBundle(ctx context.Context, m *recoveryManifest, zr *zip.Reader, defaultHostID int64, applySettings bool, createdBy string) (importSummary, []string, error) {
	var summary importSummary
	var warnings []string

	existingHosts, err := s.store.ListHosts(ctx)
	if err != nil {
		return summary, nil, err
	}
	hostIDByName := make(map[string]int64, len(existingHosts))
	for _, h := range existingHosts {
		hostIDByName[h.Name] = h.ID
	}
	for _, rh := range m.Hosts {
		if _, exists := hostIDByName[rh.Name]; exists {
			warnings = append(warnings, fmt.Sprintf("host %q already exists, skipped", rh.Name))
			continue
		}
		id, err := s.store.CreateHost(ctx, &store.Host{
			Name: rh.Name, Kind: rh.Kind, Address: rh.Address,
			TLSCA: rh.TLSCA, TLSCert: rh.TLSCert, TLSKey: rh.TLSKey,
			AlertEmail: rh.AlertEmail, Disabled: rh.Disabled,
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("host %q could not be created: %s", rh.Name, err))
			continue
		}
		if rh.HostKey != "" {
			_ = s.store.SetHostKey(ctx, id, rh.HostKey)
		}
		hostIDByName[rh.Name] = id
		summary.HostsCreated++
	}

	existingRegs, err := s.store.ListRegistries(ctx)
	if err != nil {
		return summary, warnings, err
	}
	regNames := make(map[string]bool, len(existingRegs))
	for _, reg := range existingRegs {
		regNames[reg.Name] = true
	}
	for _, rr := range m.Registries {
		if regNames[rr.Name] {
			warnings = append(warnings, fmt.Sprintf("registry %q already exists, skipped", rr.Name))
			continue
		}
		if _, err := s.store.CreateRegistry(ctx, rr.Name, rr.Address, rr.Username, rr.Password); err != nil {
			warnings = append(warnings, fmt.Sprintf("registry %q could not be created: %s", rr.Name, err))
			continue
		}
		summary.RegistriesCreated++
	}

	existingHooks, err := s.store.ListWebhooks(ctx)
	if err != nil {
		return summary, warnings, err
	}
	webhookIDByName := make(map[string]int64, len(existingHooks))
	for _, h := range existingHooks {
		webhookIDByName[h.Name] = h.ID
	}
	for _, rw := range m.Webhooks {
		if _, exists := webhookIDByName[rw.Name]; exists {
			continue // the webhook already exists; rules below link to it by name as-is
		}
		id, err := s.store.CreateWebhook(ctx, &store.Webhook{
			Name: rw.Name, URL: rw.URL, Method: rw.Method, Headers: rw.Headers, BodyTemplate: rw.BodyTemplate,
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("webhook %q could not be created: %s", rw.Name, err))
			continue
		}
		webhookIDByName[rw.Name] = id
		summary.WebhooksCreated++
	}

	for i, pr := range m.AlertRules {
		rule, warn, err := normalizeImportedRule(pr, webhookIDByName)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("alert rule %d (%q) skipped: %s", i+1, pr.Name, err))
			continue
		}
		if warn != "" {
			warnings = append(warnings, warn)
		}
		if _, err := s.store.CreateAlertRule(ctx, rule); err != nil {
			warnings = append(warnings, fmt.Sprintf("alert rule %d (%q) skipped: could not save", i+1, rule.Name))
			continue
		}
		summary.AlertRulesCreated++
	}

	if applySettings {
		if err := s.store.SetDisabledSections(ctx, m.Settings.DisabledSections); err != nil {
			warnings = append(warnings, "could not apply disabled sections: "+err.Error())
		}
		if err := s.store.SetLocalhostNo2FA(ctx, m.Settings.LocalhostNo2FA); err != nil {
			warnings = append(warnings, "could not apply localhost-2FA setting: "+err.Error())
		}
		if m.Settings.SMTP != nil {
			if err := s.store.SetSMTP(ctx, store.SMTPConfig{
				Host: m.Settings.SMTP.Host, Port: m.Settings.SMTP.Port, Username: m.Settings.SMTP.Username,
				Password: m.Settings.SMTP.Password, From: m.Settings.SMTP.From, To: m.Settings.SMTP.To, TLS: m.Settings.SMTP.TLS,
			}); err != nil {
				warnings = append(warnings, "could not apply SMTP settings: "+err.Error())
			}
		}
		if m.Settings.LDAP != nil {
			if err := s.store.SetLDAP(ctx, store.LDAPConfig{
				Enabled: m.Settings.LDAP.Enabled, URL: m.Settings.LDAP.URL, StartTLS: m.Settings.LDAP.StartTLS,
				BindDN: m.Settings.LDAP.BindDN, BindPassword: m.Settings.LDAP.BindPassword,
				UserBaseDN: m.Settings.LDAP.UserBaseDN, UserFilter: m.Settings.LDAP.UserFilter,
				AdminGroupDN: m.Settings.LDAP.AdminGroupDN,
			}); err != nil {
				warnings = append(warnings, "could not apply LDAP settings: "+err.Error())
			}
		}
	}

	existingProjects, err := s.store.ListProjects(ctx)
	if err != nil {
		return summary, warnings, err
	}
	projectSlugs := make(map[string]bool, len(existingProjects))
	for _, p := range existingProjects {
		projectSlugs[p.Slug] = true
	}

	for _, pm := range m.Projects {
		if projectSlugs[pm.Slug] {
			warnings = append(warnings, fmt.Sprintf("project %q already exists, skipped", pm.Slug))
			continue
		}
		targetHostID := defaultHostID
		if pm.HostName != "" {
			id, ok := hostIDByName[pm.HostName]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("project %q: host %q not found, imported against the chosen target host instead", pm.Slug, pm.HostName))
			} else {
				targetHostID = id
			}
		}

		tmp, terr := os.MkdirTemp("", "dc-recovery-import-*")
		if terr != nil {
			warnings = append(warnings, fmt.Sprintf("project %q skipped: %s", pm.Slug, terr))
			continue
		}
		extractZipPrefixToDir(zr, "projects/"+pm.Slug+"/", tmp)
		if _, verr := docker.ComposeConfig(ctx, tmp, pm.Slug); verr != nil {
			warnings = append(warnings, fmt.Sprintf("project %q skipped: its files no longer validate: %s", pm.Slug, verr))
			os.RemoveAll(tmp)
			continue
		}
		os.RemoveAll(tmp)

		id, cerr := s.store.CreateProject(ctx, &store.Project{
			Name: pm.Name, Slug: pm.Slug, ComposeFile: pm.ComposeFile, HostID: targetHostID,
			AllowRemoteHostPaths: pm.AllowRemoteHostPaths, CreatedBy: createdBy,
		})
		if errors.Is(cerr, store.ErrDuplicate) {
			warnings = append(warnings, fmt.Sprintf("project %q already exists, skipped", pm.Slug))
			continue
		}
		if cerr != nil {
			warnings = append(warnings, fmt.Sprintf("project %q could not be created: %s", pm.Slug, cerr))
			continue
		}
		root := s.projectRoot(id)
		if err := os.MkdirAll(root, projectDirMode); err != nil {
			warnings = append(warnings, fmt.Sprintf("project %q: could not create its directory: %s", pm.Slug, err))
			_ = s.store.DeleteProject(ctx, id)
			continue
		}
		extractZipPrefixToDir(zr, "projects/"+pm.Slug+"/", root)
		summary.ProjectsCreated++
	}

	return summary, warnings, nil
}

// extractZipPrefixToDir is extractZipToDir, scoped to entries under prefix
// within a shared multi-project zip — the same per-file safety
// (safeJoin, size cap, file-count cap) as its single-project counterpart.
func extractZipPrefixToDir(zr *zip.Reader, prefix, root string) int {
	count := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || count >= maxProjectFiles {
			continue
		}
		rel, ok := strings.CutPrefix(f.Name, prefix)
		if !ok {
			continue
		}
		full, err := safeJoin(root, rel)
		if err != nil || f.UncompressedSize64 > maxProjectFileBytes {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		content, _ := io.ReadAll(io.LimitReader(rc, maxProjectFileBytes))
		rc.Close()
		if err := os.MkdirAll(filepath.Dir(full), projectDirMode); err != nil {
			continue
		}
		if os.WriteFile(full, content, projectFileMode) == nil {
			count++
		}
	}
	return count
}
