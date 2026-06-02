# Hosts

[← Manual index](README.md)

Manage the Docker daemons Docker Commander talks to. The **local** host exists
out of the box; add remote ones and switch between them with the sidebar
switcher. Every view, stream and the alert engine bind to the selected host.

## Adding a host
- **TCP** — address `tcp://host:2376`, with optional CA / client cert / key
  (PEM) for TLS.
- **SSH** — address `user@host[:port]`. Authentication uses the **server's own
  SSH agent / `~/.ssh` keys** (no key material is stored here). The Docker API
  is tunnelled to the remote daemon's socket over SSH.

You can set a per-host **alert email** (overrides the global SMTP recipient for
alerts from that host) at creation or inline on the host card.

## SSH host-key verification
On first contact with an SSH host the daemon's host key is checked against
`~/.ssh/known_hosts`, then against a key you've trusted here:

- **Unknown** → **Test** shows the SHA-256 fingerprint and a **Trust this host**
  button (trust-on-first-use). Verify the fingerprint out-of-band before trusting.
- **Changed** → refused as a possible **man-in-the-middle**; re-trust only if you
  changed the host deliberately.

## Test
**Test** probes a host (bounded so an unreachable host fails fast) and reports
the Docker version + running count, or the connection / host-key problem.

## Tips
- Remote alerting/metrics “just work” — the engine watches every configured
  host, and alerts carry which host they came from.
- Don't expose remote/SSH hosts until host-key verification is satisfied.
