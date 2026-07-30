#!/usr/bin/env bash
# Provision a throwaway *second* Docker daemon for the remote-deploy tests.
#
# The remote-bind tests need a daemon that genuinely cannot see this machine's
# filesystem — that's the whole property under test. A docker-in-docker sidecar
# gives exactly that, reachable over both remote transports the app supports:
#
#   TCP  → tcp://127.0.0.1:12375
#   SSH  → ssh://root@127.0.0.1:12222   (real sshd, real key auth)
#
# Usage:
#   scripts/remote-test-daemon.sh up      # start it and print the test commands
#   scripts/remote-test-daemon.sh env     # print the env for the SSH transport
#   scripts/remote-test-daemon.sh down    # remove the container and scratch dir
#
# It NEVER reads or writes your ~/.ssh: the throwaway keypair and known_hosts
# live under a scratch dir, and because OpenSSH takes the home directory from the
# passwd database (not $HOME), the SSH transport is pointed at them through a
# small `ssh` shim placed first on PATH.
set -euo pipefail

NAME="${DC_REMOTE_NAME:-dc-remote-dind}"
TCP_PORT="${DC_REMOTE_TCP_PORT:-12375}"
SSH_PORT="${DC_REMOTE_SSH_PORT:-12222}"
WORKDIR="${DC_REMOTE_WORKDIR:-${TMPDIR:-/tmp}/dc-remote-test}"
IMAGE="${DC_REMOTE_IMAGE:-docker:dind}"

TCP_ADDR="tcp://127.0.0.1:${TCP_PORT}"
SSH_ADDR="ssh://root@127.0.0.1:${SSH_PORT}"

die() { echo "error: $*" >&2; exit 1; }

up() {
  command -v docker >/dev/null || die "docker is not on PATH"

  mkdir -p "$WORKDIR/home/.ssh" "$WORKDIR/bin"
  chmod 700 "$WORKDIR/home/.ssh"

  if [ ! -f "$WORKDIR/home/.ssh/id_ed25519" ]; then
    ssh-keygen -t ed25519 -N "" -C dc-remote-test \
      -f "$WORKDIR/home/.ssh/id_ed25519" >/dev/null
    chmod 600 "$WORKDIR/home/.ssh/id_ed25519"
  fi

  docker rm -f "$NAME" >/dev/null 2>&1 || true
  echo "starting $NAME ($IMAGE)…"
  # --tls=false keeps the sidecar simple; it listens on loopback only. The unix
  # socket is what the SSH transport tunnels to (docker system dial-stdio).
  docker run -d --name "$NAME" --privileged \
    -e DOCKER_TLS_CERTDIR="" \
    -p "127.0.0.1:${TCP_PORT}:2375" -p "127.0.0.1:${SSH_PORT}:22" \
    "$IMAGE" --host=tcp://0.0.0.0:2375 --host=unix:///var/run/docker.sock --tls=false >/dev/null

  echo -n "waiting for the daemon"
  for _ in $(seq 1 30); do
    if docker -H "$TCP_ADDR" version >/dev/null 2>&1; then break; fi
    echo -n .; sleep 2
  done
  echo
  docker -H "$TCP_ADDR" version >/dev/null 2>&1 \
    || die "the remote daemon never came up; see: docker logs $NAME"

  echo "installing sshd in the sidecar…"
  docker exec "$NAME" sh -c '
    set -e
    apk add --no-cache openssh >/dev/null
    ssh-keygen -A >/dev/null
    mkdir -p /root/.ssh && chmod 700 /root/.ssh
    printf "PermitRootLogin prohibit-password\nPubkeyAuthentication yes\n" >> /etc/ssh/sshd_config
    # AllowTcpForwarding gates the direct-streamlocal channel the Go SDK uses to
    # tunnel the remote /var/run/docker.sock; the app surfaces a refusal as
    # "ssh: rejected: connect failed". Alpine ships it as "no", and sshd honours
    # the FIRST occurrence of a keyword — so rewrite it in place rather than
    # appending, which would be silently ignored.
    sed -i "s/^[[:space:]]*AllowTcpForwarding.*/AllowTcpForwarding yes/I" /etc/ssh/sshd_config
    grep -qiE "^AllowTcpForwarding yes" /etc/ssh/sshd_config \
      || echo "AllowTcpForwarding yes" >> /etc/ssh/sshd_config
    pidof sshd >/dev/null || /usr/sbin/sshd
  '
  docker exec -i "$NAME" sh -c 'cat >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys' \
    < "$WORKDIR/home/.ssh/id_ed25519.pub"

  # Record the sidecar's host key so StrictHostKeyChecking stays ON.
  ssh-keyscan -p "$SSH_PORT" 127.0.0.1 2>/dev/null > "$WORKDIR/home/.ssh/known_hosts"
  [ -s "$WORKDIR/home/.ssh/known_hosts" ] || die "could not read the sidecar's SSH host key"

  # OpenSSH resolves ~ from the passwd database, so $HOME can't redirect it to
  # our scratch keys — a shim first on PATH does.
  real_ssh="$(command -v ssh)" || die "ssh is not on PATH"
  cat > "$WORKDIR/bin/ssh" <<EOF
#!/bin/sh
exec $real_ssh \\
  -o UserKnownHostsFile=$WORKDIR/home/.ssh/known_hosts \\
  -o IdentityFile=$WORKDIR/home/.ssh/id_ed25519 \\
  -o IdentitiesOnly=yes \\
  -o StrictHostKeyChecking=yes \\
  "\$@"
EOF
  chmod +x "$WORKDIR/bin/ssh"

  # A dedicated agent holding ONLY the scratch key. The Go SDK's ssh auth tries
  # SSH_AUTH_SOCK first, and a developer agent with several keys gets them all
  # offered and rejected — exhausting sshd's MaxAuthTries (6) before the right
  # key is ever tried. One key, one agent, no ambiguity; the real agent is
  # untouched.
  rm -f "$WORKDIR/agent.sock"
  ssh-agent -a "$WORKDIR/agent.sock" > "$WORKDIR/agent.env" 2>/dev/null \
    || die "could not start a dedicated ssh-agent"
  SSH_AUTH_SOCK="$WORKDIR/agent.sock" ssh-add -q "$WORKDIR/home/.ssh/id_ed25519" 2>/dev/null \
    || die "could not add the scratch key to the dedicated agent"

  PATH="$WORKDIR/bin:$PATH" SSH_AUTH_SOCK="$WORKDIR/agent.sock" \
    ssh -p "$SSH_PORT" -o BatchMode=yes root@127.0.0.1 true 2>/dev/null \
    || die "SSH into the sidecar failed"
  PATH="$WORKDIR/bin:$PATH" SSH_AUTH_SOCK="$WORKDIR/agent.sock" DOCKER_HOST="$SSH_ADDR" \
    docker version >/dev/null 2>&1 \
    || die "the SSH docker transport failed (is the docker CLI present in $IMAGE?)"

  echo
  echo "remote daemon ready — $(docker -H "$TCP_ADDR" version --format '{{.Server.Version}}')"
  echo
  echo "TCP transport:"
  echo "  DC_REMOTE_DOCKER=$TCP_ADDR \\"
  echo "    go test ./internal/docker/ -run RemoteBindDeploy -count=1 -v"
  echo
  echo "SSH transport (shim on PATH + the dedicated single-key agent):"
  echo "  eval \"\$(scripts/remote-test-daemon.sh env)\" \\"
  echo "    && go test ./internal/docker/ -run RemoteBindDeploy -count=1 -v"
  echo
  echo "NOTE: keep -count=1. Go caches test results, and the env var alone does not"
  echo "      invalidate the cache — a re-provisioned sidecar will otherwise replay"
  echo "      the previous run's verdict."
  echo
  echo "tear down with: scripts/remote-test-daemon.sh down"
}

# env prints the shell environment for the SSH transport: the shim (so OpenSSH,
# which takes ~ from the passwd database rather than $HOME, finds the scratch
# known_hosts) and the dedicated agent (so the Go SDK offers only the scratch
# key). HOME is deliberately left alone — redirecting it would also move Go's
# module cache.
env_cmd() {
  [ -x "$WORKDIR/bin/ssh" ] || die "not provisioned yet — run: $0 up"
  [ -S "$WORKDIR/agent.sock" ] || die "the dedicated ssh-agent is gone — re-run: $0 up"
  echo "export PATH='$WORKDIR/bin':\$PATH"
  echo "export SSH_AUTH_SOCK='$WORKDIR/agent.sock'"
  echo "export DC_REMOTE_DOCKER='$SSH_ADDR'"
}

down() {
  docker rm -f "$NAME" >/dev/null 2>&1 && echo "removed $NAME" || echo "$NAME was not running"
  if [ -f "$WORKDIR/agent.env" ]; then
    # ssh-agent -k kills by SSH_AGENT_PID, which is in the env file it printed.
    # shellcheck disable=SC1090
    . "$WORKDIR/agent.env" >/dev/null 2>&1 || true
    ssh-agent -k >/dev/null 2>&1 || true
    echo "stopped the dedicated ssh-agent"
  fi
  if [ -d "$WORKDIR" ]; then
    # Make everything writable first: anything Go wrote here (module cache) is
    # read-only by design and would otherwise defeat rm.
    chmod -R u+w "$WORKDIR" 2>/dev/null || true
    rm -rf "$WORKDIR"
    [ -d "$WORKDIR" ] && echo "warning: $WORKDIR could not be fully removed" || echo "removed $WORKDIR"
  fi
}

case "${1:-}" in
  up)   up ;;
  env)  env_cmd ;;
  down) down ;;
  *)    echo "usage: $0 {up|env|down}" >&2; exit 2 ;;
esac
