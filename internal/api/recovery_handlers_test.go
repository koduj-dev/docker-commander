package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/passphrase"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// newRecoveryServer builds a Server with a real (but daemon-optional) docker
// Manager, a cipher (needed for host/registry secret encryption), and an
// admin user — everything the recovery bundle handlers touch.
func newRecoveryServer(t *testing.T) (*Server, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cph, _ := crypto.New(key)
	st.SetCipher(cph)
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: config.Config{DataDir: t.TempDir()}, store: st, docker: docker.NewManager(st)}
	admin, err := st.CreateUser(context.Background(), &store.User{Username: "admin", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return srv, admin
}

func exportRecoveryRequest(srv *Server, uid int64, body, pass string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/recovery/export", strings.NewReader(body)).WithContext(ctxAs(uid, "admin"))
	if pass != "" {
		r.Header.Set(recoveryPassphraseHeader, pass)
	}
	w := httptest.NewRecorder()
	srv.handleExportRecoveryBundle(w, r)
	return w
}

func inspectRecoveryRequest(srv *Server, uid int64, data []byte, pass string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/recovery/inspect", bytes.NewReader(data)).WithContext(ctxAs(uid, "admin"))
	if pass != "" {
		r.Header.Set(recoveryPassphraseHeader, pass)
	}
	w := httptest.NewRecorder()
	srv.handleInspectRecoveryBundle(w, r)
	return w
}

func importRecoveryRequest(srv *Server, uid int64, data []byte, pass, query string) *httptest.ResponseRecorder {
	url := "/api/recovery/import"
	if query != "" {
		url += "?" + query
	}
	r := httptest.NewRequest("POST", url, bytes.NewReader(data)).WithContext(ctxAs(uid, "admin"))
	if pass != "" {
		r.Header.Set(recoveryPassphraseHeader, pass)
	}
	w := httptest.NewRecorder()
	srv.handleImportRecoveryBundle(w, r)
	return w
}

// TestRecoveryExport_ExcludesSecretsByDefault: a plain export (no
// includeSecrets, no passphrase) must not contain a host's TLS key or a
// registry's password anywhere in its bytes.
func TestRecoveryExport_ExcludesSecretsByDefault(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	if _, err := srv.store.CreateHost(ctx, &store.Host{Name: "prod", Kind: "tcp", Address: "1.2.3.4:2376", TLSKey: "TOP-SECRET-KEY"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateRegistry(ctx, "ghcr", "ghcr.io", "user", "TOP-SECRET-PASSWORD"); err != nil {
		t.Fatal(err)
	}

	w := exportRecoveryRequest(srv, admin, `{"includeSecrets":false}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}
	raw := w.Body.Bytes()
	if bytes.Contains(raw, []byte("TOP-SECRET-KEY")) || bytes.Contains(raw, []byte("TOP-SECRET-PASSWORD")) {
		t.Error("SECURITY: a secrets-excluded export leaked a plaintext secret")
	}

	manifest, _, err := parseRecoveryBundle(raw, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if manifest.IncludesSecrets {
		t.Error("manifest.IncludesSecrets should be false")
	}
	if len(manifest.Hosts) != 1 || manifest.Hosts[0].TLSKey != "" {
		t.Errorf("host TLS key should be blank, got %+v", manifest.Hosts)
	}
	if len(manifest.Registries) != 1 || manifest.Registries[0].Password != "" {
		t.Errorf("registry password should be blank, got %+v", manifest.Registries)
	}
}

// TestRecoveryExport_WithSecretsIncludesPlaintext confirms the opt-in path
// actually carries the plaintext, since that is the whole point of asking
// for it — an operator who explicitly opts in must get a usable bundle.
func TestRecoveryExport_WithSecretsIncludesPlaintext(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	if _, err := srv.store.CreateHost(ctx, &store.Host{Name: "prod", Kind: "tcp", Address: "1.2.3.4:2376", TLSKey: "TOP-SECRET-KEY"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateRegistry(ctx, "ghcr", "ghcr.io", "user", "TOP-SECRET-PASSWORD"); err != nil {
		t.Fatal(err)
	}

	w := exportRecoveryRequest(srv, admin, `{"includeSecrets":true}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}
	manifest, _, err := parseRecoveryBundle(w.Body.Bytes(), "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !manifest.IncludesSecrets {
		t.Error("manifest.IncludesSecrets should be true")
	}
	if len(manifest.Hosts) != 1 || manifest.Hosts[0].TLSKey != "TOP-SECRET-KEY" {
		t.Errorf("host TLS key should round-trip in plaintext, got %+v", manifest.Hosts)
	}
	if len(manifest.Registries) != 1 || manifest.Registries[0].Password != "TOP-SECRET-PASSWORD" {
		t.Errorf("registry password should round-trip in plaintext, got %+v", manifest.Registries)
	}
}

// TestRecoveryExportImport_PassphraseRoundTrip: sealing with a passphrase
// then opening with the same one must recover the exact manifest; the wrong
// one must fail; no passphrase against an encrypted bundle must ask for one.
func TestRecoveryExportImport_PassphraseRoundTrip(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	if _, err := srv.store.CreateRegistry(ctx, "ghcr", "ghcr.io", "user", "s3cr3t"); err != nil {
		t.Fatal(err)
	}

	w := exportRecoveryRequest(srv, admin, `{"includeSecrets":true}`, "correct horse battery staple")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}
	sealed := w.Body.Bytes()
	if bytes.Contains(sealed, []byte("s3cr3t")) {
		t.Error("SECURITY: an encrypted bundle contains plaintext")
	}

	if _, _, err := parseRecoveryBundle(sealed, ""); err == nil {
		t.Error("opening an encrypted bundle with no passphrase should fail")
	}
	if _, _, err := parseRecoveryBundle(sealed, "wrong passphrase"); err == nil {
		t.Error("opening an encrypted bundle with the wrong passphrase should fail")
	}
	manifest, _, err := parseRecoveryBundle(sealed, "correct horse battery staple")
	if err != nil {
		t.Fatalf("parse with correct passphrase: %v", err)
	}
	if len(manifest.Registries) != 1 || manifest.Registries[0].Password != "s3cr3t" {
		t.Errorf("registry password did not round-trip: %+v", manifest.Registries)
	}
}

// TestRecoveryImport_UnsupportedVersionRejected: a manifest claiming a
// version this build doesn't know must be refused, not partially applied.
func TestRecoveryImport_UnsupportedVersionRejected(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	data := buildTestBundle(t, recoveryManifest{Version: recoveryBundleVersion + 1})
	w := importRecoveryRequest(srv, admin, data, "", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("import of a future-version bundle status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// TestRecoveryImport_NeverOverwritesExistingHostOrRegistry: importing a
// bundle whose host/registry name already exists locally must skip it (with
// a warning), never overwrite — the same rule alert_portability.go already
// applies to alert rules.
func TestRecoveryImport_NeverOverwritesExistingHostOrRegistry(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	if _, err := srv.store.CreateHost(ctx, &store.Host{Name: "prod", Kind: "tcp", Address: "OLD-ADDRESS:2376"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateRegistry(ctx, "ghcr", "ghcr.io", "old-user", "old-pw"); err != nil {
		t.Fatal(err)
	}

	data := buildTestBundle(t, recoveryManifest{
		Version:    recoveryBundleVersion,
		Hosts:      []recoveryHost{{Name: "prod", Kind: "tcp", Address: "NEW-ADDRESS:2376"}},
		Registries: []recoveryRegistry{{Name: "ghcr", Address: "ghcr.io", Username: "new-user"}},
	})
	w := importRecoveryRequest(srv, admin, data, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Summary  importSummary `json:"summary"`
		Warnings []string      `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.HostsCreated != 0 || resp.Summary.RegistriesCreated != 0 {
		t.Errorf("a name collision must create nothing, got %+v", resp.Summary)
	}
	if len(resp.Warnings) < 2 {
		t.Errorf("expected warnings about both skipped collisions, got %v", resp.Warnings)
	}
	hosts, _ := srv.store.ListHosts(ctx)
	for _, h := range hosts {
		if h.Name == "prod" && h.Address != "OLD-ADDRESS:2376" {
			t.Errorf("SECURITY/DATA: the existing host was overwritten, address = %q", h.Address)
		}
	}
}

// TestRecoveryImport_ApplySettingsDefaultsToFalse: a bundle carrying settings
// must not change a live instance's settings unless applySettings=true is
// explicitly passed.
func TestRecoveryImport_ApplySettingsDefaultsToFalse(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	if err := srv.store.SetLocalhostNo2FA(ctx, false); err != nil {
		t.Fatal(err)
	}
	data := buildTestBundle(t, recoveryManifest{
		Version:  recoveryBundleVersion,
		Settings: recoverySettings{LocalhostNo2FA: true},
	})

	if w := importRecoveryRequest(srv, admin, data, "", ""); w.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", w.Code, w.Body.String())
	}
	if on, _ := srv.store.LocalhostNo2FA(ctx); on {
		t.Error("settings must not change without applySettings=true")
	}

	if w := importRecoveryRequest(srv, admin, data, "", "applySettings=true"); w.Code != http.StatusOK {
		t.Fatalf("import (applySettings=true) status = %d: %s", w.Code, w.Body.String())
	}
	if on, _ := srv.store.LocalhostNo2FA(ctx); !on {
		t.Error("settings should apply when applySettings=true is explicitly passed")
	}
}

// TestRecoveryImportExport_ProjectRoundTrip: a project exported and
// re-imported into a fresh instance must end up with the same files and
// re-deployable content. Needs the real compose CLI to validate the
// extracted files, like handleRestoreRevision's own tests.
func TestRecoveryImportExport_ProjectRoundTrip(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	pid, err := srv.store.CreateProject(ctx, &store.Project{Name: "shop", Slug: "dctest-recovery-shop", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: nginx:1.27\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	w := exportRecoveryRequest(srv, admin, `{}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}

	dst, dstAdmin := newRecoveryServer(t)
	iw := importRecoveryRequest(dst, dstAdmin, w.Body.Bytes(), "", "")
	if iw.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", iw.Code, iw.Body.String())
	}
	var resp struct {
		Summary importSummary `json:"summary"`
	}
	if err := json.Unmarshal(iw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.ProjectsCreated != 1 {
		t.Fatalf("expected 1 project created, got %+v", resp.Summary)
	}
	projects, err := dst.store.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Slug != "dctest-recovery-shop" {
		t.Fatalf("project not imported as expected: %v %+v", err, projects)
	}
	got, err := os.ReadFile(filepath.Join(dst.projectRoot(projects[0].ID), "compose.yml"))
	if err != nil || string(got) != compose {
		t.Errorf("imported compose.yml = %q, %v", got, err)
	}
}

// TestRecoveryImport_ProjectSlugCollisionSkipped: importing over an existing
// project slug must skip it, never overwrite the live project's files.
func TestRecoveryImport_ProjectSlugCollisionSkipped(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	pid, err := srv.store.CreateProject(ctx, &store.Project{Name: "shop", Slug: "dctest-recovery-collide", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services:\n  web:\n    image: nginx:1.27\n"), 0o644)

	data := buildTestBundle(t, recoveryManifest{
		Version:  recoveryBundleVersion,
		Projects: []recoveryProjectMeta{{Slug: "dctest-recovery-collide", Name: "shop", ComposeFile: "compose.yml"}},
	})
	w := importRecoveryRequest(srv, admin, data, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Summary  importSummary `json:"summary"`
		Warnings []string      `json:"warnings"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Summary.ProjectsCreated != 0 {
		t.Errorf("a slug collision must create nothing, got %+v", resp.Summary)
	}
	projects, _ := srv.store.ListProjects(ctx)
	if len(projects) != 1 {
		t.Errorf("SECURITY/DATA: expected exactly the original project to remain, got %d", len(projects))
	}
}

// buildTestBundle wraps a manifest in the exact zip+passphrase-envelope
// shape parseRecoveryBundle expects, without going through the export
// handler — used to construct manifests the export path itself would never
// produce (a bad version, a deliberately-colliding name), so the import
// path's own defenses are what's under test, not the exporter.
// TestRecoveryImport_ReimportingSameBundleDoesNotDuplicateAlertRules: a
// recovery retry (import the same bundle twice — the normal shape of "the
// first attempt half-failed, try again") must not create a second copy of
// every rule, which would double-fire every notification.
func TestRecoveryImport_ReimportingSameBundleDoesNotDuplicateAlertRules(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	data := buildTestBundle(t, recoveryManifest{
		Version: recoveryBundleVersion,
		AlertRules: []recoveryAlertRule{{
			portableRule: portableRule{Name: "die-alert", Enabled: true, Type: "state", Target: "web", Config: []byte(`{}`), Severity: "critical"},
		}},
	})

	for i := 0; i < 2; i++ {
		w := importRecoveryRequest(srv, admin, data, "", "")
		if w.Code != http.StatusOK {
			t.Fatalf("import #%d status = %d: %s", i+1, w.Code, w.Body.String())
		}
	}
	rules, err := srv.store.ListAlertRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Errorf("SECURITY/DATA: re-importing the same bundle produced %d copies of the rule, want 1", len(rules))
	}
}

// TestRecoveryExportImport_DisabledHostRoundTrip: CreateHost's own INSERT
// used to omit the disabled column, so any imported host — however it was
// marked in the bundle — came back enabled. A disabled host restored as
// enabled means the monitor may immediately start connecting to it.
func TestRecoveryExportImport_DisabledHostRoundTrip(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	if _, err := srv.store.CreateHost(context.Background(), &store.Host{Name: "quarantined", Kind: "tcp", Address: "10.0.0.9:2376", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	w := exportRecoveryRequest(srv, admin, `{}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}

	dst, dstAdmin := newRecoveryServer(t)
	iw := importRecoveryRequest(dst, dstAdmin, w.Body.Bytes(), "", "")
	if iw.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", iw.Code, iw.Body.String())
	}
	hosts, err := dst.store.ListHosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, h := range hosts {
		if h.Name == "quarantined" {
			found = true
			if !h.Disabled {
				t.Error("SECURITY: a disabled host was restored as enabled")
			}
		}
	}
	if !found {
		t.Fatal("the host was not imported at all")
	}
}

// TestRecoveryExportImport_PerRuleEmailsRoundTrip: portableRule (shared with
// alert_portability.go's own, narrower export) has no Emails field — the
// recovery bundle's own recoveryAlertRule wrapper must carry it through, or
// an imported rule silently falls back to the instance-wide recipients.
func TestRecoveryExportImport_PerRuleEmailsRoundTrip(t *testing.T) {
	srv, admin := newRecoveryServer(t)
	if _, err := srv.store.CreateAlertRule(context.Background(), &store.AlertRule{
		Name: "die-alert", Enabled: true, Type: "state", Target: "web", Config: "{}",
		Severity: "critical", Email: true, Emails: []string{"oncall@example.com", "backup@example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	w := exportRecoveryRequest(srv, admin, `{}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}

	dst, dstAdmin := newRecoveryServer(t)
	iw := importRecoveryRequest(dst, dstAdmin, w.Body.Bytes(), "", "")
	if iw.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", iw.Code, iw.Body.String())
	}
	rules, err := dst.store.ListAlertRules(context.Background())
	if err != nil || len(rules) != 1 {
		t.Fatalf("expected 1 imported rule, got %v %+v", err, rules)
	}
	got := rules[0].Emails
	if len(got) != 2 || got[0] != "oncall@example.com" || got[1] != "backup@example.com" {
		t.Errorf("per-rule recipients did not round-trip, got %v", got)
	}
}

// TestRecoveryImport_FinalizeFailureDoesNotClaimSuccess: if the atomic
// rename from the validated staging directory into the live project
// location fails (here: something already occupies that path), the project
// must not be reported as created — the old code re-extracted a SECOND,
// unchecked time directly into the live directory and always incremented
// the summary regardless of what happened.
func TestRecoveryImport_FinalizeFailureDoesNotClaimSuccess(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, admin := newRecoveryServer(t)
	// The next project created in a fresh store gets id 1 (AUTOINCREMENT
	// starting from empty) — occupy where its directory would land with a
	// plain FILE, so the finalizing os.Rename(stagingDir, ".../projects/1")
	// fails outright (renaming a directory over an existing non-directory).
	projectsDir := filepath.Join(srv.cfg.DataDir, "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectsDir, "1"), []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	mustZipEntry(t, zw, "manifest.json", mustJSONBytes(t, recoveryManifest{
		Version:  recoveryBundleVersion,
		Projects: []recoveryProjectMeta{{Slug: "dctest-recovery-finalize", Name: "shop", ComposeFile: "compose.yml"}},
	}))
	mustZipEntry(t, zw, "projects/dctest-recovery-finalize/compose.yml", []byte("services:\n  web:\n    image: nginx:1.27\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := wrapPlainBundle(t, zipBuf.Bytes())

	w := importRecoveryRequest(srv, admin, data, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Summary  importSummary `json:"summary"`
		Warnings []string      `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.ProjectsCreated != 0 {
		t.Errorf("SECURITY/DATA: a project whose files failed to finalize was still reported as created: %+v", resp.Summary)
	}
	if projects, _ := srv.store.ListProjects(context.Background()); len(projects) != 0 {
		t.Errorf("a project row must not survive a failed finalize, got %d", len(projects))
	}
}

// TestImageIsPresent_MatchesDockersTaglessRepoDigestFormat: Docker's own
// RepoDigests are always tagless ("alpine@sha256:…"), even for an image
// captured as "alpine:latest". Comparing a tag-preserving reference against
// them directly would never match an image that IS present.
func TestImageIsPresent_MatchesDockersTaglessRepoDigestFormat(t *testing.T) {
	localTags := map[string]bool{"docker.io/nginx": true}
	localDigests := map[string]bool{"docker.io/alpine@sha256:aaaa": true}

	cases := []struct {
		name string
		img  recoveryProjectImage
		want bool
	}{
		{"tagged image present locally", recoveryProjectImage{Image: "nginx:1.27"}, true},
		{"tagged image not present locally", recoveryProjectImage{Image: "redis:7"}, false},
		{"digest-pinned image present, source ref carried a tag", recoveryProjectImage{Image: "alpine:latest", Digest: "sha256:aaaa"}, true},
		{"digest-pinned image with a different digest is missing", recoveryProjectImage{Image: "alpine:latest", Digest: "sha256:bbbb"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := imageIsPresent(c.img, localTags, localDigests); got != c.want {
				t.Errorf("imageIsPresent(%+v) = %v, want %v", c.img, got, c.want)
			}
		})
	}
}

// TestWriteDirToZip_RespectsAggregateBudget: the whole export is built in
// memory before it's sent, so an aggregate byte budget — shared across every
// project packed into the SAME export, not just a per-file/per-project cap —
// must actually stop packing once exhausted, rather than only bounding one
// file or one project at a time.
func TestWriteDirToZip_RespectsAggregateBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), bytes.Repeat([]byte("y"), 100), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	budget := int64(150) // enough for one 100-byte file, not both
	_, err := writeDirToZip(zw, root, "projects/p/", &budget)
	if !errors.Is(err, errBundleTooLarge) {
		t.Fatalf("writeDirToZip with an exhausted budget = %v, want errBundleTooLarge", err)
	}
}

func buildTestBundle(t *testing.T, m recoveryManifest) []byte {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	mw, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(mw).Encode(m); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := passphrase.WritePlainTo(&out, recoveryMagic, bytes.NewReader(zipBuf.Bytes())); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
