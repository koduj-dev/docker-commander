package auth

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestPasswordHashRoundtrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
	bad, _ := VerifyPassword("wrong password", hash)
	if bad {
		t.Fatal("expected mismatch for wrong password")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("x", "not-a-hash"); err == nil {
		t.Fatal("expected error for malformed hash")
	}
}

func TestTOTPEnrollmentValidates(t *testing.T) {
	enr, err := GenerateTOTP("admin")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Generate the code the user's authenticator app would show right now.
	code, err := totp.GenerateCode(enr.Secret, time.Now())
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	if !ValidateTOTP(code, enr.Secret) {
		t.Fatal("expected the freshly generated code to validate")
	}
	if ValidateTOTP("000000", enr.Secret) {
		t.Fatal("expected a bogus code to be rejected")
	}
}

func TestTokenIssueParse(t *testing.T) {
	tm := NewTokenManager([]byte("test-secret-test-secret-32bytes!"), time.Hour)
	tok, _, err := tm.Issue(42, "admin", "admin", KindSession)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := tm.Parse(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != 42 || claims.Kind != KindSession {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}
