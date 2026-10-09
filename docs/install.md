# Installing

[← Manual index](README.md)

Every way to get Docker Commander running, how to check that what you
downloaded is what was released, and how to run it as a service. Once it runs,
open <http://127.0.0.1:8470>, create the admin account and pair an
authenticator: see [Getting started](getting-started.md).

## Release binary

Grab the binary for your OS/arch from the [Releases](https://github.com/koduj-dev/docker-commander/releases) page, then:

```bash
chmod +x dockercmd-linux-amd64
./dockercmd-linux-amd64           # serves on http://127.0.0.1:8470
```

On Windows, run `dockercmd-windows-amd64.exe` from a terminal.

Debian/Ubuntu & Fedora users can grab a `.deb` / `.rpm` from the same page — it
sets up the systemd service. Debian and Ubuntu can also use the
[signed APT repository](deployment.md#debian--ubuntu--fedora-packages-deb--rpm) and get updates with `apt upgrade`.

## Homebrew (macOS & Linux)

```bash
brew install koduj-dev/tap/dockercmd
dockercmd --version
```

Installs the signed release binary for your OS/arch from the
[koduj-dev/homebrew-tap](https://github.com/koduj-dev/homebrew-tap).

## From source

Requires **Go ≥ 1.26**, **Node.js ≥ 18** (to build the UI) and a running Docker
daemon. See [Building from source](#building-from-source) for per-OS details.

```bash
git clone https://github.com/koduj-dev/docker-commander.git
cd docker-commander
make build      # builds the UI, then the binary with the UI embedded
./dockercmd     # http://127.0.0.1:8470
```

## Docker

```bash
docker run -d --name dockercmd \
  -p 127.0.0.1:8470:8470 \
  --group-add "$(stat -c '%g' /var/run/docker.sock 2>/dev/null || stat -f '%g' /var/run/docker.sock)" \
  --read-only --tmpfs /tmp \
  --security-opt no-new-privileges \
  --cap-drop ALL \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v dockercmd-data:/data \
  ghcr.io/koduj-dev/docker-commander:latest
```

Multi-arch (amd64/arm64), distroless, runs as a **non-root** user with a
**read-only** root filesystem and **no added capabilities**. Notes:

- **⚠️ Mounting the Docker socket grants host-root-equivalent access** — whoever
  reaches the UI (or escapes the app) controls the daemon, i.e. the host. Keep
  the UI on **localhost** (as above) or behind **HTTPS + strong auth**; never
  expose it unauthenticated. The `--group-add` line gives the non-root user the
  **owning GID of the Docker socket** — read from `/var/run/docker.sock` itself
  (`stat`, with a BSD fallback), so it works even without a `docker` group. On
  **rootless / Docker Desktop**, where the socket is owned by your user, drop the
  `--group-add` line.
- Data lives in the named volume `dockercmd-data` (a fresh one inherits the
  right ownership). A **bind mount** (`-v /srv/dc:/data`) must be writable by uid
  **65532** first: `sudo chown 65532:65532 /srv/dc`.
- In production, pin an **immutable digest** (`...@sha256:…`) instead of
  `:latest`, and verify the image (see [Verifying a download](#verifying-a-download)).

## `go install`

```bash
go install github.com/koduj-dev/docker-commander/cmd/dockercmd@latest
```

Installs to `$(go env GOPATH)/bin/dockercmd`. (Built this way the version reports
`dev`; the release binaries and the image carry the real version.)

## Verifying a download

Every release ships a `SHA256SUMS` plus a keyless **cosign** signature bundle
(`SHA256SUMS.bundle`) covering the binaries **and** the SPDX **SBOM**, plus
per-binary build **provenance**:

```bash
sha256sum -c SHA256SUMS --ignore-missing        # checksums (binaries + SBOM)

cosign verify-blob --bundle SHA256SUMS.bundle \
  --certificate-identity-regexp '^https://github\.com/koduj-dev/docker-commander/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS

gh attestation verify dockercmd-linux-amd64 --repo koduj-dev/docker-commander
```

Verifying the bundle needs **cosign v3+**. Releases up to and including v1.5.0
were signed with cosign v2 and ship a `SHA256SUMS.sig` / `SHA256SUMS.pem` pair
instead — verify those with
`cosign verify-blob --certificate SHA256SUMS.pem --signature SHA256SUMS.sig …`.

The container image is signed and carries SLSA provenance + an SBOM as well:

```bash
# verify the exact digest you'll run (copy it from the release notes or
# `docker buildx imagetools inspect ghcr.io/koduj-dev/docker-commander:latest`):
IMAGE=ghcr.io/koduj-dev/docker-commander@sha256:<digest>
cosign verify "$IMAGE" \
  --certificate-identity-regexp '^https://github\.com/koduj-dev/docker-commander/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify "oci://$IMAGE" --repo koduj-dev/docker-commander
```

## Run as a service

The server keeps monitoring, alerting and metric history running 24/7 whether or
not a browser is connected — so run it as a background service. On
Linux/macOS/Windows the binary installs itself:

```bash
sudo ./dockercmd --install-service     # Linux — systemd (needs root)
./dockercmd --install-service          # macOS — launchd LaunchAgent (your user, not sudo)
dockercmd.exe --install-service        # Windows — native SCM service (elevated PowerShell/cmd)
```

It creates a dedicated user (or, on Windows, registers with the Service Control
Manager), writes the (hardened) service definition and starts it. To read
exactly what gets installed, use the equivalent scripts in [`deploy/`](../deploy/):

```bash
sudo ./deploy/install-linux.sh ./dockercmd                  # Linux  — systemd
./deploy/install-macos.sh ./dockercmd                       # macOS  — launchd
.\deploy\install-windows.ps1 -BinPath .\dockercmd.exe       # Windows — Scheduled Task (elevated PowerShell)
```

The Windows Scheduled Task script remains as a dependency-free alternative to
the native SCM service above.

See **[Deployment](deployment.md)** for what each installer does, the manual
systemd steps, HTTPS, logging and the config reference.

It binds to loopback by default — put it behind a TLS reverse proxy (nginx,
Caddy) to expose it, and keep the **localhost 2FA exemption off** on servers.

## Building from source

The UI is built with Node and embedded into the Go binary; the result is a
single CGO-free static executable.

```bash
make build          # current platform → ./dockercmd
make release        # cross-compile all platforms → dist-bin/ (+ SHA256SUMS)
make test vet       # tests + static checks
VERSION=v1.0.0 make release   # stamp the version into the binary
```

Per OS (building **from source** — end users can just download a release):

| Host OS | Notes |
|---------|-------|
| **Linux**   | `make build`. Default target for releases. |
| **macOS**   | `make build` (Intel or Apple Silicon). Cross-compiles to both `darwin/amd64` and `darwin/arm64`. |
| **Windows** | Use WSL or Git Bash for `make`, or run the two steps manually: `cd web && npm ci && npm run build` then `go build -o dockercmd.exe ./cmd/dockercmd`. Releases ship `windows/amd64` + `windows/arm64` `.exe`. |

`make release` builds `linux/{amd64,arm64}`, `darwin/{amd64,arm64}` and
`windows/{amd64,arm64}` from any host (no C toolchain needed).
