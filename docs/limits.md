# Limits

[← Manual index](README.md)

Every cap you can actually hit, with the reason where it isn't obvious. The
caps stop one request from using up the memory, disk or responsiveness of the
whole installation. The app tells you when you hit one. This page lets you know
in advance, and whether the number can be changed.

## Signing in

| Limit | Value | Notes |
| --- | --- | --- |
| Session lifetime | **12 hours** | Set with `-session-ttl` (flag only, no `DC_` variable). Then you sign in again. |
| Failed sign-ins | **5 per 15 minutes**, per address | After that the password form is refused until the window ends, even with the right password. Behind a reverse proxy without `DC_TRUSTED_PROXIES`, all clients share one address. See [Deployment](deployment.md). |
| 2FA code attempts | **5 per 15 minutes**, per address and per account | The account budget keeps counting when the address changes. |
| Passkey sign-in attempts | **30 per 5 minutes**, per address | A separate budget, because everyone sees the button and closing the browser prompt is common. It must not lock the password form. |
| Authenticators and passkeys | **10 per account** | TOTP apps and passkeys count together. |
| Password length | **at least 10 characters** | Everywhere, including the offline `--reset-password`. |

## Uploads and files

| Limit | Value | Notes |
| --- | --- | --- |
| Ordinary request body | **1 MiB** | Everything except the streaming routes below. |
| Seed volumes saved before a remote restore | **2 GiB** in all | Kept on the Docker Commander machine until the restore ends. Over it, the restore is refused before anything changes. |
| File upload into a container or volume, or an image build context | **2 GiB** | Written to a temporary file that is unlinked at once, so it costs no memory. |
| Archive uploaded for **Extract** | **4 GiB** compressed | |
| Uploaded archive, after decompression | **512 MiB** | Protects against a zip or gzip bomb. |
| Idle time during a streaming upload | **2 minutes** | Counts silence, not total time. A slow large upload is fine; a stalled one is dropped. |
| Any request's body, start to finish | **60 seconds** | Streaming routes use the idle limit above instead. |

## Projects and stacks

| Limit | Value | Notes |
| --- | --- | --- |
| Files in a project | **100** | |
| Size of one project file | **1 MiB** | The editor refuses to save a larger file. |
| Imported project `.zip` | **32 MiB** | Entries over the file size or file count limit are skipped. |
| Compose file read or displayed | **1 MiB** | |
| `docker compose` command | **10 minutes** | A longer deploy is abandoned. |

## History retention

Set under [Settings → Data retention](settings.md#data-retention).

| Limit | Value | Notes |
| --- | --- | --- |
| Alert events and deliveries | 90 days by default | 1 to 36 500 days, or forever. Deliveries are never kept longer than their events. |
| Audit log | 365 days by default | 30 to 36 500 days, or forever. |
| Project revisions | newest 50 per project by default | At least 3, or unlimited. |
| Purge schedule | first run about 2 minutes after start, then every 24 hours | Deletes in batches of 2 000 rows. |
| Backup job run history | newest 200 runs per job | Not adjustable. See [Backup jobs](backup-jobs.md). |

## Containers and recovery

| Limit | Value | Notes |
| --- | --- | --- |
| Containers in one bulk action | **200** per request | |
| Project files in a recovery bundle | **1 GiB** and **20 000** files in total | An export over 1 GiB fails with `413`; an import over either limit is refused before anything is written. See [Recovery bundle](recovery.md). |

## Images

| Limit | Value | Notes |
| --- | --- | --- |
| Vulnerability scan | **6 minutes**, 2 at a time | Trivy does the scan. Two at a time so scans cannot starve the daemon. |
| Vulnerabilities reported per scan | **5000** | |
| Registry response while listing tags | **2 MiB** | |
| CVEs ignored in one request | **500** | |

## Logs, events and alerts

| Limit | Value | Notes |
| --- | --- | --- |
| Lines held by the Logs page | **3000** | Oldest lines are dropped. This is browser memory, not server. |
| Events held by the Events page | **2000** | Same. The feed is live only and shows nothing from before you opened it. |
| Alert feed | **50** per page | The feed itself has no cap. The API returns at most 500 per request. |
| Audit entries fetched by the page | **1000** | The server returns at most that many per request. |
| Recurring maintenance occurrence | **24 hours** | A longer one is refused. See [Alerts](alerts.md). |

## MCP (AI-tool access)

| Limit | Value | Notes |
| --- | --- | --- |
| Token lifetime | **30 days** default, **365** maximum | Both set by an admin in [Settings](settings.md). |
| OAuth access token | **15 minutes** | |
| Control actions | **30 per minute** | Keeps a runaway agent in check. See [MCP](mcp.md). |
| `container_logs` | **1000** lines, **64 KiB** | |
| `search_logs` | tail of **2000** lines, **200** matches, pattern up to **200** characters | |
| `container_changes` | **500** entries | |
| `list_alerts`, `recent_audit` | **50** by default, **200** at most | |
| `recent_events` | **360** minutes back, **100** by default, **500** at most | |
| `scan_image` | **100** findings | |

## Sessions and ceremonies

| Limit | Value | Notes |
| --- | --- | --- |
| WebAuthn ceremony | **2 minutes** | Time between pressing the button and answering the browser prompt. |
| Half-finished passkey sign-ins held at once | **512** | Server-wide. Anyone can start one without signing in, so it has its own cap. A flood of them cannot stop anyone pairing a passkey or finishing a second factor. |
