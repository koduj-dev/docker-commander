package docker

import "testing"

// PENTEST: a hostile or compromised remote host must not be able to OOM this
// process by returning a huge amount of output from a diagnostics probe
// command. boundedWriter is what caps it — this both proves the cap holds and
// proves Write never errors/short-returns, which matters because sshExec
// wires it directly onto an SSH session's Stdout/Stderr: a Write that errored
// there would stall the session's read loop instead of just discarding bytes.
func TestBoundedWriter_CapsOutput(t *testing.T) {
	w := &boundedWriter{max: 256 * 1024}
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	var totalWritten int
	for i := 0; i < 160; i++ { // 160 * 64KB = 10MB total attempted
		n, err := w.Write(chunk)
		if err != nil {
			t.Fatalf("Write returned an error on iteration %d: %v", i, err)
		}
		if n != len(chunk) {
			t.Fatalf("Write on iteration %d returned short count %d, want %d (a short return would stall the SSH session's read loop)", i, n, len(chunk))
		}
		totalWritten += n
	}
	if totalWritten != 160*64*1024 {
		t.Fatalf("test bug: totalWritten = %d", totalWritten)
	}
	if len(w.buf) != w.max {
		t.Errorf("buf len = %d, want exactly the cap %d — SECURITY: unbounded output was retained", len(w.buf), w.max)
	}
}

func TestBoundedWriter_UnderCapKeepsEverything(t *testing.T) {
	w := &boundedWriter{max: 1024}
	n, err := w.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if w.String() != "hello" {
		t.Errorf("String() = %q, want %q", w.String(), "hello")
	}
}
