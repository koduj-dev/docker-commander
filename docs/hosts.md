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

> The remote server only needs **Docker installed and reachable** — it does
> **not** need Docker Commander. One Docker Commander instance talks to many
> remote Docker daemons; it never talks to another Docker Commander.

## Connecting a remote host — worked example

### Option 1 · SSH (recommended)
Nothing is exposed to the network and no secrets are stored here — Docker
Commander tunnels the Docker API over SSH using the **OS user's own SSH keys**
(the user that runs the `dockercmd` process).

On the **Docker Commander server**, as the user that runs `dockercmd`:

```bash
# 1. Have an SSH key (skip if you already do) and install it on the remote host:
ssh-keygen -t ed25519
ssh-copy-id deploy@10.0.0.42

# 2. Sanity check — this must succeed WITHOUT a password prompt:
ssh deploy@10.0.0.42 docker info
```

On the **remote host**, the SSH user must be allowed to reach the Docker socket:

```bash
sudo usermod -aG docker deploy    # then reconnect so the group takes effect
```

Then in the UI: **Hosts → Add host → SSH**, address `deploy@10.0.0.42`
(or `deploy@host:2222` for a custom port) → **Test** → **Trust this host**
after verifying the SSH host-key fingerprint (see below).

> Key auth uses the server's SSH **agent** or `~/.ssh` keys. For a passphrase-
> protected key, make sure an agent is available to the `dockercmd` process
> (e.g. `systemctl` service with `SSH_AUTH_SOCK`, or a passphrase-less key
> dedicated to this).

### Option 2 · TCP + TLS
Use this when SSH isn't an option. **Never expose the Docker socket over TCP
without TLS** — that grants root-equivalent access to anyone who can reach the
port.

1. On the remote host, enable a TLS-protected TCP listener on `:2376` and
   generate a CA + server + client certificates — follow Docker's official
   guide: <https://docs.docker.com/engine/security/protect-access/>.
2. In the UI: **Hosts → Add host → TCP (+TLS)**, address
   `tcp://10.0.0.42:2376`, and paste the **CA cert**, **client cert** and
   **client key** (PEM) into the three fields.

Quick sanity check from the Docker Commander server:

```bash
docker --tlsverify --tlscacert=ca.pem --tlscert=cert.pem --tlskey=key.pem \
  -H tcp://10.0.0.42:2376 info
```

## Switching the active host
With more than one host configured, a **Viewing host** switcher appears at the
top of the sidebar. Pick a host and the whole app — dashboard, containers,
images, logs, stats, exec — rebinds to it; the rest stays focused on that one
host (so nothing is "mixed" across servers). The currently active host is also
shown as a badge in every page header. The selection is remembered in your
browser. (The switcher is hidden when only the local host exists.)

## Host detail
The **ℹ️** button on a host card opens its **hardware / OS / engine** summary:
CPUs, memory, architecture, OS and kernel, Docker version, storage & logging
drivers, cgroup, and the current container/image counts.

> **Docker Desktop:** the engine runs inside a Linux VM, so these values
> describe that VM, not the Windows/macOS host — the Docker API can't see the
> underlying OS. The **kernel** is the best hint (e.g. `…-WSL2` ⇒ Windows/WSL2).

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
