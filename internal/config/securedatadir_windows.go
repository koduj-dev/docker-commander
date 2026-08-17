//go:build windows

package config

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SecureDataDirACL applies a protected (non-inherited) DACL to path granting
// FullControl to SYSTEM and Administrators only. `0o700` on MkdirAll is a
// no-op on Windows — the Go runtime does not translate Unix mode bits into an
// NTFS ACL — so without this the directory inherits whatever the parent
// (typically %ProgramData%) already grants, which can include ordinary local
// accounts. This directory holds the SQLite DB (registry credentials,
// encrypted secrets, session state) and, when TLS is self-managed, the
// private key — see docs/gotchas.md.
//
// Exported so both Load (every startup, regardless of how dockercmd was
// launched — foreground, the Scheduled Task installer, or the native SCM
// service) and internal/service's Windows installer (which additionally
// verifies a pre-existing dir before trusting it — see VerifyDataDirACL) call
// the exact same implementation rather than two copies that could drift.
//
// The built-in Administrators group is granted by its well-known SID, not
// the localized name "Administrators" — that string is translated on
// non-English Windows installs (e.g. "Administratoren", "Administrateurs"),
// while a well-known SID resolves identically regardless of locale.
func SecureDataDirACL(path string) error {
	systemSid, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve SYSTEM sid: %w", err)
	}
	adminSid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("resolve Administrators sid: %w", err)
	}

	inherit := uint32(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(systemSid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(adminSid),
			},
		},
	}

	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build ACL: %w", err)
	}

	// PROTECTED_DACL_SECURITY_INFORMATION strips inherited ACEs (e.g. a
	// permissive one inherited from %ProgramData% itself) so only the two
	// explicit grants above apply.
	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
	if err != nil {
		return fmt.Errorf("apply ACL to %s: %w", path, err)
	}
	return nil
}

// VerifyDataDirACL refuses a pre-existing data dir whose DACL grants access
// to anything beyond SYSTEM, Administrators, or CREATOR OWNER (an inherited
// default that doesn't itself widen access). Used only at the explicit,
// interactive install step (internal/service's Windows installer) before
// SecureDataDirACL repairs a fresh/legitimate dir's ACL — a dir that already
// grants broader access (planted, misconfigured, or left over from something
// else entirely) is surfaced for manual inspection there instead of silently
// adopted and "fixed" as if it had always been fine. Load does not call this:
// an ordinary service start must not fail just because a directory looked
// odd — the interactive install is where a human is present to act on it.
func VerifyDataDirACL(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read existing ACL: %w", err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read existing DACL: %w", err)
	}
	if acl == nil {
		return errors.New("existing data dir has no DACL (fully permissive) — remove it or repair its ACL by hand")
	}
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return fmt.Errorf("read ACE %d: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue // deny/audit ACEs aren't a disclosure risk
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsWellKnown(windows.WinLocalSystemSid) &&
			!sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) &&
			!sid.IsWellKnown(windows.WinCreatorOwnerSid) {
			return fmt.Errorf("existing data dir grants access to unexpected SID %s — inspect it manually before reinstalling", sid.String())
		}
	}
	return nil
}

// secureDataDir is Load's own hook (see config.go): always re-apply, never
// refuse — see SecureDataDirACL's doc comment for why VerifyDataDirACL is not
// called here too. A var (not a plain func) so a test can override it to
// prove Load actually calls and checks it, without needing a real Windows
// ACL engine.
var secureDataDir = func(dir string) error { return SecureDataDirACL(dir) }
