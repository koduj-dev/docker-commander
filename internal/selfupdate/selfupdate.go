// Package selfupdate implements `dockercmd --self-upgrade`: download the latest
// GitHub release asset for this OS/arch, verify its SHA-256 and atomically
// replace the running executable.
//
// Self-update executes downloaded code, so the checksum check is mandatory: the
// binary is only swapped in once its SHA-256 matches the digest GitHub records
// for the asset. The download lands in the target directory (same filesystem)
// so the final swap is an atomic rename.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/version"
)

const (
	repo        = "koduj-dev/docker-commander"
	userAgent   = "docker-commander-selfupgrade"
	httpTimeout = 5 * time.Minute
)

type ghAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // e.g. "sha256:abcdef…"
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	HTMLURL string    `json:"html_url"`
	Assets  []ghAsset `json:"assets"`
}

// Run checks for a newer release and, unless checkOnly is set, downloads and
// installs it. Progress is written to w; it is a no-op (with a message) when the
// running version is already current.
func Run(ctx context.Context, current string, w io.Writer, checkOnly bool) error {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	fmt.Fprintf(w, "Current version: %s\n", current)
	rel, err := latestRelease(ctx)
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	fmt.Fprintf(w, "Latest release:  %s\n", latest)

	if !version.Less(current, rel.TagName) {
		fmt.Fprintln(w, "Already up to date.")
		return nil
	}

	// --check: report that an update is waiting, but don't download anything.
	if checkOnly {
		fmt.Fprintf(w, "Update available: %s → %s\n", current, latest)
		if rel.HTMLURL != "" {
			fmt.Fprintf(w, "  %s\n", rel.HTMLURL)
		}
		fmt.Fprintln(w, "Run `dockercmd --self-upgrade` to install it.")
		return nil
	}

	asset, err := assetForPlatform(rel)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// Resolve the expected checksum *before* downloading, so we fail closed if no
	// verifiable digest exists.
	want, err := expectedSHA256(ctx, rel, asset)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "Downloading %s (%.1f MiB)…\n", asset.Name, float64(asset.Size)/(1<<20))
	tmp, sum, err := download(ctx, exe, asset.URL)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // harmless once the rename has consumed tmp

	if err := verifyDigest("sha256:"+want, sum); err != nil {
		return err
	}
	fmt.Fprintf(w, "Checksum OK (sha256:%s)\n", sum)

	if err := replaceExecutable(exe, tmp); err != nil {
		return err
	}
	fmt.Fprintf(w, "Upgraded %s → %s. Restart Docker Commander to run the new version.\n", current, latest)
	return nil
}

// assetForPlatform returns the release asset matching this binary's OS/arch.
func assetForPlatform(rel *ghRelease) (*ghAsset, error) {
	name := "dockercmd-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("release %s has no asset %q for your platform", rel.TagName, name)
}

func latestRelease(ctx context.Context) (*ghRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API: %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New("github API returned no release tag")
	}
	return &rel, nil
}

// download streams url into a temp file in the same directory as exe (so the
// later rename stays on one filesystem) and returns the temp path plus the
// hex-encoded SHA-256 of the downloaded bytes.
func download(ctx context.Context, exe, url string) (path, sum string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("download: %s", resp.Status)
	}

	f, err := os.CreateTemp(filepath.Dir(exe), ".dockercmd-upgrade-*")
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	if _, err := io.Copy(f, io.TeeReader(resp.Body, h)); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// expectedSHA256 returns the SHA-256 the release records for the asset: the
// asset's own `digest` field when GitHub populates it, otherwise the entry for
// it in the release's `SHA256SUMS` asset. Returns an error when neither exists —
// we never install without a checksum to verify against.
func expectedSHA256(ctx context.Context, rel *ghRelease, asset *ghAsset) (string, error) {
	if d := strings.TrimPrefix(strings.TrimSpace(asset.Digest), "sha256:"); d != "" {
		return d, nil
	}

	var sumsURL string
	for i := range rel.Assets {
		if rel.Assets[i].Name == "SHA256SUMS" {
			sumsURL = rel.Assets[i].URL
			break
		}
	}
	if sumsURL == "" {
		return "", errors.New("release provides no SHA-256 digest or SHA256SUMS to verify against")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	// Lines are "<hex>  <name>" (a leading "*" on the name marks binary mode).
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.TrimPrefix(f[len(f)-1], "*") == asset.Name {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %q", asset.Name)
}

// verifyDigest compares the downloaded SHA-256 against the digest GitHub records
// for the asset ("sha256:…"). A missing or mismatched digest is a hard error —
// we never run unverified code.
func verifyDigest(digest, sum string) error {
	want := strings.TrimPrefix(strings.TrimSpace(digest), "sha256:")
	if want == "" {
		return errors.New("release asset has no sha256 digest to verify against")
	}
	if !strings.EqualFold(want, sum) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s (refusing to install)", want, sum)
	}
	return nil
}

// replaceExecutable swaps tmp in for exe, preserving exe's permission bits. On
// Windows a running .exe can't be renamed over, so the current binary is moved
// aside to ".old" first (rolled back if the swap fails).
func replaceExecutable(exe, tmp string) error {
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(exe); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe) // roll back
			return err
		}
		return nil
	}
	return os.Rename(tmp, exe)
}
