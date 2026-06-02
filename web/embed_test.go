package web

import "testing"

func TestDistFS(t *testing.T) {
	// The committed web/dist lets the binary build without Node; DistFS exposes
	// it (and index.html) for the SPA handler.
	fsys, err := DistFS()
	if err != nil {
		t.Fatalf("DistFS: %v", err)
	}
	if _, err := fsys.Open("index.html"); err != nil {
		t.Errorf("embedded dist should contain index.html: %v", err)
	}
}
