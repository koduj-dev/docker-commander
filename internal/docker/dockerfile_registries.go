package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

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
		refs, unresolved := dockerfileImageRefs(b.String(), buildArgs)
		seen := map[string]bool{}
		for _, ref := range refs {
			h := store.NormalizeRegistryHost(registryHost(ref))
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
		sort.Strings(hosts)
		return hosts, unresolved, nil
	}
}

var (
	argVar      = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:?[-+][^}]*)?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	copyFromArg = regexp.MustCompile(`(?i)^--from=(\S+)$`)
)

// dockerfileImageRefs returns the image references a Dockerfile pulls, with
// ARG values and build args substituted the way Docker does it: a FROM sees
// only the ARGs declared before the first FROM; a COPY --from also sees the
// ARGs of its own stage. A build arg overrides an ARG's default only where that
// ARG is declared. Stage names, stage indexes and scratch are not images.
func dockerfileImageRefs(content string, buildArgs map[string]string) (refs, unresolved []string) {
	global := map[string]string{}
	var stage map[string]string // nil until the first FROM
	stages := map[string]bool{}
	expand := func(s string, args map[string]string) (string, bool) {
		ok := true
		out := argVar.ReplaceAllStringFunc(s, func(m string) string {
			sub := argVar.FindStringSubmatch(m)
			name, mod := sub[1], sub[2]
			if name == "" {
				name = sub[3]
			}
			v, set := args[name]
			switch {
			case strings.HasPrefix(mod, ":-") || strings.HasPrefix(mod, "-"):
				if !set || (v == "" && strings.HasPrefix(mod, ":")) {
					return strings.TrimLeft(mod, ":-")
				}
			case strings.HasPrefix(mod, ":+") || strings.HasPrefix(mod, "+"):
				if set && (v != "" || !strings.HasPrefix(mod, ":")) {
					return strings.TrimLeft(mod, ":+")
				}
				return ""
			}
			if !set {
				ok = false
			}
			return v
		})
		return out, ok
	}
	add := func(ref string, args map[string]string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.EqualFold(ref, "scratch") || stages[strings.ToLower(ref)] {
			return
		}
		if strings.Trim(ref, "0123456789") == "" { // COPY --from=0: a stage index
			return
		}
		x, ok := expand(ref, args)
		if !ok || strings.Contains(x, "$") || x == "" {
			unresolved = append(unresolved, ref)
			return
		}
		if strings.EqualFold(x, "scratch") || stages[strings.ToLower(x)] {
			return
		}
		refs = append(refs, x)
	}

	for _, line := range dockerfileLines(content) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "ARG":
			target := global
			if stage != nil {
				target = stage
			}
			for _, f := range fields[1:] {
				name, val, hasVal := strings.Cut(f, "=")
				if v, given := buildArgs[name]; given {
					target[name] = v
				} else if hasVal {
					target[name] = strings.Trim(val, `"'`)
				} else if stage != nil {
					// Redeclaring a global ARG inside a stage brings its value in.
					if v, ok := global[name]; ok {
						target[name] = v
					}
				}
			}
		case "FROM":
			stage = map[string]string{}
			rest := fields[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				continue
			}
			add(rest[0], global)
			if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
				stages[strings.ToLower(rest[2])] = true
			}
		case "COPY", "ADD":
			for _, f := range fields[1:] {
				if !strings.HasPrefix(f, "--") {
					break
				}
				if m := copyFromArg.FindStringSubmatch(f); m != nil {
					add(m[1], stage)
				}
			}
		}
	}
	return refs, unresolved
}

// dockerfileLines joins continuation lines and drops comments.
func dockerfileLines(content string) []string {
	var out []string
	var cur strings.Builder
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			cur.WriteString(strings.TrimSuffix(line, `\`))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
