package templates

import (
	"strings"
	"testing"
)

// builtinVars resolves a declaration set and adds the always-present Slug/Name
// keys the API injects at create time, so test renders mirror production.
func builtinVars(t *testing.T, decl []Variable) map[string]string {
	t.Helper()
	v, err := ResolveVars(decl, nil)
	if err != nil {
		t.Fatalf("resolve vars: %v", err)
	}
	v["Slug"] = "test-project"
	v["Name"] = "Test Project"
	return v
}

func TestBuiltinPresetsRender(t *testing.T) {
	presets, err := BuiltinPresets()
	if err != nil {
		t.Fatalf("load presets: %v", err)
	}
	if len(presets) < 4 {
		t.Fatalf("expected at least 4 presets, got %d", len(presets))
	}
	for _, p := range presets {
		if p.ID == "" || p.Name == "" || len(p.Files) == 0 || p.Source != SourceBuiltin {
			t.Errorf("preset %q incomplete: %+v", p.ID, p)
		}
		if _, err := Render(p.Files, builtinVars(t, p.Variables)); err != nil {
			t.Errorf("preset %q render: %v", p.ID, err)
		}
	}
}

func TestBuiltinBlocksAssemble(t *testing.T) {
	blocks, err := BuiltinBlocks()
	if err != nil {
		t.Fatalf("load blocks: %v", err)
	}
	if len(blocks) < 7 {
		t.Fatalf("expected at least 7 blocks, got %d", len(blocks))
	}
	var decl []Variable
	for _, b := range blocks {
		if b.Service == "" || b.ServiceYAML == "" {
			t.Errorf("block %q missing service/serviceYaml", b.ID)
		}
		decl = append(decl, b.Variables...)
	}
	files, err := AssembleCompose("test-project", blocks, nil, builtinVars(t, decl))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if files[0].Path != "compose.yml" {
		t.Fatalf("expected compose.yml first, got %q", files[0].Path)
	}
	compose := files[0].Content
	for _, want := range []string{"name: test-project", "services:", "  web:", "  db:", "  redis:", "volumes:", "pgdata:"} {
		if !strings.Contains(compose, want) {
			t.Errorf("assembled compose missing %q:\n%s", want, compose)
		}
	}
	// No template markers should survive rendering.
	if strings.Contains(compose, "{{") {
		t.Errorf("unrendered template marker in compose:\n%s", compose)
	}
}

func TestResolveVars(t *testing.T) {
	decl := []Variable{
		{Key: "Port", Default: "8080"},
		{Key: "Pw", Secret: true, Generate: "password"},
	}
	// Defaults + generation when nothing is provided.
	v, err := ResolveVars(decl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["Port"] != "8080" {
		t.Errorf("default not applied: %q", v["Port"])
	}
	if len(v["Pw"]) < 16 {
		t.Errorf("password not generated: %q", v["Pw"])
	}
	// Provided values win over defaults/generation.
	v2, _ := ResolveVars(decl, map[string]string{"Port": "9000", "Pw": "hunter2"})
	if v2["Port"] != "9000" || v2["Pw"] != "hunter2" {
		t.Errorf("provided values not honored: %+v", v2)
	}
}

func TestRenderMissingKeyErrors(t *testing.T) {
	if _, err := Render([]File{{Path: "x", Content: "{{.Nope}}"}}, map[string]string{}); err == nil {
		t.Error("expected an error for an undefined template key")
	}
}
