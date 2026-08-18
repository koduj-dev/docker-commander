package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // same CGO-free driver the store package uses

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// A failed persist of "last deployed profiles" must not turn a successful
// deploy into an apparent failure (the response still says ok:true — the
// deploy itself worked), but it also must not be completely invisible: an
// operator needs some trace that the UI's "Deployed with" badge may now be
// stale. This forces SetLastDeployedProfiles to fail against a REAL sqlite
// write (not a mock), the way TestRBACFailsClosedOnStoreError forces a store
// failure elsewhere in this package — but that test closes the whole store,
// which would also fail the earlier reads this handler needs (loadProject,
// projectHost) before it ever reaches the write under test. Instead, a
// second raw connection to the SAME sqlite file installs a trigger that
// aborts writes to just the one column this handler writes, on just this
// project's row — reads, and every other write, keep working.
func TestHandleDeployProject_PersistFailureIsLoggedNotSwallowed(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	const slug = "dctest-deploy-profiles-persist-fail"
	compose := `
services:
  web:
    image: ` + deployTestImage + `
    command: ["sleep", "300"]
`
	dbPath := filepath.Join(t.TempDir(), "test.db")
	srv, st, pid, admin := deployTestServerAt(t, dbPath, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	// Sabotage: a BEFORE UPDATE trigger that aborts any write to
	// last_deployed_profiles on this project's row. It does not touch reads,
	// nor any other column/row, so loadProject/projectHost and the audit
	// insert below keep working — only the write under test fails.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite conn: %v", err)
	}
	trigger := fmt.Sprintf(`
CREATE TRIGGER dctest_block_profiles
BEFORE UPDATE OF last_deployed_profiles ON projects
WHEN NEW.id = %d
BEGIN
  SELECT RAISE(ABORT, 'dctest: forced last_deployed_profiles write failure');
END;`, pid)
	if _, err := raw.Exec(trigger); err != nil {
		t.Fatalf("install sabotage trigger: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite conn: %v", err)
	}

	var logBuf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	w := deployRequest(srv, pid, admin, `{"profiles":[],"build":false}`)
	if w.Code != 200 {
		t.Fatalf("deploy request failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("a persistence hiccup must not turn a successful deploy into a reported failure: %s / %s", resp.Error, resp.Output)
	}

	if got := logBuf.String(); got == "" {
		t.Error("SetLastDeployedProfiles failure was completely swallowed — nothing was logged")
	} else if !bytes.Contains(logBuf.Bytes(), []byte("last deployed profiles")) {
		t.Errorf("logged output does not mention the failed write, got: %q", got)
	}

	// The store itself never advanced past "no profiles recorded" — the sabotage
	// trigger really did block the write, this isn't a false pass.
	p, err := st.ProjectByID(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if p.LastDeployedProfiles != nil {
		t.Errorf("expected the write to have failed and left profiles unset, got %v", p.LastDeployedProfiles)
	}
}
