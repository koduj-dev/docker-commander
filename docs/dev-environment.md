# Dev environment notes

[← Manual index](README.md)

How the maintainer's machine is set up, and the throwaway services the
Docker-backed tests expect. Nothing here is required to *use* Docker Commander —
it is here so a development session doesn't start by rediscovering it.

For how the test suite is organised, see [Testing](testing.md). For behaviours of
the app itself that are easy to get wrong, see [Gotchas](gotchas.md).

## This machine

- **Go lives at `~/.local/go`** and is not on the default PATH:
  `export PATH="$HOME/.local/go/bin:$PATH"` (with `GOTOOLCHAIN=local`).
- **Don't use `pkill`** in this sandbox — it exits 144 and aborts the rest of the
  command. Stop background servers explicitly:
  `lsof -ti tcp:PORT | xargs kill`.
- **Build:** `make build` (UI, then binary). The committed `web/dist` is what lets
  `go build ./...` work without Node installed.
- **Frontend:** `make ui` for every rebuild, Dependabot npm PRs included. It runs
  `npm ci`, so it installs exactly the locked tree and leaves `package-lock.json`
  alone. (It used to run `npm install`, which quietly rewrote the lockfile: an npm
  older than the one that wrote it drops fields it does not recognise — `libc`
  platform hints, most recently. Anything else running `npm install` over this repo,
  an IDE's npm integration included, still does that; if the lockfile turns up
  modified on its own, that is why.)
- **CI checks the committed `web/dist` against `web/src`.** If you change anything
  under `web/src`, rebuild and commit the bundle or the build fails. It matters
  because `go install` embeds the committed copy — tagged binaries rebuild it.
- **Headless UI verification:** puppeteer-core driving the system
  `google-chrome`, with a Node TOTP helper to get past mandatory 2FA. Two quirks
  worth knowing: controlled inputs need the native value-setter plus an `input`
  event, and checkboxes nested in a `<label>` double-fire on click — use focus and
  Space instead.

## Throwaway services the tests use

Each is optional: the matching tests skip cleanly when the service isn't there.

| Service | Command | Wire-up |
|---|---|---|
| Redis | `docker run -d --rm -p 6399:6379 redis:7-alpine` | `DC_REDIS_ADDR=127.0.0.1:6399` |
| SMTP sink | `docker run -d -p 1025:1025 -p 8025:8025 axllent/mailpit` | HTTP API on `:8025` to read the mail back |
| LDAP | `osixia/openldap:1.5.0` | `LDAP_DOMAIN=example.org`, admin `cn=admin,dc=example,dc=org`; `ldapadd` a user |
| Auth registry | `registry:2` with an htpasswd file | no `htpasswd` binary here — generate the bcrypt line with Go's `x/crypto/bcrypt`. localhost is insecure-by-default for the daemon |
| sshd | alpine + `apk add openssh` | the host key is exchanged before auth; `DC_SSH_INTEGRATION` runs `TestSSHHostKeyEndToEnd` |
| Second daemon | `scripts/remote-test-daemon.sh up 2` | prints the exact `DC_REMOTE_DOCKER` invocation; serves both TCP and SSH |
| Pinned Engine | `docker run -d --privileged -e DOCKER_TLS_CERTDIR="" -p 127.0.0.1:12375:2375 docker:NN-dind --host=tcp://0.0.0.0:2375 --tls=false` | `DC_COMPAT_DOCKER` **and** `DOCKER_HOST` must both point at it |

> **Always pass `-count=1`** for anything driven by a `DC_*` environment variable.
> Go caches test results and an env-var change does not invalidate that cache, so
> a re-provisioned daemon otherwise replays the previous run's verdict.
