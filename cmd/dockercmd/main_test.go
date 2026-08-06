package main

import (
	"bytes"
	"context"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestLoadOrCreateSecret(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	a, err := loadOrCreateSecret(ctx, st, "k")
	if err != nil || len(a) != 32 {
		t.Fatalf("first call should generate a 32-byte secret: len=%d err=%v", len(a), err)
	}
	b, err := loadOrCreateSecret(ctx, st, "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("subsequent calls must return the persisted secret")
	}
	// JWT helper wraps the generic one with a fixed key.
	if s, err := loadOrCreateJWTSecret(ctx, st); err != nil || len(s) != 32 {
		t.Errorf("jwt secret: len=%d err=%v", len(s), err)
	}
}

func TestServeWebFS(t *testing.T) {
	// Production: the embedded dist is returned and contains index.html.
	dist := serveWebFS(config.Config{})
	if dist == nil {
		t.Fatal("expected embedded web assets in non-dev mode")
	}
	if _, err := dist.Open("index.html"); err != nil {
		t.Errorf("embedded dist should contain index.html: %v", err)
	}
	// Dev mode hands the UI to Vite, so no embedded FS is served.
	if serveWebFS(config.Config{Dev: true}) != nil {
		t.Error("dev mode should not serve embedded assets")
	}
}

func TestLogStartup(t *testing.T) {
	// Smoke test: it only logs, so we just make sure both branches run cleanly.
	logStartup(config.Config{Addr: "127.0.0.1:8080", DataDir: "/tmp/dc"})
	logStartup(config.Config{Addr: "127.0.0.1:8080", DataDir: "/tmp/dc", Dev: true})
}

// withArgs swaps os.Args for the duration of fn (the standalone-action helpers
// scan os.Args directly).
func withArgs(args []string, fn func()) {
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{"dockercmd"}, args...)
	fn()
}

func TestStandaloneActionArgs(t *testing.T) {
	withArgs([]string{"--version"}, func() {
		if !wantsVersion() {
			t.Error("--version should be recognised")
		}
	})
	withArgs([]string{"version"}, func() {
		if !wantsVersion() {
			t.Error("bare `version` subcommand should be recognised")
		}
	})
	withArgs([]string{"--self-upgrade", "--check"}, func() {
		up, checkOnly := wantsSelfUpgrade()
		if !up || !checkOnly {
			t.Errorf("--self-upgrade --check: up=%v checkOnly=%v", up, checkOnly)
		}
		if wantsVersion() {
			t.Error("--self-upgrade is not a version request")
		}
	})
	withArgs([]string{"--install-service"}, func() {
		if got := serviceAction(); got != "install" {
			t.Errorf("serviceAction = %q, want install", got)
		}
	})
	// Args after `--` must be ignored.
	withArgs([]string{"--", "--version"}, func() {
		if wantsVersion() {
			t.Error("--version after `--` should be ignored")
		}
	})
	// Plain server start: no action.
	withArgs([]string{"-port", "9000"}, func() {
		if wantsVersion() || serviceAction() != "" {
			t.Error("a normal server invocation should not match any standalone action")
		}
	})
}

func TestWantsMakeCerts(t *testing.T) {
	withArgs([]string{"--make-certs", "example.lan", "10.0.0.5"}, func() {
		yes, hosts := wantsMakeCerts()
		if !yes || len(hosts) != 2 || hosts[0] != "example.lan" || hosts[1] != "10.0.0.5" {
			t.Errorf("--make-certs host1 host2: yes=%v hosts=%v", yes, hosts)
		}
	})
	withArgs([]string{"--make-certs"}, func() {
		if yes, hosts := wantsMakeCerts(); !yes || len(hosts) != 0 {
			t.Errorf("--make-certs alone: yes=%v hosts=%v", yes, hosts)
		}
	})
	// Hostname collection stops at the first flag.
	withArgs([]string{"--make-certs", "host", "-data-dir", "/x"}, func() {
		if _, hosts := wantsMakeCerts(); len(hosts) != 1 || hosts[0] != "host" {
			t.Errorf("hosts should stop at the first flag, got %v", hosts)
		}
	})
	withArgs([]string{"-port", "9000"}, func() {
		if yes, _ := wantsMakeCerts(); yes {
			t.Error("a normal server invocation should not trigger --make-certs")
		}
	})
}

func TestUsageListsStandaloneActions(t *testing.T) {
	var buf bytes.Buffer
	old := flag.CommandLine.Output()
	flag.CommandLine.SetOutput(&buf)
	defer flag.CommandLine.SetOutput(old)

	usage()
	out := buf.String()
	for _, want := range []string{"--version", "--make-certs", "--self-upgrade", "--install-service", "--uninstall-service", "--service-status"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() output is missing the %q action:\n%s", want, out)
		}
	}
}

// TestManPageDocumentsActions ensures the standalone actions stay documented in
// the man page (the config package test covers the flags).
func TestManPageDocumentsActions(t *testing.T) {
	man, err := os.ReadFile("../../deploy/dockercmd.1")
	if err != nil {
		t.Fatalf("read deploy/dockercmd.1: %v", err)
	}
	manStr := string(man)
	for _, want := range []string{"version", "make-certs", "self-upgrade", "install-service", "uninstall-service", "service-status"} {
		if !strings.Contains(manStr, want) {
			t.Errorf("action %q is not documented in deploy/dockercmd.1", want)
		}
	}
}

// The backup line quotes a size to an operator, and this project writes MiB/GiB
// everywhere else. Dividing by 1024 while printing "MB" is a small lie that ends
// up in a support conversation about why two tools disagree.
func TestHumanBytesLabelsBinaryUnitsAsBinary(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{3 << 30, "3.0 GiB"},
	} {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The listener's timeouts are security-relevant, and both were deletable with the
// whole suite still green — the read timeout is the only thing stopping a client
// from holding a handler open by dribbling out a body, and nothing noticed its
// absence. Asserting the values is crude; it is also the difference between a
// guard and a comment.
func TestHTTPServerHasReadTimeouts(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())

	if srv.ReadHeaderTimeout <= 0 {
		t.Error("SECURITY: no ReadHeaderTimeout; slow headers can hold a connection open")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("SECURITY: no ReadTimeout; a dribbled body can hold a handler open indefinitely")
	}
	if srv.ReadTimeout < srv.ReadHeaderTimeout {
		t.Errorf("ReadTimeout (%s) is shorter than ReadHeaderTimeout (%s), so the body gets no time at all",
			srv.ReadTimeout, srv.ReadHeaderTimeout)
	}
	// Deliberately absent: WebSocket streams are long-lived and a write deadline
	// would cut them off. Asserted so that adding one is a decision, not a reflex.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout is set (%s); this would break WebSocket streams", srv.WriteTimeout)
	}
}

// …and that the listener main actually runs is the one the constructor builds.
//
// The test above pins newHTTPServer. It does not pin that anything USES it: a
// literal &http.Server{} written back into run() keeps every test green, which is
// precisely how the timeouts came to be missing in the first place. So this reads
// the package's own source and refuses a second http.Server literal.
//
// Crude, and worth it — the alternative is a guard that only guards a function
// nobody is obliged to call.
func TestNoHandRolledHTTPServerInMain(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Server" {
					return true
				}
				if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != "http" {
					return true
				}
				// The one inside newHTTPServer is the point; anything else is a
				// listener built without the timeouts.
				if fn := enclosingFunc(file, lit.Pos()); fn != "newHTTPServer" {
					t.Errorf("%s builds an http.Server by hand in %s; use newHTTPServer so it gets the read timeouts",
						fset.Position(lit.Pos()), fn)
				}
				return true
			})
		}
	}
}

// enclosingFunc names the function a position falls inside, or "" at file scope.
func enclosingFunc(file *ast.File, pos token.Pos) string {
	name := ""
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Pos() <= pos && pos <= fn.End() {
			name = fn.Name.Name
		}
		return true
	})
	return name
}
