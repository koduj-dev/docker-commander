package docker

import "testing"

func TestDiskFree_RealFilesystem(t *testing.T) {
	total, free, err := diskFree(".")
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Error("total = 0, want a real filesystem size")
	}
	if free > total {
		t.Errorf("free (%d) > total (%d)", free, total)
	}
}

func TestDiskFree_NonexistentPath(t *testing.T) {
	if _, _, err := diskFree("/this/path/should/not/exist/anywhere/xyz123"); err == nil {
		t.Error("expected an error for a nonexistent path")
	}
}
