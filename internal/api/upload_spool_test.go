package api

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// spoolUpload exists because the tar header needs the size up front, and the old
// answer was to hold the whole body in memory with a 4 GiB ceiling — enough for
// one request to drive RSS into the gigabytes (io.ReadAll doubles as it grows)
// and a few concurrent ones to OOM-kill the process.
func TestSpoolUploadProducesATarOfTheBody(t *testing.T) {
	payload := bytes.Repeat([]byte("payload\n"), 4096)
	r := httptest.NewRequest("POST", "/upload", bytes.NewReader(payload))
	w := httptest.NewRecorder()

	content, size, err := spoolUpload(w, r, "app.conf")
	if err != nil {
		t.Fatalf("spoolUpload: %v", err)
	}
	defer content.Close()
	if size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", size, len(payload))
	}

	tr := tar.NewReader(content)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("reading the tar: %v", err)
	}
	if hdr.Name != "app.conf" || hdr.Size != int64(len(payload)) {
		t.Errorf("header = %q/%d, want app.conf/%d", hdr.Name, hdr.Size, len(payload))
	}
	got, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("reading the entry: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("the archived bytes differ from what was uploaded")
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("expected exactly one entry, got another (%v)", err)
	}
}

// A body past the ceiling is refused rather than spooled, so neither memory nor
// disk absorbs it.
func TestSpoolUploadRefusesAnOversizedBody(t *testing.T) {
	r := httptest.NewRequest("POST", "/upload", strings.NewReader(""))
	r.Body = io.NopCloser(io.LimitReader(zeroes{}, maxUploadBytes+(1<<20)))
	w := httptest.NewRecorder()

	if _, _, err := spoolUpload(w, r, "big.bin"); !errors.Is(err, errUploadTooBig) {
		t.Fatalf("want errUploadTooBig, got %v", err)
	}
}

// Closing the reader must release the spool file — the descriptor is the only
// handle on it (the file is unlinked at creation), so a leak here is a leak of
// disk that nothing can reclaim until the process exits.
func TestSpoolUploadReleasesItsTempFile(t *testing.T) {
	r := httptest.NewRequest("POST", "/upload", strings.NewReader("hello"))
	content, _, err := spoolUpload(httptest.NewRecorder(), r, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(content); err != nil {
		t.Fatalf("draining: %v", err)
	}
	if err := content.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
