package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// maxDockerfileBytes bounds the Dockerfile read out of a build context. A real
// one is a few KiB; anything past this is not one.
const maxDockerfileBytes = 1 << 20

// errNoDockerfile reports a build context without the named Dockerfile.
var errNoDockerfile = errors.New("the build context has no such Dockerfile")

// BuildRegistries reads the Dockerfile out of a build context (a tar, gzip or
// not) and returns the registry hosts its images come from: every FROM, and
// every COPY/ADD --from that names an image rather than a stage. unresolved
// lists image references whose registry can't be told (a variable with no
// value), for a warning.
//
// It decides which stored credentials a build sends. The daemon gets them all
// in one header before it reads a byte of the context, so sending every one
// handed a remote host the credentials of registries it had nothing to do with.
func BuildRegistries(buildContext io.Reader, dockerfile string, buildArgs map[string]string) (hosts, unresolved []string, err error) {
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	want := path.Clean(strings.TrimPrefix(dockerfile, "/"))
	br := bufio.NewReader(buildContext)
	var r io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, nil, errNoDockerfile
		}
		if err != nil {
			return nil, nil, err
		}
		if h.Typeflag != tar.TypeReg || path.Clean(strings.TrimPrefix(h.Name, "./")) != want {
			continue
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, io.LimitReader(tr, maxDockerfileBytes)); err != nil {
			return nil, nil, err
		}
		refs, unresolved, err := dockerfileImageRefs(b.String(), buildArgs)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", want, err)
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			named, err := reference.ParseNormalizedNamed(ref)
			if err != nil { // dockerfileImageRefs only returns ones that parse
				unresolved = append(unresolved, ref)
				continue
			}
			h := store.NormalizeRegistryHost(reference.Domain(named))
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
		sort.Strings(hosts)
		return hosts, unresolved, nil
	}
}

// dockerfileImageRefs returns the image references a Dockerfile pulls, with
// ARG values and build args substituted the way Docker does it: a FROM sees
// only the ARGs declared before the first FROM; a COPY --from also sees the
// ARGs of its own stage. A build arg overrides an ARG's default only where that
// ARG is declared. Stage names, stage indexes and scratch are not images.
//
// The Dockerfile is read by BuildKit's own parser, so parser directives
// (`# escape=`), line continuations and heredoc bodies are what Docker makes
// of them. A hand-rolled line reader took a FROM inside a heredoc for a real
// one, and sent the daemon a credential for that registry. A file that doesn't
// parse is an error: no credentials at all.
//
// This is BuildKit's only importer. The module brings a large go.mod graph
// for one parser, so it may be replaced by a smaller one; the table tests here
// and TestPentestBuildSendsOnlyTheRegistriesItUses define what a replacement
// must keep. See docs/gotchas.md.
func dockerfileImageRefs(content string, buildArgs map[string]string) (refs, unresolved []string, err error) {
	res, err := parser.Parse(strings.NewReader(content))
	if err != nil {
		return nil, nil, err
	}
	lex := shell.NewLex(res.EscapeToken)
	expand := func(word string, vars map[string]string) (string, bool) {
		env := make([]string, 0, len(vars))
		for k, v := range vars {
			env = append(env, k+"="+v)
		}
		// What Docker makes of it, unset variables included: `${X:-d}` gives
		// d, and a bare unset `$X` gives "". Whether the result still names an
		// image is decided by parsing it as one, below.
		out, err := lex.ProcessWordWithMatches(word, shell.EnvsFromSlice(env))
		if err != nil {
			return "", false
		}
		return out.Result, true
	}

	global := map[string]string{}
	var stage map[string]string // nil until the first FROM
	stages := map[string]bool{}
	add := func(ref string, vars map[string]string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.EqualFold(ref, "scratch") || stages[strings.ToLower(ref)] {
			return
		}
		if strings.Trim(ref, "0123456789") == "" { // COPY --from=0: a stage index
			return
		}
		x, ok := expand(ref, vars)
		if ok && (strings.EqualFold(x, "scratch") || stages[strings.ToLower(x)]) {
			return
		}
		// Only a reference that parses as one says which registry it is.
		// Anything else gets no credential, rather than one guessed from text
		// that isn't an image name.
		if _, err := reference.ParseNormalizedNamed(x); !ok || err != nil {
			unresolved = append(unresolved, ref)
			return
		}
		refs = append(refs, x)
	}

	for _, n := range res.AST.Children {
		switch strings.ToLower(n.Value) {
		case "arg":
			target := global
			if stage != nil {
				target = stage
			}
			for a := n.Next; a != nil; a = a.Next {
				name, val, hasVal := strings.Cut(a.Value, "=")
				switch v, given := buildArgs[name]; {
				case given:
					target[name] = v
				case hasVal:
					if x, ok := expand(val, target); ok {
						target[name] = x
					}
				case stage != nil:
					// Redeclaring a global ARG inside a stage brings its value in.
					if v, ok := global[name]; ok {
						target[name] = v
					}
				}
			}
		case "from":
			stage = map[string]string{}
			if n.Next == nil {
				continue
			}
			add(n.Next.Value, global)
			if as := n.Next.Next; as != nil && strings.EqualFold(as.Value, "AS") && as.Next != nil {
				stages[strings.ToLower(as.Next.Value)] = true
			}
		case "copy", "add":
			for _, f := range n.Flags {
				if from, ok := strings.CutPrefix(f, "--from="); ok {
					add(from, stage)
				}
			}
		}
	}
	return refs, unresolved, nil
}
