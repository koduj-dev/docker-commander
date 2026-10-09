# Docker versions

[← Manual index](README.md)

The app talks to Docker two ways: the **Engine API** through the official Go SDK,
and the **`docker compose` CLI** as a subprocess for project deploys. Both move,
and you run whatever your distro ships — so here is what is actually tested, not
what is hoped for.

| | Version |
|---|---|
| **Minimum Engine API** | **1.43** (Docker Engine 24) |
| **Tested Engine majors** | 24, 25, 26, 27, 28, 29 (nightly; see the workflow runs for the current result) |
| **Tested Engine patches** | a handful of exact patch releases of the newest majors are also pinned and tested nightly, independent of the floating major tags (see the workflow runs for the current exact versions) |
| **Compose** | the `docker compose` plugin, v2 or newer (legacy `docker-compose` v1 is not supported); a handful of recent v2 releases are pinned and tested nightly (see the workflow runs) |
| **Client SDK** | pinned in `go.mod`, negotiated **down** to the daemon at connect time |

The SDK negotiates the API version on its own (it does so by default), so a newer
client speaks whatever the daemon understands — you do not need to match versions.
Below API 1.43 the app is neither tested nor claimed to work.

These numbers are **measured, not remembered**: the
[compatibility workflow](../.github/workflows/compat.yml) runs the app's whole Docker
integration suite against a pinned `docker:NN-dind` for each major, nightly and on
demand, and prints the negotiated API version — plus the Compose version it ran
with — for every run. Three axes are pinned: every Engine major is tested against
the newest of a small set of recent Compose releases, the newest Engine major is
additionally tested against the older ones in that set, and a handful of exact
Engine **patch** releases (`docker:X.Y.Z-dind`, not just `docker:NN-dind`) are
pinned and tested too — since the plain major tag always floats to whatever patch
is newest when it's pulled, it alone can't prove a *specific* patch is good, only
that the latest one currently is. So the matrix answers "which daemons work",
"which recent Compose releases work", and "does this exact Engine patch work",
short of a full Engine × Compose cross product. Reproduce any row locally:

```bash
docker run -d --name dc-compat --privileged -e DOCKER_TLS_CERTDIR="" \
  -p 127.0.0.1:12375:2375 docker:24-dind --host=tcp://0.0.0.0:2375 --tls=false
DC_COMPAT_DOCKER=tcp://127.0.0.1:12375 DOCKER_HOST=tcp://127.0.0.1:12375 \
  go test -count=1 -run 'TestIntegration|TestCompat' ./internal/docker/
```

> Both variables matter: `DC_COMPAT_DOCKER` points the app at the daemon,
> `DOCKER_HOST` points the Compose CLI at the *same* one. Set only the first and
> the Compose tests deploy to your own daemon and hang waiting for containers
> that started somewhere else.
