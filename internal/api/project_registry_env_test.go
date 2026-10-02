package api

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// A project deploy hands `docker compose` a config holding the credentials
// stored under Registries, and the deploy's cleanup removes it.
func TestProjectDeployEnvCarriesStoredRegistryCredentials(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	cph, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(cph)
	ctx := context.Background()
	if _, err := st.CreateRegistry(ctx, "ghcr", "ghcr.io", "bot", "s3cret"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	srv := &Server{cfg: config.Config{}, store: st}

	p := &store.Project{ID: 1, Slug: "demo", ComposeFile: "compose.yml", HostID: 0}
	env, _, _, cleanup, _, err := srv.projectDeployEnv(ctx, p, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var dir string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "DOCKER_CONFIG="); ok {
			dir = v
		}
	}
	if dir == "" {
		cleanup()
		t.Fatalf("the deploy env has no DOCKER_CONFIG: %v", env)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("bot:s3cret"))
	if !strings.Contains(string(b), want) {
		t.Errorf("the stored credential is not in the deploy's docker config: %s", b)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the deploy's cleanup left the config with the password behind")
	}
}

// One stored credential that can't be decrypted (say, saved under another key)
// doesn't stop the deploy. It is left out and named in the deploy's note; the
// other credentials are still used.
func TestProjectDeployEnvSkipsAnUndecryptableCredential(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	oldKey, _ := crypto.New(make([]byte, 32))
	st.SetCipher(oldKey)
	if _, err := st.CreateRegistry(ctx, "broken", "quay.io", "x", "y"); err != nil {
		t.Fatal(err)
	}
	newKey := make([]byte, 32)
	newKey[0] = 1
	cph, _ := crypto.New(newKey)
	st.SetCipher(cph)
	if _, err := st.CreateRegistry(ctx, "ghcr", "ghcr.io", "bot", "s3cret"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	srv := &Server{cfg: config.Config{}, store: st}

	p := &store.Project{ID: 1, Slug: "demo", ComposeFile: "compose.yml", HostID: 0}
	env, _, note, cleanup, _, err := srv.projectDeployEnv(ctx, p, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("one broken credential stopped the deploy: %v", err)
	}
	defer cleanup()
	if !strings.Contains(note, "quay.io") || !strings.Contains(note, "could not be decrypted") {
		t.Errorf("the skipped credential isn't named in the deploy note: %q", note)
	}
	var dir string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "DOCKER_CONFIG="); ok {
			dir = v
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), base64.StdEncoding.EncodeToString([]byte("bot:s3cret"))) {
		t.Error("the working credential was dropped along with the broken one")
	}
}
