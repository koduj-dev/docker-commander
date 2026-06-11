// Package templates provides the built-in project scaffolds ("presets") and the
// composable service blocks ("blocks", the builder/skládačka) for the Projects
// feature, plus the rendering that turns either into concrete project files.
//
// Built-ins are embedded from catalog/; the API layer merges these with
// user-saved presets/blocks loaded from the store. Both presets and blocks carry
// optional Variables that are filled in (and secrets generated) at create time
// and rendered with text/template — the same mechanism as webhook bodies.
package templates

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template"
)

//go:embed all:catalog
var catalogFS embed.FS

// Source identifies where a preset/block came from. "remote" is reserved for a
// future catalog provider that fetches from an external API.
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
	SourceRemote  = "remote"
)

// Variable is a fill-in parameter declared by a preset or block.
type Variable struct {
	Key      string `json:"key"`                // template key, e.g. "HttpPort"
	Label    string `json:"label"`              // human label for the form
	Default  string `json:"default,omitempty"`  // value used when the field is left blank
	Secret   bool   `json:"secret,omitempty"`   // masked in the UI, encrypted at rest
	Generate string `json:"generate,omitempty"` // "password" → autofill a random value when blank
}

// File is one file in a project scaffold (path is relative to the project root).
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Preset is a complete project scaffold (one or more files).
type Preset struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Source      string     `json:"source"`
	Variables   []Variable `json:"variables,omitempty"`
	Files       []File     `json:"files,omitempty"` // omitted from list responses, included on detail
}

// Block is a single compose service fragment for the builder. ServiceYAML holds
// the service body already indented two spaces under `services:`.
type Block struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Source      string     `json:"source"`
	Service     string     `json:"service"` // the service key in compose, e.g. "db"
	Variables   []Variable `json:"variables,omitempty"`
	Volumes     []string   `json:"volumes,omitempty"` // top-level named volumes to declare
	ServiceYAML string     `json:"serviceYaml,omitempty"`
	Files       []File     `json:"files,omitempty"` // sidecar files copied into the project
}

type presetManifest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Variables   []Variable `json:"variables"`
}

type blockManifest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Service     string     `json:"service"`
	Variables   []Variable `json:"variables"`
	Volumes     []string   `json:"volumes"`
}

// BuiltinPresets returns the embedded project presets, sorted by name.
func BuiltinPresets() ([]Preset, error) {
	dirs, err := fs.ReadDir(catalogFS, "catalog/presets")
	if err != nil {
		return nil, err
	}
	var out []Preset
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p, err := loadPreset(path.Join("catalog/presets", d.Name()), d.Name())
		if err != nil {
			return nil, fmt.Errorf("preset %q: %w", d.Name(), err)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// BuiltinBlocks returns the embedded service blocks, sorted by name.
func BuiltinBlocks() ([]Block, error) {
	dirs, err := fs.ReadDir(catalogFS, "catalog/blocks")
	if err != nil {
		return nil, err
	}
	var out []Block
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b, err := loadBlock(path.Join("catalog/blocks", d.Name()), d.Name())
		if err != nil {
			return nil, fmt.Errorf("block %q: %w", d.Name(), err)
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadPreset(dir, id string) (Preset, error) {
	var m presetManifest
	if err := readManifest(dir, &m); err != nil {
		return Preset{}, err
	}
	files, err := readDirFiles(dir, "manifest.json")
	if err != nil {
		return Preset{}, err
	}
	return Preset{
		ID: id, Name: m.Name, Description: m.Description, Source: SourceBuiltin,
		Variables: m.Variables, Files: files,
	}, nil
}

func loadBlock(dir, id string) (Block, error) {
	var m blockManifest
	if err := readManifest(dir, &m); err != nil {
		return Block{}, err
	}
	svc, err := fs.ReadFile(catalogFS, path.Join(dir, "service.yml"))
	if err != nil {
		return Block{}, fmt.Errorf("read service.yml: %w", err)
	}
	files, err := readDirFiles(dir, "manifest.json", "service.yml")
	if err != nil {
		return Block{}, err
	}
	return Block{
		ID: id, Name: m.Name, Description: m.Description, Source: SourceBuiltin,
		Service: m.Service, Variables: m.Variables, Volumes: m.Volumes,
		ServiceYAML: strings.TrimRight(string(svc), "\n"), Files: files,
	}, nil
}

func readManifest(dir string, v any) error {
	raw, err := fs.ReadFile(catalogFS, path.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	return nil
}

// readDirFiles reads every file under dir (recursively) except the named ones,
// returning them with paths relative to dir.
func readDirFiles(dir string, skip ...string) ([]File, error) {
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var out []File
	err := fs.WalkDir(catalogFS, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepathRel(dir, p)
		if skipped[rel] {
			return nil
		}
		raw, err := fs.ReadFile(catalogFS, p)
		if err != nil {
			return err
		}
		out = append(out, File{Path: rel, Content: string(raw)})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// filepathRel is path.Rel for the slash-separated embed FS.
func filepathRel(base, target string) (string, error) {
	base = strings.TrimSuffix(base, "/") + "/"
	if !strings.HasPrefix(target, base) {
		return target, nil
	}
	return strings.TrimPrefix(target, base), nil
}

// ResolveVars merges provided values over the declared variables: a blank value
// falls back to Default, or to a generated secret when Generate is set. It
// errors if a non-generated variable ends up empty and had no default.
func ResolveVars(decl []Variable, provided map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range decl {
		val := strings.TrimSpace(provided[v.Key])
		if val == "" {
			val = v.Default
		}
		if val == "" && v.Generate == "password" {
			pw, err := randomPassword()
			if err != nil {
				return nil, err
			}
			val = pw
		}
		out[v.Key] = val
	}
	return out, nil
}

// Render fills in the variables on a set of files via text/template, erroring on
// any template key that has no value (missingkey=error).
func Render(files []File, vars map[string]string) ([]File, error) {
	out := make([]File, 0, len(files))
	for _, f := range files {
		content, err := renderString(f.Path, f.Content, vars)
		if err != nil {
			return nil, err
		}
		out = append(out, File{Path: f.Path, Content: content})
	}
	return out, nil
}

func renderString(name, body string, vars map[string]string) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return buf.String(), nil
}

// AssembleCompose builds the project files for a builder selection: a single
// compose.yml merging each block's service (plus any top-level named volumes)
// and the sidecar files the blocks contribute. Everything is rendered with vars.
func AssembleCompose(slug string, blocks []Block, vars map[string]string) ([]File, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n\nservices:\n", slug)
	var volumes []string
	var sidecars []File
	for _, blk := range blocks {
		// Built-in blocks carry declared variables and are rendered; user blocks
		// declare none and are copied literally, so a stray "{{" in user YAML can't
		// break assembly (and user blocks simply don't support variables yet).
		svc := blk.ServiceYAML
		if blk.Source == SourceBuiltin {
			rendered, err := renderString("block:"+blk.ID, svc, vars)
			if err != nil {
				return nil, err
			}
			svc = rendered
		}
		b.WriteString(svc)
		b.WriteString("\n")
		volumes = append(volumes, blk.Volumes...)
		sidecars = append(sidecars, blk.Files...)
	}
	if len(volumes) > 0 {
		b.WriteString("\nvolumes:\n")
		for _, v := range dedupe(volumes) {
			fmt.Fprintf(&b, "  %s:\n", v)
		}
	}
	// The compose skeleton + service bodies are already final; only built-in
	// sidecar files still carry template markers, so render just those.
	sidecars, err := Render(sidecars, vars)
	if err != nil {
		return nil, err
	}
	return append([]File{{Path: "compose.yml", Content: b.String()}}, sidecars...), nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// randomPassword returns a 24-char random string for generated secrets. It uses
// rejection sampling so the alphabet is sampled uniformly (no modulo bias).
func randomPassword() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	const limit = 256 - (256 % len(alphabet)) // largest multiple of len ≤ 256
	out := make([]byte, 24)
	buf := make([]byte, 1)
	for i := 0; i < len(out); {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if int(buf[0]) >= limit {
			continue // discard the biased tail
		}
		out[i] = alphabet[int(buf[0])%len(alphabet)]
		i++
	}
	return string(out), nil
}
