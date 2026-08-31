package passphrase

import (
	"bytes"
	"testing"
)

var testMagic = []byte("TEST1\n")

func TestRoundTripPlain(t *testing.T) {
	var buf bytes.Buffer
	plain := []byte("hello, world")
	if err := WritePlainTo(&buf, testMagic, bytes.NewReader(plain)); err != nil {
		t.Fatalf("WritePlainTo: %v", err)
	}

	encrypted, err := ReadFlag(&buf, testMagic)
	if err != nil {
		t.Fatalf("ReadFlag: %v", err)
	}
	if encrypted {
		t.Fatal("expected a plain archive to report encrypted=false")
	}
	got := buf.Bytes()
	if !bytes.Equal(got, plain) {
		t.Errorf("plain payload = %q, want %q", got, plain)
	}
}

func TestRoundTripEncrypted(t *testing.T) {
	var buf bytes.Buffer
	plain := []byte("a secret payload that must round-trip exactly")
	if err := SealTo(&buf, testMagic, plain, "correct horse battery staple"); err != nil {
		t.Fatalf("SealTo: %v", err)
	}

	encrypted, err := ReadFlag(&buf, testMagic)
	if err != nil {
		t.Fatalf("ReadFlag: %v", err)
	}
	if !encrypted {
		t.Fatal("expected an encrypted archive to report encrypted=true")
	}
	got, err := OpenFrom(&buf, testMagic, "correct horse battery staple")
	if err != nil {
		t.Fatalf("OpenFrom: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("decrypted payload = %q, want %q", got, plain)
	}
}

func TestOpenFrom_WrongPassphraseFails(t *testing.T) {
	var buf bytes.Buffer
	if err := SealTo(&buf, testMagic, []byte("payload"), "right-passphrase"); err != nil {
		t.Fatalf("SealTo: %v", err)
	}
	if _, err := ReadFlag(&buf, testMagic); err != nil {
		t.Fatalf("ReadFlag: %v", err)
	}
	if _, err := OpenFrom(&buf, testMagic, "wrong-passphrase"); err == nil {
		t.Error("expected an error decrypting with the wrong passphrase")
	}
}

func TestOpenFrom_EmptyPassphraseReturnsErrPassphraseRequired(t *testing.T) {
	var buf bytes.Buffer
	if err := SealTo(&buf, testMagic, []byte("payload"), "some-passphrase"); err != nil {
		t.Fatalf("SealTo: %v", err)
	}
	if _, err := ReadFlag(&buf, testMagic); err != nil {
		t.Fatalf("ReadFlag: %v", err)
	}
	if _, err := OpenFrom(&buf, testMagic, ""); err != ErrPassphraseRequired {
		t.Errorf("OpenFrom with empty passphrase = %v, want ErrPassphraseRequired", err)
	}
}

func TestReadFlag_BadMagicRejected(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("NOT-THE-RIGHT-MAGIC")
	if _, err := ReadFlag(&buf, testMagic); err != ErrBadMagic {
		t.Errorf("ReadFlag with bad magic = %v, want ErrBadMagic", err)
	}
}

func TestReadFlag_TruncatedInputRejected(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(testMagic[:len(testMagic)-1]) // short, no flag byte at all
	if _, err := ReadFlag(&buf, testMagic); err != ErrBadMagic {
		t.Errorf("ReadFlag with truncated input = %v, want ErrBadMagic", err)
	}
}

// A sealed payload is bound to its magic via AEAD associated data — sealing
// under one magic and trying to open under a different one (as if a recovery
// bundle's bytes were fed to the backup reader, or vice versa) must fail
// rather than silently "working" with the wrong format assumptions.
func TestOpenFrom_MagicMismatchFailsEvenWithCorrectPassphrase(t *testing.T) {
	var buf bytes.Buffer
	if err := SealTo(&buf, []byte("MAGIC-A"), []byte("payload"), "pw"); err != nil {
		t.Fatalf("SealTo: %v", err)
	}
	data := buf.Bytes()
	// Skip past MAGIC-A's own header (ReadFlag under the wrong magic will
	// itself fail first) to isolate the AEAD-AAD check: reconstruct a stream
	// claiming MAGIC-B's flag/salt/nonce/len/sealed body from MAGIC-A's actual
	// encrypted bytes, minus the magic+flag prefix which differs in length
	// only if magics differ in length — use equal-length magics here.
	altMagic := []byte("MAGIC-B")
	body := data[len(altMagic)+1:] // same length prefix as "MAGIC-A"+flag
	r := bytes.NewReader(body)
	if _, err := OpenFrom(r, altMagic, "pw"); err == nil {
		t.Error("expected OpenFrom to fail when the magic used as AAD doesn't match what was sealed")
	}
}
