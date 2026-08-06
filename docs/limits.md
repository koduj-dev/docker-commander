# Limits

[← Manual index](README.md)

Every cap the app enforces that you can actually hit, with the reason where the
number is not obvious. They exist so one request cannot cost the whole
installation its memory, its disk or its responsiveness.

If you hit one, the app says so — the point of this page is that you can find out
*before* that, and know whether the number is adjustable.

## Signing in

| Limit | Value | Notes |
| --- | --- | --- |
| Session lifetime | **12 hours** | `-session-ttl`, flag-only (no `DC_` equivalent). After it you are signed out and sign in again. |
| Failed sign-ins | **5 per 15 minutes**, per address | Then the password form is refused for the rest of the window, right password or not. Behind a reverse proxy without `DC_TRUSTED_PROXIES`, every client shares one address — see [Deployment](deployment.md). |
| Passkey sign-in attempts | **30 per 5 minutes**, per address | A separate budget on purpose: the sign-in button is offered to everyone, and dismissing the browser prompt is the commonest outcome, so it must not close the password form. |
| Authenticators and passkeys | **10 per account** | TOTP apps and passkeys share the pool. |
| Password length | **at least 10 characters** | The same floor everywhere, including the offline `--reset-password`. |

## Uploads and files

| Limit | Value | Notes |
| --- | --- | --- |
| Ordinary request body | **1 MiB** | Everything that is not one of the streaming routes below. |
| File upload into a container or volume | **2 GiB** | Buffered to a temporary file, unlinked immediately, so it never costs memory. |
| Uploaded archive, after decompression | **512 MiB** | The guard against a zip/gzip bomb: a small archive that expands without bound. |
| Idle time during a streaming upload | **2 minutes** | Measures *silence*, not duration — a slow multi-gigabyte upload is fine, a stalled one is dropped. |
| Any request's body, start to finish | **60 seconds** | Except the streaming routes, which use the idle limit above. |

## Projects and stacks

| Limit | Value | Notes |
| --- | --- | --- |
| Files in a project | **100** | |
| Size of one project file | **1 MiB** | The editor refuses a larger write. |
| Imported project `.zip` | **32 MiB** | Entries over the per-file limit, or past the file count, are skipped. |
| Compose file read or displayed | **1 MiB** | |
| `docker compose` command | **10 minutes** | A deploy that takes longer is given up on. |

## Images

| Limit | Value | Notes |
| --- | --- | --- |
| Vulnerability scan | **6 minutes**, 2 at a time | Trivy is the one doing the work; concurrency is capped so a scan cannot starve the daemon. |
| Vulnerabilities reported per scan | **5000** | |
| Registry response while listing tags | **2 MiB** | |

## Logs, events and alerts

| Limit | Value | Notes |
| --- | --- | --- |
| Lines held by the Logs page | **3000** | Oldest are dropped; this is the browser's memory, not the server's. |
| Events held by the Events page | **2000** | Same, and the feed is live-only — it shows nothing from before you opened it. |
| Alert feed | **500** entries | |
| Audit entries fetched by the page | **1000** | The server will not return more than that in one request. |

## MCP (AI-tool access)

| Limit | Value | Notes |
| --- | --- | --- |
| Token lifetime | **30 days** default, **365** ceiling | Both admin-settable; see [Settings](settings.md). |
| OAuth access token | **15 minutes** | |
| Control actions | **30 per minute** | So a runaway agent is bounded. See [MCP](mcp.md). |

## Sessions and ceremonies

| Limit | Value | Notes |
| --- | --- | --- |
| Sessions listed on your profile | **256** | |
| WebAuthn ceremony | **2 minutes** | The window between pressing the button and answering the browser prompt. |
| Half-finished passkey sign-ins held at once | **512** | Server-wide. Reachable without signing in, so it is bounded separately from everything else — a flood of them cannot stop anyone pairing a passkey or completing a second factor. |
