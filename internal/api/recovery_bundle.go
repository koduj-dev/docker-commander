package api

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Portable recovery bundle: one file capturing everything Docker Commander
// itself knows — project/stack files, host/registry/alert-rule definitions,
// image digests, and (optionally) the instance's own settings — so an
// operator can move or rebuild an installation without a database restore.
// It deliberately carries no volume DATA (see NEXT.md): restoring a bundle
// puts configuration back, not the bytes a container wrote at runtime.
//
// The bundle is a zip (manifest.json + one directory per project) wrapped in
// the same passphrase envelope internal/backup uses (see internal/passphrase):
// an operator who includes secrets can also choose to encrypt the whole
// bundle, since — exactly as internal/backup's package doc explains for a
// full-DB backup — a bundle carrying decrypted secrets is, in practice,
// equivalent to the plaintext of every credential it names.
const (
	recoveryBundleVersion = 1

	// maxBundleProjects caps how many projects a single export/import walks,
	// so a pathological request can't enumerate or extract an unbounded number
	// of project directories.
	maxBundleProjects = 500

	// maxRecoveryUploadBytes caps the raw uploaded bundle file (compressed,
	// and passphrase-sealed if encrypted) — the same shape as maxImportBytes
	// for a single project's zip, sized up for a multi-project bundle.
	maxRecoveryUploadBytes = 128 << 20
)

// recoveryMagic identifies a Docker Commander recovery bundle to
// internal/passphrase, distinct from internal/backup's own magic so the two
// archive kinds can never be fed to each other's reader by mistake.
var recoveryMagic = []byte("DCREC1\n")

// recoveryHost is one exported host definition. TLSKey is blank unless the
// export included secrets — CreateHost re-encrypts whatever is here under the
// importing instance's own cipher, so this always carries plaintext, never
// the source instance's ciphertext (which is only ever meaningful next to
// that instance's own encryption key).
type recoveryHost struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Address    string `json:"address"`
	TLSCA      string `json:"tlsCa,omitempty"`
	TLSCert    string `json:"tlsCert,omitempty"`
	TLSKey     string `json:"tlsKey,omitempty"`
	HostKey    string `json:"hostKey,omitempty"`
	AlertEmail string `json:"alertEmail,omitempty"`
	Disabled   bool   `json:"disabled"`
}

// recoveryRegistry is one exported registry credential. Password is blank
// unless the export included secrets.
type recoveryRegistry struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// recoveryWebhook mirrors store.Webhook. Unlike alert_portability.go's rule
// export (which never carries a webhook's URL, by deliberate SSRF/exfiltration
// policy — see its own doc comment), a recovery bundle DOES carry it, but only
// when the operator explicitly opted into includeSecrets: a webhook URL is, in
// the same sense as a registry password, a credential this instance holds, and
// includeSecrets is the one place in this app an operator explicitly consents
// to exporting that class of value.
type recoveryWebhook struct {
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyTemplate string            `json:"bodyTemplate,omitempty"`
}

// recoverySettings is the instance-wide configuration a bundle can carry.
// Never applied on import unless the caller explicitly asks (see
// handleImportRecoveryBundle) — importing a bundle onto a live instance must
// not silently repoint its mail relay or its feature flags.
type recoverySettings struct {
	DisabledSections []string          `json:"disabledSections,omitempty"`
	LocalhostNo2FA   bool              `json:"localhostNo2FA"`
	SMTP             *recoverySMTP     `json:"smtp,omitempty"`
	LDAP             *recoveryLDAPConf `json:"ldap,omitempty"`
}

// recoverySMTP mirrors store.SMTPConfig; Password is blank unless secrets
// were included.
type recoverySMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`
	TLS      bool   `json:"tls"`
}

// recoveryLDAPConf mirrors store.LDAPConfig, minus GroupMappings: a mapping
// names role/section ids that only mean something on the instance that
// defined them, so it is never round-tripped through a bundle. BindPassword
// is blank unless secrets were included.
type recoveryLDAPConf struct {
	Enabled      bool   `json:"enabled"`
	URL          string `json:"url"`
	StartTLS     bool   `json:"startTls"`
	BindDN       string `json:"bindDn"`
	BindPassword string `json:"bindPassword,omitempty"`
	UserBaseDN   string `json:"userBaseDn"`
	UserFilter   string `json:"userFilter"`
	AdminGroupDN string `json:"adminGroupDn,omitempty"`
}

// recoveryProjectImage records one service's image reference and the digest
// actually running at export time — the same pair a deployment revision
// records, letting a restored project's compatibility check flag an image
// that no longer exists locally without redeploying first.
type recoveryProjectImage struct {
	Service string `json:"service"`
	Image   string `json:"image"`
	Digest  string `json:"digest,omitempty"`
}

// recoveryProjectMeta is one project's metadata; its files live in the
// bundle's zip under "projects/<Slug>/...".
type recoveryProjectMeta struct {
	Slug                 string                 `json:"slug"`
	Name                 string                 `json:"name"`
	ComposeFile          string                 `json:"composeFile"`
	HostName             string                 `json:"hostName"` // "" = local; resolved by NAME, not id
	AllowRemoteHostPaths bool                   `json:"allowRemoteHostPaths"`
	LastDeployedProfiles []string               `json:"lastDeployedProfiles,omitempty"`
	Images               []recoveryProjectImage `json:"images,omitempty"`
}

// recoveryManifest is the bundle's manifest.json.
type recoveryManifest struct {
	Version          int                   `json:"version"`
	ExportedAt       string                `json:"exportedAt"`
	ExportedBy       string                `json:"exportedBy"`
	IncludesSecrets  bool                  `json:"includesSecrets"`
	Hosts            []recoveryHost        `json:"hosts"`
	Registries       []recoveryRegistry    `json:"registries"`
	AlertRules       []portableRule        `json:"alertRules"`
	Webhooks         []recoveryWebhook     `json:"webhooks,omitempty"`
	Settings         recoverySettings      `json:"settings"`
	Projects         []recoveryProjectMeta `json:"projects"`
	NetworkInventory []string              `json:"networkInventory,omitempty"`
	VolumeInventory  []string              `json:"volumeInventory,omitempty"`
}

// writeDirToZip walks root and adds each regular file under it into zw, named
// with prefix (e.g. "projects/shop/"). It's zipDir's walk logic (same
// symlink-skip, same "abort cleanly on a read error" behavior), just able to
// write into an already-open zip.Writer shared by an entire bundle rather
// than producing its own standalone archive. Returns the number of files
// written.
func writeDirToZip(zw *zip.Writer, root, prefix string) (int, error) {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		if count >= maxProjectFiles {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if len(data) > maxProjectFileBytes {
			return nil // skip, don't abort the whole export over one oversized file
		}
		fw, werr := zw.Create(prefix + filepath.ToSlash(rel))
		if werr != nil {
			return werr
		}
		if _, werr := fw.Write(data); werr != nil {
			return werr
		}
		count++
		return nil
	})
	if err != nil {
		return count, err
	}
	return count, nil
}

// readAllLimit reads r fully, refusing anything past limit rather than
// silently truncating a bundle mid-file.
func readAllLimit(r io.Reader, limit int64) ([]byte, error) {
	lr := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("upload is too large")
	}
	return data, nil
}
