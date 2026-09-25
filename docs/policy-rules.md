# Policy rules

[← Manual index](README.md)

![Policy rules](images/settings_policy.png)

**Policy rules** check a [Project](projects.md) before it is deployed. Docker
Commander evaluates the Compose model about to run against a list of risky
settings. Each rule is set to **Off**, **Warn** or **Block**.

Find them under **Settings → Policy rules** (`/settings?tab=policy`). Only
admins can view or change them.

## The rules
| Rule | Triggers when a service... |
|------|----------------------------|
| **Privileged containers** (`privileged`) | sets `privileged: true` |
| **Host network mode** (`host_network`) | uses `network_mode: host` |
| **Host PID namespace** (`host_pid`) | uses `pid: host` |
| **Docker socket mount** (`docker_socket_mount`) | bind-mounts the Docker socket (see below) |
| **Unpinned image (:latest)** (`latest_tag`) | uses an image with no tag or the `latest` tag, and no digest |
| **Missing resource limits** (`missing_resource_limits`) | has no CPU or memory limit |
| **Missing healthcheck** (`missing_healthcheck`) | has no healthcheck, or disables it |

Rules are evaluated per service. Two offending services give two violations.

- **Unpinned image:** `nginx` and `nginx:latest` are unpinned. `nginx:1.27` is
  pinned. An `@sha256:…` digest counts as pinned even with a tag. A registry
  port (`registry:5000/app`) is not read as a tag.
- **Resource limits:** a limit under `deploy.resources.limits`, or the
  service-level `cpus` or `mem_limit`, satisfies the rule.
- **Healthcheck:** `healthcheck: {disable: true}` counts as none.

### Docker socket check
Only bind mounts are inspected. A bind mount is flagged when:

- the source or target path ends in `docker.sock`
- the source is `/`
- the source is a parent directory of `/var/run/docker.sock` or
  `/run/docker.sock`, for example `/var`, `/var/run` or `/run`
- the source is a rootless runtime directory: `/run/user`, `/run/user/<uid>`,
  or the same under `/var/run`

Paths are normalised first, so `/var/run/../run/docker.sock` is caught. A
socket under another directory name that does not end in `docker.sock` is not
detected.

The rules run on the resolved Compose model (the output of
`docker compose config`: variables interpolated, files merged, this deploy's
profiles applied). A violation in a profile you are not deploying is not
reported.

## The three modes
| Mode | Effect on a deploy |
|------|--------------------|
| **Off** | The rule is not evaluated. This is the default for every rule. |
| **Warn** | The deploy pauses and asks you to confirm. Confirming runs it and is audited. |
| **Block** | The deploy is refused. There is no per-deploy override. An admin must change the mode, or you must fix the project. |

If every rule is Off, deploys skip the check.

To change modes, pick **Off / Warn / Block** per rule and press **Save policy
rules**. If the current rules fail to load, the page shows an error and
disables saving.

## What you see when deploying
- A **Warn** violation opens **"Deploy has policy warnings"** with each rule,
  service and reason. Choose **Deploy anyway** or cancel.
- A **Block** violation opens **"Deploy blocked by policy"**. Nothing is
  started.

The check runs before anything changes. On a remote host no files are shipped
until the policy passes. On a restore no project files are replaced.

## Where the rules apply
- **Deploy** from the UI or the REST API.
- **Restoring a revision** from the deploy history. The files are checked in a
  staging copy first, so a refused restore leaves the project unchanged. The
  deploy-history dialog has no "restore anyway" step, so under **Warn** a
  restore that triggers a rule is refused. The REST API accepts
  `confirmPolicyWarnings` to acknowledge it.
- **MCP**, the `deploy_project` tool. **Block** refuses. A **Warn** violation
  is refused once, with a message to retry with
  `confirm_policy_warnings=true`. See [MCP](mcp.md).

Not covered:

- containers created by hand in the UI
- anything started outside Docker Commander
- containers already running when you change a rule
- the read-only deploy preview

## If the check itself fails
- If the stored rules cannot be read, the deploy is refused.
- If the Compose model cannot be resolved and at least one rule is on Block,
  the deploy is refused with "policy check failed; refusing to deploy for
  safety".
- If the model cannot be resolved and every active rule is Warn, the failure is
  logged and the deploy proceeds.

## Audit
Changing the rules is audited as `policy.rules.update`. Deploys record:

| Situation | Deploy | Revision restore |
|-----------|--------|------------------|
| Refused by a Block rule | `project.deploy.policy_block` | `project.revision.restore.policy_block` |
| Warn confirmed | `project.deploy.policy_warn_ack` | `project.revision.restore.policy_warn_ack` |
| Check itself failed | `project.deploy.policy_check_failed` | `project.revision.restore.policy_check_failed` |

Each entry names the project and lists the `rule:service` pairs. A deploy
through MCP is recorded as `mcp.project.deploy`, including when refused, without
the policy actions. See the [audit log](audit.md).
