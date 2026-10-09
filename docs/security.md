# Security model

[← Manual index](README.md)

How Docker Commander protects access to the Docker daemons it controls. To
report a vulnerability, see [SECURITY.md](../SECURITY.md) (privately, please).

- Local-by-default (binds to loopback). Behind a server, terminate TLS at a reverse proxy.
- **2FA is enforced everywhere** unless an admin enables the *localhost exemption* (Settings), which applies only to a **direct** loopback connection. A request from a proxy listed in `DC_TRUSTED_PROXIES`, or one carrying any forwarding header (`Forwarded`, `X-Forwarded-*`, `X-Real-Ip`, `Via`), never qualifies. A local proxy that adds none of those headers makes every client look local, so keep the exemption off behind one. Failed 2FA attempts are rate limited and audited, so the second factor can't be brute-forced by someone who already has the password.
- **Passkeys are bound to this site's address.** A page that impersonates this one cannot use an assertion it captures, and a signature counter that goes backwards — the sign of a cloned key — is refused and audited.
- **Sessions are revocable.** A session is a recorded row, not just a signed token: signing out, revoking one from your profile, or changing your password takes effect on the **next request** rather than whenever the token would have expired.
- **SSH hosts** verify the daemon host key (known_hosts / trust-on-first-use); a changed key is refused as a possible MITM.
- Signing key and at-rest encryption key are generated on first run and stored in the data dir; stored secrets are never returned by the API.
- The **MCP server is off by default** (`DC_MCP_ENABLED`); when on, it's bearer/OAuth-authenticated, reuses the app's RBAC (with per-token **read-only** / section scope), and exposes only reads + *safe* control — no exec, image export, file reads or prune/remove. Control calls are additionally **rate limited per user** to bound the damage a runaway or stolen token can do. See [MCP](mcp.md).
