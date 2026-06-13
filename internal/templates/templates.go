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
	"regexp"
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

// Fragment is a top-level "shared definition" for the builder — a YAML anchor
// (e.g. `x-common: &common ...`) emitted above services: so any service can
// merge it with `<<: *common`. Content is copied literally (never rendered), so
// anchors and merge keys survive intact.
type Fragment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Content     string `json:"content,omitempty"`
}

type fragmentManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
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

// BuiltinFragments returns the embedded shared definitions, sorted by name.
func BuiltinFragments() ([]Fragment, error) {
	dirs, err := fs.ReadDir(catalogFS, "catalog/fragments")
	if err != nil {
		// No fragments dir embedded yet → no built-ins, not an error.
		return nil, nil
	}
	var out []Fragment
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		f, err := loadFragment(path.Join("catalog/fragments", d.Name()), d.Name())
		if err != nil {
			return nil, fmt.Errorf("fragment %q: %w", d.Name(), err)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadFragment(dir, id string) (Fragment, error) {
	var m fragmentManifest
	if err := readManifest(dir, &m); err != nil {
		return Fragment{}, err
	}
	content, err := fs.ReadFile(catalogFS, path.Join(dir, "fragment.yml"))
	if err != nil {
		return Fragment{}, fmt.Errorf("read fragment.yml: %w", err)
	}
	return Fragment{
		ID: id, Name: m.Name, Description: m.Description, Source: SourceBuiltin,
		Content: strings.TrimRight(string(content), "\n"),
	}, nil
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

// Instance is one service placed by the builder: a block under a chosen service
// key, optionally merging shared-definition anchors. Adding the same block twice
// (two instances, distinct keys) builds a cluster.
type Instance struct {
	Block        Block
	Key          string   // service key in the output; defaults to Block.Service
	MergeAnchors []string // anchor names to inject as `<<: *name` at the top of the service
}

// svcKeyLine matches a top-level service key line (`  name:`), tolerating a
// trailing inline comment.
var svcKeyLine = regexp.MustCompile(`^(\s*)([A-Za-z0-9._-]+):\s*(#.*)?$`)

var anchorRe = regexp.MustCompile(`&([A-Za-z0-9_-]+)`)

// AnchorNames returns every YAML anchor (`&name`) declared in a fragment's
// content (in order, de-duplicated) — used to wire `<<: *name` merges. Comment
// lines are skipped so a `# … &x …` note can't be mistaken for an anchor.
func AnchorNames(content string) []string {
	var out []string
	seen := map[string]bool{}
	for _, ln := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		for _, m := range anchorRe.FindAllStringSubmatch(ln, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// AssembleCompose builds the project files for a builder selection: a single
// compose.yml with any shared definitions (top-level YAML anchors) above
// services:, each instance's service (renamed to its key, with `<<: *anchor`
// merges injected and named volumes de-duplicated per instance), the top-level
// named volumes, and the sidecar files the blocks contribute. Built-in block
// YAML/sidecars are rendered with vars; fragments are copied literally so
// anchors/merge keys survive.
func AssembleCompose(slug string, instances []Instance, fragments []Fragment, vars map[string]string) ([]File, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n\n", slug)
	// Shared definitions first: a service can only merge `<<: *anchor` if the
	// anchor is defined earlier in the document.
	for _, fr := range fragments {
		content := strings.TrimRight(fr.Content, "\n")
		if content == "" {
			continue
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}

	// A named volume used by more than one instance must be made unique per
	// instance (else clustered services would share — and corrupt — one volume).
	volCount := map[string]int{}
	for _, in := range instances {
		for _, v := range in.Block.Volumes {
			volCount[v]++
		}
	}

	b.WriteString("services:\n")
	var topVols []string
	seenVol := map[string]bool{}
	var sidecars []File
	seenSidecar := map[string]bool{}
	usedKeys := map[string]bool{}
	for _, in := range instances {
		// Built-in blocks carry declared variables and are rendered; user blocks
		// declare none and are copied literally, so a stray "{{" in user YAML can't
		// break assembly.
		svc := in.Block.ServiceYAML
		if in.Block.Source == SourceBuiltin {
			rendered, err := renderString("block:"+in.Block.ID, svc, vars)
			if err != nil {
				return nil, err
			}
			svc = rendered
		}
		key := strings.TrimSpace(in.Key)
		if key == "" {
			key = in.Block.Service
		}
		// Defensively guarantee unique service keys even if a client posts blank or
		// duplicate keys — otherwise two instances would collide into one compose
		// service (a map key) and silently drop a service.
		if usedKeys[key] {
			base := key
			for n := 2; usedKeys[key]; n++ {
				key = fmt.Sprintf("%s-%d", base, n)
			}
		}
		usedKeys[key] = true
		volMap := map[string]string{}
		for _, v := range in.Block.Volumes {
			nv := v
			if volCount[v] > 1 {
				nv = key + "-" + v
			}
			volMap[v] = nv
			if !seenVol[nv] {
				seenVol[nv] = true
				topVols = append(topVols, nv)
			}
		}
		b.WriteString(placeInstance(svc, key, in.MergeAnchors, volMap))
		b.WriteString("\n")
		for _, f := range in.Block.Files {
			if !seenSidecar[f.Path] {
				seenSidecar[f.Path] = true
				sidecars = append(sidecars, f)
			}
		}
	}
	if len(topVols) > 0 {
		b.WriteString("\nvolumes:\n")
		for _, v := range topVols {
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

// placeInstance rewrites a rendered service body for one instance: it renames the
// (single, top-level) service key to key, injects a `<<: *anchor` merge line
// right after it, and rewrites named-volume mounts via volRename. The service
// key is the first 2-indent `name:` line; everything else is preserved verbatim.
func placeInstance(svcYAML, key string, anchors []string, volRename map[string]string) string {
	lines := strings.Split(svcYAML, "\n")
	out := make([]string, 0, len(lines)+1)
	keyDone := false
	for _, ln := range lines {
		if !keyDone {
			if m := svcKeyLine.FindStringSubmatch(ln); m != nil {
				out = append(out, m[1]+key+":")
				keyDone = true
				if len(anchors) > 0 {
					out = append(out, m[1]+"  "+mergeLine(anchors))
				}
				continue
			}
		}
		// Rename a named-volume mount, but only when the line *is* that mount list
		// item (`      - vol:/path`) — never when "- vol:" merely appears inside a
		// command/env string value.
		trimmed := strings.TrimSpace(ln)
		for v, nv := range volRename {
			if v != nv && strings.HasPrefix(trimmed, "- "+v+":") {
				ln = strings.Replace(ln, "- "+v+":", "- "+nv+":", 1)
				break
			}
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func mergeLine(anchors []string) string {
	// De-duplicate so two fragments sharing an anchor name can't yield the
	// compose-invalid `<<: [*a, *a]`.
	seen := map[string]bool{}
	var uniq []string
	for _, a := range anchors {
		if !seen[a] {
			seen[a] = true
			uniq = append(uniq, a)
		}
	}
	if len(uniq) == 1 {
		return "<<: *" + uniq[0]
	}
	parts := make([]string, len(uniq))
	for i, a := range uniq {
		parts[i] = "*" + a
	}
	return "<<: [" + strings.Join(parts, ", ") + "]"
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
