#!/usr/bin/env bash
# Provision throwaway *second* (and third) Docker daemons for the remote-deploy
# tests.
#
# The remote tests need daemons that genuinely cannot see this machine's
# filesystem — that's the whole property under test. docker-in-docker sidecars
# give exactly that, reachable over both remote transports the app supports.
# Each sidecar N (1-based) gets:
#
#   TCP  → tcp://127.0.0.1:$((12374 + N))
#   SSH  → ssh://root@127.0.0.1:$((12221 + N))     (real sshd, real key auth)
#
# Usage:
#   scripts/remote-test-daemon.sh up [count]   # default 1, max 3
#   scripts/remote-test-daemon.sh env          # shell env for the test run
#   scripts/remote-test-daemon.sh down         # remove everything
#
# It NEVER reads or writes your ~/.ssh: the throwaway keypair and known_hosts
# live under a scratch dir, and because OpenSSH takes the home directory from the
# passwd database (not $HOME), the SSH transport is pointed at them through a
# small `ssh` shim placed first on PATH.
set -euo pipefail

NAME="${DC_REMOTE_NAME:-dc-remote-dind}"
WORKDIR="${DC_REMOTE_WORKDIR:-${TMPDIR:-/tmp}/dc-remote-test}"
IMAGE="${DC_REMOTE_IMAGE:-docker:dind}"
MAX_HOSTS=3

die() { echo "error: $*" >&2; exit 1; }

host_name() { [ "$1" = 1 ] && echo "$NAME" || echo "${NAME}-$1"; }
tcp_port()  { echo $((12374 + $1)); }
ssh_port()  { echo $((12221 + $1)); }
tcp_addr()  { echo "tcp://127.0.0.1:$(tcp_port "$1")"; }
ssh_addr()  { echo "ssh://root@127.0.0.1:$(ssh_port "$1")"; }

# provision_one starts sidecar N and installs sshd + the scratch key in it.
provision_one() {
  local n="$1" cname tcp sshp
  cname="$(host_name "$n")"; tcp="$(tcp_addr "$n")"; sshp="$(ssh_port "$n")"

  docker rm -f "$cname" >/dev/null 2>&1 || true
  echo "starting $cname ($IMAGE) — tcp $(tcp_port "$n"), ssh $sshp"
  # --tls=false keeps the sidecar simple; it binds loopback only. The unix socket
  # is what the SSH transport tunnels to.
  docker run -d --name "$cname" --privileged \
    -e DOCKER_TLS_CERTDIR="" \
    -p "127.0.0.1:$(tcp_port "$n"):2375" -p "127.0.0.1:${sshp}:22" \
    "$IMAGE" --host=tcp://0.0.0.0:2375 --host=unix:///var/run/docker.sock --tls=false >/dev/null

  echo -n "  waiting for the daemon"
  for _ in $(seq 1 30); do
    if docker -H "$tcp" version >/dev/null 2>&1; then break; fi
    echo -n .; sleep 2
  done
  echo
  docker -H "$tcp" version >/dev/null 2>&1 \
    || die "$cname never came up; see: docker logs $cname"

  docker exec "$cname" sh -c '
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
  docker exec -i "$cname" sh -c 'cat >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys' \
    < "$WORKDIR/home/.ssh/id_ed25519.pub"

  # Record this sidecar's host key so StrictHostKeyChecking stays ON.
  ssh-keyscan -p "$sshp" 127.0.0.1 2>/dev/null >> "$WORKDIR/home/.ssh/known_hosts"
}

up() {
  local count="${1:-1}"
  case "$count" in
    1|2|3) ;;
    *) die "count must be 1..$MAX_HOSTS" ;;
  esac
  command -v docker >/dev/null || die "docker is not on PATH"

  mkdir -p "$WORKDIR/home/.ssh" "$WORKDIR/bin"
  chmod 700 "$WORKDIR/home/.ssh"
  : > "$WORKDIR/home/.ssh/known_hosts"

  if [ ! -f "$WORKDIR/home/.ssh/id_ed25519" ]; then
    ssh-keygen -t ed25519 -N "" -C dc-remote-test \
      -f "$WORKDIR/home/.ssh/id_ed25519" >/dev/null
    chmod 600 "$WORKDIR/home/.ssh/id_ed25519"
  fi

  # Remove any sidecar above the requested count, so `up 1` after `up 3` leaves a
  # clean single-host setup rather than stale daemons on old ports.
  local i
  for i in $(seq 1 "$MAX_HOSTS"); do
    if [ "$i" -gt "$count" ]; then
      docker rm -f "$(host_name "$i")" >/dev/null 2>&1 || true
    fi
  done

  for i in $(seq 1 "$count"); do provision_one "$i"; done
  echo "$count" > "$WORKDIR/count"

  # OpenSSH resolves ~ from the passwd database, so $HOME can't redirect it to
  # our scratch keys — a shim first on PATH does.
  local real_ssh
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
  if [ ! -S "$WORKDIR/agent.sock" ]; then
    ssh-agent -a "$WORKDIR/agent.sock" > "$WORKDIR/agent.env" 2>/dev/null \
      || die "could not start a dedicated ssh-agent"
    SSH_AUTH_SOCK="$WORKDIR/agent.sock" ssh-add -q "$WORKDIR/home/.ssh/id_ed25519" 2>/dev/null \
      || die "could not add the scratch key to the dedicated agent"
  fi

  # Verify every sidecar over BOTH transports before declaring success.
  for i in $(seq 1 "$count"); do
    docker -H "$(tcp_addr "$i")" version >/dev/null 2>&1 \
      || die "sidecar $i is unreachable over TCP"
    PATH="$WORKDIR/bin:$PATH" SSH_AUTH_SOCK="$WORKDIR/agent.sock" DOCKER_HOST="$(ssh_addr "$i")" \
      docker version >/dev/null 2>&1 \
      || die "sidecar $i is unreachable over SSH (is the docker CLI in $IMAGE?)"
  done

  echo
  echo "$count remote daemon(s) ready:"
  for i in $(seq 1 "$count"); do
    echo "  #$i  $(tcp_addr "$i")   $(ssh_addr "$i")"
  done
  echo
  echo "single-host tests:"
  echo "  DC_REMOTE_DOCKER=$(tcp_addr 1) \\"
  echo "    go test ./internal/docker/ -run RemoteBindDeploy -count=1 -v"
  echo
  echo "  eval \"\$($0 env)\" \\"
  echo "    && go test ./internal/docker/ -run RemoteBindDeploy -count=1 -v"
  if [ "$count" -ge 2 ]; then
    echo
    echo "multi-host tests (needs >= 2 sidecars; host 1 over SSH, host 2 over TCP):"
    echo "  eval \"\$($0 env)\" \\"
    echo "    && go test ./internal/docker/ -run MultiHost -count=1 -v"
  fi
  echo
  echo "NOTE: keep -count=1. Go caches test results, and the env vars alone do not"
  echo "      invalidate the cache — a re-provisioned sidecar will otherwise replay"
  echo "      the previous run's verdict."
  echo
  echo "tear down with: $0 down"
}

# env prints the shell environment for the tests: the shim (so OpenSSH, which
# takes ~ from the passwd database rather than $HOME, finds the scratch
# known_hosts) and the dedicated agent (so the Go SDK offers only the scratch
# key). HOME is deliberately left alone — redirecting it would also move Go's
# module cache.
#
# Host 1 is exported over SSH and host 2 over TCP on purpose: the multi-host test
# then exercises a mixed-transport fleet, which is where per-host state could
# cross-contaminate.
env_cmd() {
  [ -x "$WORKDIR/bin/ssh" ] || die "not provisioned yet — run: $0 up"
  [ -S "$WORKDIR/agent.sock" ] || die "the dedicated ssh-agent is gone — re-run: $0 up"
  local count
  count="$(cat "$WORKDIR/count" 2>/dev/null || echo 1)"
  echo "export PATH='$WORKDIR/bin':\$PATH"
  echo "export SSH_AUTH_SOCK='$WORKDIR/agent.sock'"
  echo "export DC_REMOTE_DOCKER='$(ssh_addr 1)'"
  [ "$count" -ge 2 ] && echo "export DC_REMOTE_DOCKER_2='$(tcp_addr 2)'"
  [ "$count" -ge 3 ] && echo "export DC_REMOTE_DOCKER_3='$(ssh_addr 3)'"
  return 0
}

down() {
  local i
  for i in $(seq 1 "$MAX_HOSTS"); do
    local cname
    cname="$(host_name "$i")"
    docker rm -f "$cname" >/dev/null 2>&1 && echo "removed $cname" || true
  done
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
  up)   up "${2:-1}" ;;
  env)  env_cmd ;;
  down) down ;;
  *)    echo "usage: $0 {up [count]|env|down}" >&2; exit 2 ;;
esac
