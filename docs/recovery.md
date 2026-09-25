# Recovery bundle

[← Manual index](README.md)

![Recovery bundle](images/settings_recovery.png)

The **recovery bundle** is one portable file holding everything Docker Commander
itself knows about your setup — projects and their files, hosts, registries,
alert rules, domain mappings and more — so you can move to a new machine or
rebuild after a loss **without restoring a database**.

It is found under **Settings → Recovery bundle** (`/settings?tab=recovery`), with
**Export** and **Import** as sub-tabs. Admin only. The page used to be a separate
menu item.

**It carries configuration, not data.** No volume contents, no container
filesystems. Use [Backup jobs](backup-jobs.md) (or your own tooling) for the
bytes your containers wrote.

## What is in a bundle
| Included | Notes |
|----------|-------|
| **Projects** | name, slug, the compose file name, target host (by name), the "allow remote host paths" opt-in, last-deployed profiles, and **every file in the project folder** |
| **Project image digests** | the image and running digest of each service at export time, used for the compatibility check |
| **Project secrets** | names always; **values only with "Include secrets"** |
| **Domain mappings** | domain, service, port, TLS mode — always exported in full |
| **Hosts** | every non-local host (the built-in local host is not exported); its TLS CA/certificate, SSH host key, alert e-mail and disabled flag. The **TLS key only with "Include secrets"** |
| **Registries** | name, address, username; the **password only with "Include secrets"** |
| **Alert rules** | including each rule's own e-mail recipients and the webhook it points at (by name) |
| **Webhooks** | **only with "Include secrets"** — a webhook URL is a credential |
| **Instance settings** | disabled feature sections, the localhost 2FA exemption, SMTP and LDAP (SMTP/LDAP **passwords only with "Include secrets"**; LDAP group mappings are never included) |
| **Inventory** | the names of the local host's networks and volumes, used to tell you what is missing on the target |

**Not included:** volume data, users and roles, API/MCP tokens, the audit log,
[policy rules](policy-rules.md), backup jobs, retention settings, and project
**revision history** (deploy history) — the bundle holds each project's current
files only. For a whole-installation copy (database, encryption key, revision
snapshots) use the command-line backup described in
[Deployment](deployment.md#backup--restore).

Size limits: up to **500 projects**; per project, up to **100 files** of **1 MiB**
each (larger files are skipped, and symbolic links are never followed); **1 GiB**
of project files in total. Import also rejects a bundle with more than 5,000
hosts, registries, alert rules or webhooks.

## Export
On **Export**:

- **Include secrets** — adds host TLS keys, registry passwords, webhook URLs,
  SMTP/LDAP passwords and project secret values. Unticked (the default), those
  values are left out (names and other non-secret fields are still exported).
- **Passphrase** — encrypts the whole bundle with AES-256-GCM, key derived from
  the passphrase with Argon2id. **A passphrase is required whenever the export
  contains any project**, because project files (a `.env`, keys, registry
  configs) may hold secrets no matter what *Include secrets* says. The server
  refuses an unencrypted export that carries projects. Without projects the
  passphrase is optional, but if you tick *Include secrets* you should always
  set one: such a bundle is the plaintext of every credential it names.

**Export bundle** downloads `docker-commander-recovery-YYYY-MM-DD.dcbundle`. The
passphrase is sent in a request header, never in the URL. The file is a zip
(`manifest.json` plus `projects/<slug>/…`), wrapped in an envelope that
identifies it as a recovery bundle; the format is versioned (currently 1).

The UI always exports every project; the API can also export a chosen subset
(`projectIds`).

## Import
Importing is a two-step flow — check first, then import.

1. Choose the `.dcbundle` file, enter the passphrase (if it is encrypted) and
   pick the **Target host** (*Local* by default).
2. **Check compatibility** reads the bundle and reports, writing nothing:
   - what the bundle contains (export time, exporter, and counts of projects,
     hosts, registries and alert rules) and whether secrets are included;
   - **images not found** on the target host — matched by repository and, when
     a digest was recorded at export, that digest;
   - **volumes not found** on the target, from the bundle's inventory;
   - projects that reference a **host** that is neither in the bundle nor
     already configured;
   - a warning when the bundle excludes secrets: registry passwords and host TLS
     keys will need to be re-entered.
3. **Import** is enabled only when the file, passphrase and target host are
   exactly what was just checked — change any of them and you must check again.
   A confirmation dialog follows.

Finally, an optional checkbox, **Also apply the bundle's instance settings**,
applies the disabled sections, localhost 2FA exemption, SMTP and LDAP settings.
It is **off by default**: importing onto a live instance must not silently
repoint its mail relay or its feature flags.

### What import does
- **Nothing is overwritten.** Hosts, registries, webhooks and alert rules are
  matched by **name**, projects by **slug**; anything that already exists is
  **skipped** and reported as a warning (an existing webhook is simply reused
  by rules that reference it). Re-importing the same bundle doesn't duplicate
  anything.
- **Projects are created but not deployed.** Each project's files are unpacked
  into a staging folder and checked with Compose; only if that passes is the
  project created and the folder moved into place. A project whose files no
  longer validate is skipped with a warning rather than half-restored.
- **Hosts mapping.** A project's host is looked up **by name** among the hosts
  in the bundle and those already configured, so it re-attaches correctly even
  though host IDs differ between instances. A project that was on the local host,
  or whose host can't be found (with a warning), is attached to the **target
  host** you chose.
- **Secrets.** A project secret is restored only if the bundle carries its
  value. Otherwise it is *not* recreated with an empty value — you get a warning
  to add it before deploying, so a `${NAME}` never silently resolves to nothing.
- **Domain mappings** are restored; a domain already mapped on this instance is
  skipped with a warning, and an unsupported TLS mode is skipped too.
- **Alert rules** are recreated with their per-rule recipients and linked to
  webhooks by name.

When done, a summary lists how many projects, hosts, registries, webhooks and
alert rules were created, followed by every warning (what was skipped and why).

## Safety properties
- **Admin only.** The whole `/api/recovery/*` surface is admin-only; a bundle can
  aggregate host keys, registry passwords and every project's files, so it can't
  be granted per-section.
- **No accidental exposure of secrets.** Whether a bundle includes secrets is
  decided from the export request itself, never from a value the client echoes
  back. The *inspect* response reports counts and flags, never secret values.
- **Passphrase and format.** Wrong and missing passphrases are
  indistinguishable ("passphrase required"), and a recovery bundle can't be fed
  to the full-backup restore or vice versa — the two formats have different
  markers.
- **A hostile bundle can't escape.** Import rejects unknown manifest fields and
  unsupported versions, refuses paths that climb out of the project folder,
  bounds the total number and size of files (and of the manifest itself) against
  what an export can produce *before* writing any project file, and refuses a
  disabled host as the target.
- **Disk, not memory.** The export is assembled in a temporary file that is
  removed afterwards.

## Audit
Recorded in the [audit log](audit.md): `recovery.export` (number of projects,
and whether secrets were included), `recovery.inspect` and `recovery.import`
(projects created, plus host, registry and rule counts).
