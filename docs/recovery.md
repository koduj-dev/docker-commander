# Recovery bundle

[← Manual index](README.md)

![Recovery bundle](images/settings_recovery.png)

A recovery bundle is one file with your Docker Commander configuration. Use it
to move to a new machine or rebuild after a loss. You do not need a database
restore.

Open **Settings → Recovery bundle** (`/settings?tab=recovery`) and use the
**Export** and **Import** sub-tabs. Admin only.

It has no volume contents or container filesystems. Use
[Backup jobs](backup-jobs.md) for those.

## What is in a bundle
| Included | Notes |
|----------|-------|
| Projects | name, slug, compose file name, target host (by name), "allow remote host paths" opt-in, last-deployed profiles, every file in the project folder |
| Project image digests | image and running digest per service, used for the compatibility check |
| Project secrets | names always; values only with **Include secrets** |
| Domain mappings | domain, service, port, TLS mode |
| Hosts | every non-local host: TLS CA/certificate, SSH host key, alert e-mail, disabled flag. TLS key only with **Include secrets** |
| Registries | name, address, username. Password only with **Include secrets** |
| Alert rules | with per-rule e-mail recipients and the webhook (by name) |
| Webhooks | only with **Include secrets**, because a webhook URL is a credential |
| Instance settings | disabled feature sections, localhost 2FA exemption, SMTP, LDAP. Passwords only with **Include secrets**. LDAP group mappings are never included |
| Inventory | names of the local host's networks and volumes, used to report what is missing on the target |

Not included: volume data, users and roles, API/MCP tokens, the audit log,
[policy rules](policy-rules.md), backup jobs, retention settings and project
revision history. For a full copy (database, encryption key, revision
snapshots) use the command-line backup in
[Deployment](deployment.md#backup--restore).

Limits:

- 500 projects.
- Per project: 100 files of 1 MiB each. Larger files are skipped. Symbolic links are not followed.
- 1 GiB of project files in total.
- Import rejects more than 5,000 hosts, registries, alert rules or webhooks.

## Export
On **Export**, set:

- **Include secrets**: adds host TLS keys, registry passwords, webhook URLs, SMTP/LDAP passwords and project secret values. Off by default.
- **Passphrase**: encrypts the bundle with AES-256-GCM. The key is derived with Argon2id.

A passphrase is required if the export contains any project. Project files such
as `.env` can hold secrets whatever **Include secrets** says. The server
refuses an unencrypted export with projects.

Without projects the passphrase is optional. Set one if you tick **Include
secrets**. Otherwise the file holds every credential in plain text.

**Export bundle** downloads `docker-commander-recovery-YYYY-MM-DD.dcbundle`. It
is a zip (`manifest.json` and `projects/<slug>/…`) in a versioned envelope
(version 1). The UI exports all projects. The API can export a subset with
`projectIds`.

## Import
Import has two steps: check, then import.

1. Choose the `.dcbundle` file. Enter the passphrase if the bundle is encrypted. Pick the **Target host** (*Local* by default).
2. Click **Check compatibility**. It writes nothing and reports:
   - bundle contents: export time, exporter, counts of projects, hosts, registries and alert rules, and whether secrets are included;
   - images not found on the target host, matched by repository and by digest if one was recorded;
   - volumes not found on the target;
   - projects whose host is neither in the bundle nor already configured;
   - a warning if the bundle has no secrets. You must re-enter registry passwords and host TLS keys.
3. Click **Import**. It is enabled only while the file, passphrase and target host match the last check. Change one and you must check again. A confirmation dialog follows.

**Also apply the bundle's instance settings** applies the disabled sections,
localhost 2FA exemption, SMTP and LDAP. It is off by default.

### What import does
- **Nothing is overwritten.** Hosts, registries, webhooks and alert rules match by name. Projects match by slug. Existing items are skipped with a warning. Importing the same bundle twice creates no duplicates.
- **Projects are created, not deployed.** Files are checked with Compose first. If the check fails, the project is skipped with a warning.
- **Hosts match by name** among the bundle's hosts and the configured ones. A project on the local host, or whose host is not found, goes to the **Target host**. A missing host gives a warning.
- **Secrets** are restored only if the bundle has the value. Otherwise you get a warning. Add the secret before you deploy.
- **Domain mappings** are restored. A domain already mapped, or an unsupported TLS mode, is skipped with a warning.
- **Alert rules** are recreated with their recipients and linked to webhooks by name.

The summary lists what was created, then every warning.

## Safety properties
- The `/api/recovery/*` API is admin-only and cannot be granted per section.
- A wrong passphrase and a missing one give the same error, "passphrase required".
- A recovery bundle cannot be restored as a full backup, and the reverse.
- Import rejects unknown manifest fields, unsupported versions, paths that leave the project folder and a disabled target host.

## Audit
The [audit log](audit.md) records `recovery.export` (number of projects, whether
secrets were included), `recovery.inspect` and `recovery.import` (projects
created, host, registry and rule counts).
