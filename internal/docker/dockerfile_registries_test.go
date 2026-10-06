package docker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"reflect"
	"testing"
)

func TestDockerfileImageRefs(t *testing.T) {
	cases := []struct {
		name       string
		dockerfile string
		args       map[string]string
		refs       []string
		unresolved []string
	}{
		{"plain", "FROM nginx:1.27\n", nil, []string{"nginx:1.27"}, nil},
		{"platform flag and stage", "FROM --platform=$BUILDPLATFORM golang:1.26 AS build\nFROM build\nFROM scratch\n",
			nil, []string{"golang:1.26"}, nil},
		{"global ARG default", "ARG BASE=ghcr.io/acme/base:1\nFROM ${BASE}\n", nil, []string{"ghcr.io/acme/base:1"}, nil},
		{"build arg overrides a declared ARG", "ARG REG=docker.io\nFROM $REG/library/alpine\n",
			map[string]string{"REG": "registry.acme.io"}, []string{"registry.acme.io/library/alpine"}, nil},
		{"build arg for an undeclared ARG is not used", "FROM ${REG}/alpine\n",
			map[string]string{"REG": "registry.acme.io"}, nil, []string{"${REG}/alpine"}},
		{"default modifier", "FROM ${REG:-quay.io}/org/img\n", nil, []string{"quay.io/org/img"}, nil},
		{"ARG after FROM doesn't reach the next FROM", "FROM alpine\nARG X=ghcr.io/x\nFROM $X/y\n",
			nil, []string{"alpine"}, []string{"$X/y"}},
		{"COPY --from an image, a stage and an index", "FROM alpine AS a\nCOPY --from=a /x /x\nCOPY --from=0 /y /y\nCOPY --chown=1:1 --from=ghcr.io/acme/tools:2 /bin/t /t\n",
			nil, []string{"alpine", "ghcr.io/acme/tools:2"}, nil},
		{"continuation and comments", "# FROM evil.example/x\nFROM \\\n  registry.acme.io/app:1 \\\n  AS app\n",
			nil, []string{"registry.acme.io/app:1"}, nil},
		{"lower-case instruction", "from quay.io/a/b\n", nil, []string{"quay.io/a/b"}, nil},
		{"heredoc body is file content, not instructions", "FROM ghcr.io/acme/base:1\nCOPY <<EOF /note.txt\nFROM docker.io/library/alpine:latest\nEOF\n",
			nil, []string{"ghcr.io/acme/base:1"}, nil},
		{"several heredocs, <<- and a quoted delimiter", "FROM alpine\nRUN <<-'A' <<B\n\tFROM evil.example/a\n\tA\nFROM evil.example/b\nB\nFROM quay.io/real/one\n",
			nil, []string{"alpine", "quay.io/real/one"}, nil},
		{"escape directive: backtick continues a line", "# escape=`\nFROM `\n  private.example/base:1\nRUN dir C:\\\n", nil,
			[]string{"private.example/base:1"}, nil},
		{"a directive after the first instruction is just a comment", "FROM alpine\n# escape=`\nFROM a.example/x \\\n  AS b\n", nil,
			[]string{"alpine", "a.example/x"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refs, unresolved, err := dockerfileImageRefs(c.dockerfile, c.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(refs, c.refs) {
				t.Errorf("refs = %q, want %q", refs, c.refs)
			}
			if !reflect.DeepEqual(unresolved, c.unresolved) {
				t.Errorf("unresolved = %q, want %q", unresolved, c.unresolved)
			}
		})
	}
}

func tarOf(t *testing.T, files map[string]string, gz bool) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var tw *tar.Writer
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		tw = tar.NewWriter(zw)
	} else {
		tw = tar.NewWriter(&buf)
	}
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return &buf
}

func TestBuildRegistries(t *testing.T) {
	df := "FROM nginx\nFROM ghcr.io/acme/x AS b\nCOPY --from=registry.acme.io:5000/t /a /a\n"
	want := []string{"docker.io", "ghcr.io", "registry.acme.io:5000"}

	for _, gz := range []bool{false, true} {
		hosts, _, err := BuildRegistries(tarOf(t, map[string]string{"./Dockerfile": df, "app.go": "x"}, gz), "", nil)
		if err != nil || !reflect.DeepEqual(hosts, want) {
			t.Errorf("gzip=%v: hosts %q err %v, want %q", gz, hosts, err, want)
		}
	}

	// A custom Dockerfile path, and a decoy at the default one.
	ctx := tarOf(t, map[string]string{"Dockerfile": "FROM decoy.example/x\n", "build/prod.Dockerfile": "FROM quay.io/a/b\n"}, false)
	hosts, _, err := BuildRegistries(ctx, "build/prod.Dockerfile", nil)
	if err != nil || !reflect.DeepEqual(hosts, []string{"quay.io"}) {
		t.Errorf("custom path: hosts %q err %v", hosts, err)
	}

	// Read without its escape directive, this file's FROM is a lone backtick.
	// Text that isn't an image reference must not be taken for a Docker Hub
	// image and get the Hub credential.
	hosts, unresolved, err := BuildRegistries(tarOf(t, map[string]string{"Dockerfile": "FROM `\n  private.example/base:1\n"}, false), "", nil)
	if err != nil || len(hosts) != 0 || len(unresolved) != 1 {
		t.Errorf("a non-reference FROM: hosts %q unresolved %q err %v, want no host", hosts, unresolved, err)
	}

	if _, _, err := BuildRegistries(tarOf(t, map[string]string{"x": "y"}, false), "", nil); !errors.Is(err, errNoDockerfile) {
		t.Errorf("no Dockerfile: err %v", err)
	}
}
