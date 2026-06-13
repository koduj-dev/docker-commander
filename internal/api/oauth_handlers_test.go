package api

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	if !verifyPKCE(verifier, challenge) {
		t.Fatal("matching verifier/challenge should pass")
	}
	if verifyPKCE("wrong-verifier", challenge) {
		t.Fatal("non-matching verifier must fail")
	}
	if verifyPKCE("", challenge) || verifyPKCE(verifier, "") {
		t.Fatal("empty verifier or challenge must fail")
	}
	// A plain-method-style challenge (verifier == challenge) must NOT pass S256.
	if verifyPKCE(verifier, verifier) {
		t.Fatal("plain challenge must not satisfy S256")
	}
}

func TestValidRedirectURI(t *testing.T) {
	ok := []string{
		"https://claude.ai/api/mcp/auth_callback",
		"https://app.example.com:8443/cb",
		"http://localhost:1455/callback",
		"http://127.0.0.1/cb",
	}
	bad := []string{
		"http://evil.example.com/cb",  // plain http, non-loopback
		"ftp://example.com/cb",        // wrong scheme
		"https://example.com/cb#frag", // fragment not allowed
		"app://callback",              // custom scheme unsupported in MVP
		"not a url at all %%%",        // unparseable
		"",                            // empty
	}
	for _, u := range ok {
		if !validRedirectURI(u) {
			t.Errorf("expected valid: %q", u)
		}
	}
	for _, u := range bad {
		if validRedirectURI(u) {
			t.Errorf("expected invalid: %q", u)
		}
	}
}
