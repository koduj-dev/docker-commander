package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The audit log documents itself, or it documents nothing.
//
// docs/audit.md used to name seven actions out of a hundred and forty-five, as
// illustrative prose ("e.g. container.stop, image.pull…"). That is a poor shape
// for this page in particular: the audit log is where somebody goes to find out
// what happened, and a reader has no way to learn what the log *can* contain. The
// gap was invisible in review because a missing sentence looks like nothing.
//
// So the list is generated from the source and this test keeps it that way: add an
// audited action without a row in the table and the build says so, naming it.
//
// This is the same shape as internal/config's TestManPageDocumentsAllFlags, which
// has kept every CLI flag documented for the same reason.

// auditCall matches an action literal in either form the code uses:
//
//	s.audit(r, "image.pull", …)
//	st.Audit(ctx, store.AuditEntry{… Action: "auth.password.reset" …})
var auditCall = regexp.MustCompile(`(?:\.audit\([^,]+,\s*|Action:\s*)"([a-z0-9_]+(?:\.[a-z0-9_]+){1,3})"`)

// dynamicAction matches the families built at runtime, e.g. `"container." + action`.
var dynamicAction = regexp.MustCompile(`\.audit\([^,]+,\s*"([a-z0-9_]+(?:\.[a-z0-9_]+)*\.)"\s*\+`)

// dynamicFamilies are the actions assembled from a verb at runtime. The verbs come
// from a switch the caller has already validated, so they cannot be scraped from
// the audit call itself — they are listed here, and the test checks the source
// still agrees (see TestDynamicAuditFamiliesMatchTheirSwitch).
var dynamicFamilies = map[string][]string{
	"container.":     {"start", "stop", "restart", "pause", "unpause", "kill"},
	"stack.":         {"start", "stop", "restart", "remove"},
	"project.":       {"down", "restart"},
	"mcp.container.": {"start", "stop", "restart"},
	"mcp.stack.":     {"start", "stop", "restart"},
}

// auditActionsInSource walks the tree and returns every action the code can write.
func auditActionsInSource(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	found := map[string]bool{}

	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range auditCall.FindAllStringSubmatch(string(src), -1) {
				found[m[1]] = true
			}
			for _, m := range dynamicAction.FindAllStringSubmatch(string(src), -1) {
				for _, verb := range dynamicFamilies[m[1]] {
					found[m[1]+verb] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	out := make([]string, 0, len(found))
	for a := range found {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func TestEveryAuditedActionIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "audit.md"))
	if err != nil {
		t.Fatalf("read docs/audit.md: %v", err)
	}
	actions := auditActionsInSource(t)
	if len(actions) < 100 {
		// The extraction found almost nothing, which means the regex stopped
		// matching rather than that the code stopped auditing. A test that
		// inspects nothing passes.
		t.Fatalf("only %d audited actions found in the source; the extraction is broken, not the docs", len(actions))
	}

	var missing []string
	for _, a := range actions {
		// Backticked, so a prefix like `auth.login` cannot satisfy `auth.login.failed`.
		if !strings.Contains(string(doc), "`"+a+"`") {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		t.Errorf(`%d audited action(s) are written by the code and appear nowhere in docs/audit.md.
The audit log is where someone goes to find out what happened; an action nobody
documented is one they cannot look for. Add a row for each:
  %s`, len(missing), strings.Join(missing, "\n  "))
	}
}

// The reverse: a documented action that no longer exists sends a reader hunting
// for something the app cannot write.
func TestDocumentedAuditActionsStillExist(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "audit.md"))
	if err != nil {
		t.Fatal(err)
	}
	real := map[string]bool{}
	for _, a := range auditActionsInSource(t) {
		real[a] = true
	}

	inDoc := regexp.MustCompile("`([a-z0-9_]+(?:\\.[a-z0-9_]+){1,3})`").FindAllStringSubmatch(string(doc), -1)
	var stale []string
	for _, m := range inDoc {
		// Only judge things shaped like an action; the page also mentions column
		// names and settings keys in backticks.
		if strings.Contains(m[1], ".") && !real[m[1]] {
			stale = append(stale, m[1])
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("docs/audit.md documents action(s) the code never writes: %s", strings.Join(stale, ", "))
	}
}

// The dynamic families are a hand-kept list, so it gets its own guard: if someone
// adds a verb to the switch, the list must grow with it or the action above will
// silently go undocumented.
func TestDynamicAuditFamiliesMatchTheirSwitch(t *testing.T) {
	for _, tc := range []struct {
		file   string
		fn     string // the function whose switch decides the verbs
		family string
	}{
		{filepath.Join("..", "docker", "ops.go"), "ContainerAction", "container."},
		{filepath.Join("..", "docker", "stacks.go"), "StackAction", "stack."},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		// Scoped to the one function: these files hold other switches (transports,
		// for one) whose cases are not action verbs at all.
		body := funcBody(t, string(src), tc.fn)
		known := map[string]bool{}
		for _, v := range dynamicFamilies[tc.family] {
			known[v] = true
		}
		for _, m := range regexp.MustCompile(`case ((?:"[a-z]+",?\s*)+):`).FindAllStringSubmatch(body, -1) {
			for _, v := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(m[1], -1) {
				if !known[v[1]] {
					t.Errorf("%s handles %q but dynamicFamilies[%q] does not list it — "+
						"the action %s%s would go undocumented", tc.fn, v[1], tc.family, tc.family, v[1])
				}
			}
		}
	}
}

// funcBody returns the source of one function, from its signature to the next
// top-level declaration.
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	start := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?` + regexp.QuoteMeta(name) + `\(`).FindStringIndex(src)
	if start == nil {
		t.Fatalf("function %s not found; the guard would inspect nothing", name)
	}
	rest := src[start[1]:]
	if end := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); end != nil {
		return rest[:end[0]]
	}
	return rest
}
