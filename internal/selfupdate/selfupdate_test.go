package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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

	tmp, sum, err := download(context.Background(), exe, srv.URL)
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
