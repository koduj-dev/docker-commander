//go:build windows

package config

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dataDirAccessRights is what a caller needs on the data dir handle to both
// read its current owner/DACL (VerifyDataDirACL) and rewrite them
// (SecureDataDirACL) — READ_CONTROL is implicit in WRITE_DAC/WRITE_OWNER's
// GENERIC_READ, kept explicit here for clarity.
const dataDirAccessRights = windows.GENERIC_READ | windows.WRITE_DAC | windows.WRITE_OWNER

// openDataDirHandle opens path WITHOUT following it if it is a reparse point
// (a symlink or NTFS junction) and refuses outright if it is one, returning
// an open handle on success.
//
// FILE_FLAG_OPEN_REPARSE_POINT makes CreateFile open the reparse point
// object itself rather than transparently redirecting through it —
// otherwise a locally-planted junction pointing the data dir path at some
// other location would have this code faithfully secure/verify the WRONG
// directory while a privileged (SYSTEM, under the service) process went on
// to read/write through the same path into whatever the junction actually
// points at. FILE_FLAG_BACKUP_SEMANTICS is required to open a directory
// handle via CreateFile at all.
//
// The returned handle is also what closes the TOCTOU window a path-based
// verify-then-secure sequence would otherwise have: both the read (owner/DACL
// check) and the write (new owner/DACL) happen against the SAME open handle,
// not two independent path lookups an attacker could swap between.
func openDataDirHandle(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("encode path %s: %w", path, err)
	}
	h, err := windows.CreateFile(p, dataDirAccessRights,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("%s is a reparse point (symlink/junction) — refusing to treat a redirected "+
			"path as the data dir; if this is intentional, point -data-dir at the real target directly", path)
	}
	return h, nil
}

// currentUserSID resolves the SID of whoever this process is actually
// running as. Best-effort: a lookup failure is not itself fatal to the
// caller, since SYSTEM + Administrators are granted either way.
func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid, nil
}

// SecureDataDirACL applies a protected (non-inherited) DACL to path granting
// FullControl to SYSTEM, Administrators, and — if it can be resolved — the
// user this process is actually running as, and sets the owner to
// Administrators.
//
// The three-principal DACL (not just SYSTEM+Administrators) is what makes
// this safe to call from EVERY startup path, not just the SCM-service
// install: dockercmd running in a plain console under an ordinary,
// non-Administrator account is a supported way to run it, and a DACL that
// only ever granted SYSTEM/Administrators would lock that very account out
// of the data dir this same call just secured — its own next restart would
// fail to open the database it had just been reading. Administrators
// (rather than SYSTEM) is the owner set here because this runs under
// whatever token invoked it (an elevated Administrator during
// --install-service, not literally SYSTEM), and only Administrators can
// reliably take ownership without needing SeRestorePrivilege additionally
// enabled.
//
// `0o700` on MkdirAll is a no-op on Windows — the Go runtime does not
// translate Unix mode bits into an NTFS ACL — so without this the directory
// inherits whatever the parent (typically %ProgramData%) already grants,
// which can include ordinary local accounts. This directory holds the
// SQLite DB (registry credentials, encrypted secrets, session state) and,
// when TLS is self-managed, the private key — see docs/gotchas.md.
func SecureDataDirACL(path string) error {
	h, err := openDataDirHandle(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	adminSid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("resolve Administrators sid: %w", err)
	}
	systemSid, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve SYSTEM sid: %w", err)
	}

	inherit := uint32(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	grant := func(sid *windows.SID, tt windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  tt,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	entries := []windows.EXPLICIT_ACCESS{
		grant(systemSid, windows.TRUSTEE_IS_USER),
		grant(adminSid, windows.TRUSTEE_IS_GROUP),
	}
	if userSid, err := currentUserSID(); err == nil && !userSid.Equals(adminSid) {
		entries = append(entries, grant(userSid, windows.TRUSTEE_IS_USER))
	}

	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build ACL: %w", err)
	}

	// PROTECTED_DACL_SECURITY_INFORMATION strips inherited ACEs (e.g. a
	// permissive one inherited from %ProgramData% itself) so only the
	// explicit grants above apply. Owner is set alongside the DACL in the
	// same call, on the same handle opened above — a Windows object's owner
	// can always rewrite its own DACL (implicit WRITE_DAC) regardless of
	// what the DACL says, so leaving a pre-existing, untrusted owner in
	// place would let them silently regain access later no matter how
	// tightly the DACL itself is written.
	err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		adminSid, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("apply ACL to %s: %w", path, err)
	}
	return nil
}

// VerifyDataDirACL refuses a pre-existing data dir whose OWNER or DACL grants
// anything beyond SYSTEM, Administrators, CREATOR OWNER, or (best-effort) the
// current process's own user — the same set SecureDataDirACL grants — and
// refuses a reparse point outright (see openDataDirHandle). Used only at the
// explicit, interactive install step (internal/service's Windows installer)
// before SecureDataDirACL repairs a fresh/legitimate dir's security — a dir
// that already grants broader access, or is owned by an unexpected
// principal, is surfaced for manual inspection there instead of silently
// adopted and "fixed" as if it had always been fine.
//
// The owner check matters independently of the DACL check: a Windows
// object's owner can always rewrite its own DACL, so an attacker who
// pre-created the directory and left themselves as owner could otherwise
// pass a DACL-only check (by setting an innocuous-looking DACL) and then
// simply re-grant themselves access afterward. Load does not call this: an
// ordinary service start must not fail just because a directory looked odd —
// the interactive install is where a human is present to act on it.
func VerifyDataDirACL(path string) error {
	h, err := openDataDirHandle(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read existing security info: %w", err)
	}

	var userSid *windows.SID
	if s, err := currentUserSID(); err == nil {
		userSid = s
	}
	allowed := func(sid *windows.SID) bool {
		return sid.IsWellKnown(windows.WinLocalSystemSid) ||
			sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
			sid.IsWellKnown(windows.WinCreatorOwnerSid) ||
			(userSid != nil && sid.Equals(userSid))
	}

	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read existing owner: %w", err)
	}
	// The owner check does not accept CREATOR OWNER (that well-known SID
	// only ever appears in inherited ACEs, never as an actual object owner)
	// or the current user as a substitute for SYSTEM/Administrators — an
	// arbitrary local account owning this directory is exactly the planted-
	// directory scenario this check exists to catch, even if that account
	// happens to be the one running the installer right now.
	if !owner.IsWellKnown(windows.WinLocalSystemSid) && !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return fmt.Errorf("existing data dir is owned by unexpected SID %s (not SYSTEM/Administrators) — "+
			"inspect it manually before reinstalling; its owner could otherwise rewrite its ACL at any time "+
			"regardless of what that ACL currently says", owner.String())
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
		if !allowed(sid) {
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
