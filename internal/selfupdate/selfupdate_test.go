package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExpectedSHA256(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "abc123  dockercmd-linux-amd64\ndef456  *dockercmd-darwin-arm64\n")
	}))
	defer srv.Close()

	rel := &ghRelease{Assets: []ghAsset{
		{Name: "dockercmd-linux-amd64"}, // no digest → falls back to SHA256SUMS
		{Name: "SHA256SUMS", URL: srv.URL},
	}}

	// Fallback to the SHA256SUMS asset when the per-asset digest is missing.
	if got, err := expectedSHA256(context.Background(), rel, &rel.Assets[0]); err != nil || got != "abc123" {
		t.Fatalf("SHA256SUMS fallback = %q, %v; want abc123", got, err)
	}

	// A present digest is used directly (no SUMS lookup).
	rel.Assets[0].Digest = "sha256:DEADBEEF"
	if got, _ := expectedSHA256(context.Background(), rel, &rel.Assets[0]); got != "DEADBEEF" {
		t.Errorf("digest path = %q; want DEADBEEF", got)
	}

	// No digest and no SHA256SUMS → hard error (never install unverified).
	if _, err := expectedSHA256(context.Background(), &ghRelease{Assets: []ghAsset{{Name: "x"}}}, &ghAsset{Name: "x"}); err == nil {
		t.Error("missing checksum source must error")
	}
}

func TestVerifyDigest(t *testing.T) {
	if err := verifyDigest("sha256:ABCDEF", "abcdef"); err != nil {
		t.Errorf("case-insensitive match should pass: %v", err)
	}
	if err := verifyDigest("sha256:abcdef", "abcdef"); err != nil {
		t.Errorf("exact match should pass: %v", err)
	}
	if err := verifyDigest("sha256:dead", "beef"); err == nil {
		t.Error("mismatch must error")
	}
	if err := verifyDigest("", "beef"); err == nil {
		t.Error("missing digest must error (never run unverified code)")
	}
}

func TestAssetForPlatform(t *testing.T) {
	name := "dockercmd-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	rel := &ghRelease{TagName: "v9.9.9", Assets: []ghAsset{
		{Name: "SHA256SUMS"},
		{Name: name, URL: "http://example/x"},
	}}
	a, err := assetForPlatform(rel)
	if err != nil {
		t.Fatalf("expected to find %q: %v", name, err)
	}
	if a.Name != name {
		t.Errorf("got %q, want %q", a.Name, name)
	}

	if _, err := assetForPlatform(&ghRelease{TagName: "v9.9.9"}); err == nil {
		t.Error("a release with no matching asset must error")
	}
}

func TestDownloadVerifyReplace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "dockercmd")
	if err := os.WriteFile(exe, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	payload := []byte("NEW BINARY v2 \x00\x01\x02 bytes")
	digest := sha256.Sum256(payload)
	want := hex.EncodeToString(digest[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	tmp, sum, err := download(context.Background(), exe, srv.URL, int64(len(payload)))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if sum != want {
		t.Fatalf("sum = %s, want %s", sum, want)
	}
	if filepath.Dir(tmp) != dir {
		t.Errorf("temp file landed in %s, want same dir as exe (%s)", filepath.Dir(tmp), dir)
	}
	if err := verifyDigest("sha256:"+want, sum); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := replaceExecutable(exe, tmp); err != nil {
		t.Fatalf("replace: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("exe content = %q, want %q", got, payload)
	}
	if fi, err := os.Stat(exe); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
		t.Errorf("exe mode = %o, want 0755 (permissions should be preserved)", fi.Mode().Perm())
	}
}

// platformAsset is the release asset name for the running OS/arch.
func platformAsset() string {
	n := "dockercmd-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// fakeReleaseServer mimics the GitHub Releases API: the "latest" endpoint plus
// the asset download. advertisedDigest is what the release CLAIMS the asset's
// SHA-256 is ("" = use the real one; "none" = omit it, leaving the release
// unverifiable). It points apiBaseURL at itself for the duration of the test.
func fakeReleaseServer(t *testing.T, tag string, binary []byte, advertisedDigest string) {
	t.Helper()
	asset := platformAsset()
	sum := sha256.Sum256(binary)
	realHex := hex.EncodeToString(sum[:])

	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "download" {
			_, _ = w.Write(binary)
			return
		}
		a := map[string]any{"name": asset, "browser_download_url": base + "/download", "size": len(binary)}
		switch advertisedDigest {
		case "none":
			// no digest field, no SHA256SUMS asset → unverifiable
		case "":
			a["digest"] = "sha256:" + realHex
		default:
			a["digest"] = advertisedDigest
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag, "html_url": "https://example/r", "assets": []map[string]any{a},
		})
	}))
	base = srv.URL
	t.Cleanup(srv.Close)

	prevBase, prevExe := apiBaseURL, osExecutable
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL, osExecutable = prevBase, prevExe })
}

// fakeExe points osExecutable at a temp file holding old, returning its path.
func fakeExe(t *testing.T, old []byte) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "dockercmd")
	if err := os.WriteFile(exe, old, 0o755); err != nil {
		t.Fatal(err)
	}
	osExecutable = func() (string, error) { return exe, nil }
	return exe
}

func TestApplyInstallsVerifiedRelease(t *testing.T) {
	newBin := []byte("NEW-BINARY-v2")
	fakeReleaseServer(t, "v2.0.0", newBin, "")
	exe := fakeExe(t, []byte("OLD-v1"))

	res, err := Apply(context.Background(), "1.0.0")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.From != "1.0.0" || res.To != "2.0.0" {
		t.Errorf("result = %+v, want 1.0.0 → 2.0.0", res)
	}
	if got, _ := os.ReadFile(exe); string(got) != string(newBin) {
		t.Errorf("binary not replaced: %q", got)
	}
}

func TestApplyUpToDate(t *testing.T) {
	fakeReleaseServer(t, "v1.0.0", []byte("x"), "")
	fakeExe(t, []byte("OLD"))
	if _, err := Apply(context.Background(), "1.0.0"); !errors.Is(err, ErrUpToDate) {
		t.Errorf("Apply on current version: err = %v, want ErrUpToDate", err)
	}
}

// PENTEST: the release advertises a digest that doesn't match the downloaded
// bytes (tampered/swapped asset). Apply MUST refuse and leave the binary as-is.
func TestApplyRejectsTamperedBinary(t *testing.T) {
	wrong := sha256.Sum256([]byte("not-what-gets-downloaded"))
	fakeReleaseServer(t, "v2.0.0", []byte("MALICIOUS"), "sha256:"+hex.EncodeToString(wrong[:]))
	exe := fakeExe(t, []byte("TRUSTED-ORIGINAL"))

	if _, err := Apply(context.Background(), "1.0.0"); err == nil {
		t.Fatal("Apply installed a binary whose checksum did not match")
	}
	if got, _ := os.ReadFile(exe); string(got) != "TRUSTED-ORIGINAL" {
		t.Errorf("running binary overwritten despite checksum mismatch: %q", got)
	}
}

// PENTEST: the download is bounded — an asset that streams far more than its
// advertised size is rejected (disk-exhaustion guard) and the binary untouched.
func TestDownloadRejectsOversizeAsset(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "dockercmd")
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 4096)) // server sends far more than the advertised 16 bytes
	}))
	defer srv.Close()

	if _, _, err := download(context.Background(), exe, srv.URL, 16); err == nil {
		t.Error("download accepted an asset larger than the advertised size")
	}
}

// PENTEST: a release with no digest and no SHA256SUMS is unverifiable; Apply
// must fail closed and never overwrite the running binary.
func TestApplyRejectsUnverifiableRelease(t *testing.T) {
	fakeReleaseServer(t, "v2.0.0", []byte("UNVERIFIED"), "none")
	exe := fakeExe(t, []byte("ORIGINAL"))

	if _, err := Apply(context.Background(), "1.0.0"); err == nil {
		t.Fatal("Apply installed an unverifiable release")
	}
	if got, _ := os.ReadFile(exe); string(got) != "ORIGINAL" {
		t.Errorf("exe overwritten by unverifiable release: %q", got)
	}
}
