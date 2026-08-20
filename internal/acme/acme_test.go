package acme

import (
	"context"
	"testing"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

func TestNewManager_HostPolicyWhitelistsOnlyConfiguredDomains(t *testing.T) {
	mgr := NewManager([]string{"example.com", "www.example.com"}, "", t.TempDir(), "")

	if err := mgr.HostPolicy(context.Background(), "example.com"); err != nil {
		t.Errorf("a configured domain should be allowed: %v", err)
	}
	if err := mgr.HostPolicy(context.Background(), "www.example.com"); err != nil {
		t.Errorf("a configured domain should be allowed: %v", err)
	}
	// SECURITY: an unconfigured SNI name must be refused a certificate, not
	// silently attempted — otherwise a client connecting by IP and lying about
	// SNI could make this server burn its rate-limit budget on arbitrary names.
	if err := mgr.HostPolicy(context.Background(), "not-configured.example.com"); err == nil {
		t.Error("SECURITY: an unconfigured hostname was accepted by HostPolicy")
	}
	if err := mgr.HostPolicy(context.Background(), "evil.example"); err == nil {
		t.Error("SECURITY: an arbitrary hostname was accepted by HostPolicy")
	}
}

func TestNewManager_CachesUnderTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager([]string{"example.com"}, "", dir, "")
	got, ok := mgr.Cache.(autocert.DirCache)
	if !ok {
		t.Fatalf("Cache = %T, want autocert.DirCache (nil Cache only holds certs for the process lifetime)", mgr.Cache)
	}
	if string(got) != dir {
		t.Errorf("Cache dir = %q, want %q", got, dir)
	}
}

func TestNewManager_EmailAndDirectoryURL(t *testing.T) {
	mgr := NewManager([]string{"example.com"}, "ops@example.com", t.TempDir(), "")
	if mgr.Email != "ops@example.com" {
		t.Errorf("Email = %q, want ops@example.com", mgr.Email)
	}
	if mgr.Client != nil {
		t.Error("Client should be nil (default directory) when no directoryURL override is given")
	}

	mgr2 := NewManager([]string{"example.com"}, "", t.TempDir(), "https://pebble.local/dir")
	if mgr2.Client == nil {
		t.Fatal("Client should be set when a directoryURL override is given")
	}
	if got, ok := any(mgr2.Client).(*acme.Client); !ok || got.DirectoryURL != "https://pebble.local/dir" {
		t.Errorf("Client.DirectoryURL not wired to the override: %+v", mgr2.Client)
	}
}

func TestNewManager_AcceptsTOSWithoutPrompting(t *testing.T) {
	mgr := NewManager([]string{"example.com"}, "", t.TempDir(), "")
	if mgr.Prompt == nil {
		t.Fatal("Prompt should be set — a nil Prompt means autocert refuses to register at all")
	}
	if !mgr.Prompt("https://example.com/tos") {
		t.Error("Prompt should always accept (autocert.AcceptTOS) so the server doesn't hang waiting for interactive consent")
	}
}
