package docker

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"testing"
)

// PENTEST: gzip of a repetitive payload expands about a thousandfold, so a
// modest upload becomes gigabytes written into the container — onto the host's
// filesystem, where it fills the disk out from under everything else on it.
//
// The zip branch has always been capped. The gzip one was not, while the comment
// on maxExtractBytes claimed both were, which is how it stayed unnoticed.
func TestPentestCappedReaderRefusesADecompressionBomb(t *testing.T) {
	// A gzip stream that expands well past the cap. Zeroes compress ~1000:1, so
	// this stays small on disk while promising far more on the way out.
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	chunk := make([]byte, 1<<20) // 1 MiB of zeroes
	for range (maxExtractBytes >> 20) + 8 {
		if _, err := zw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, cappedReader(gz))
	if !errors.Is(err, errArchiveTooBig) {
		t.Fatalf("a bomb must be refused, got err=%v after %d bytes", err, n)
	}
	if n > maxExtractBytes+(1<<20) {
		t.Errorf("refusal came late: %d bytes through a %d cap", n, maxExtractBytes)
	}
}

// The counterweight: an ordinary archive must pass through untouched, byte for
// byte. A cap that mangles or truncates normal uploads is a worse bug than the
// one it prevents.
func TestCappedReaderPassesOrdinaryContentThrough(t *testing.T) {
	payload := bytes.Repeat([]byte("docker-commander\n"), 5000)
	got, err := io.ReadAll(cappedReader(bytes.NewReader(payload)))
	if err != nil {
		t.Fatalf("an ordinary upload must not error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("content changed in transit: %d bytes in, %d out", len(payload), len(got))
	}
}
