# Policy rules

[← Manual index](README.md)

![Policy rules](images/settings_policy.png)

**Policy rules** are deploy-time guardrails for [Projects](projects.md). Before a
project is deployed, Docker Commander looks at the Compose model that is about
to run and checks it against a short list of risky or sloppy settings. Each rule
is independently **Off**, **Warn** or **Block**.

You'll find them under **Settings → Policy rules** (`/settings?tab=policy`).
Only admins can view or change them. The page used to be a separate menu item.

## The rules
| Rule | Triggers when a service… |
|------|--------------------------|
| **Privileged containers** (`privileged`) | sets `privileged: true` — effectively root on the host |
| **Host network mode** (`host_network`) | uses `network_mode: host` |
| **Host PID namespace** (`host_pid`) | uses `pid: host` |
| **Docker socket mount** (`docker_socket_mount`) | bind-mounts the Docker socket (see below) — control over every container on the host |
| **Unpinned image (:latest)** (`latest_tag`) | uses an image with no tag or with the `latest` tag, and no digest |
| **Missing resource limits** (`missing_resource_limits`) | has no CPU or memory limit |
| **Missing healthcheck** (`missing_healthcheck`) | has no healthcheck, or disables it |

Every rule is evaluated **per service**, so a project with two offending
services reports two violations. Details:

- **Unpinned image** — `nginx` and `nginx:latest` are unpinned; `nginx:1.27` is
  pinned; anything with an `@sha256:…` digest counts as pinned, even if it also
  has a tag. A registry port (`registry:5000/app`) is not mistaken for a tag.
- **Resource limits** — satisfied by a CPU or memory limit under
  `deploy.resources.limits`, or by the service-level `cpus` / `mem_limit`.
- **Healthcheck** — a service with `healthcheck: {disable: true}` counts as
  having none.

### How the Docker socket check works
Only **bind mounts** are inspected (a named volume can't expose the socket). A
bind mount is flagged when any of these holds:

- the source or the target path ends in `docker.sock`;
- the source is `/`, the whole host filesystem;
- the source is a **parent directory of a well-known socket location**
  (`/var/run/docker.sock`, `/run/docker.sock`) — for example `/var`,
  `/var/run` or `/run`. Mounting the directory hands the container the socket as
  surely as mounting the file;
- the source is a user's **rootless runtime directory** — `/run/user`,
  `/run/user/<uid>`, or the same under `/var/run` — where rootless Docker keeps
  its `docker.sock`.

Paths are normalised first, so `/var/run/../run/docker.sock` doesn't slip past.
The check knows the default locations; a socket reachable only through some
other directory name that doesn't end in `docker.sock` is not recognised.

The rules see the **resolved** Compose model (what `docker compose config`
produces — interpolated variables, merged files, and the profiles this deploy
activates), not the raw text of the file. A violation in a service from a
profile you are deploying is caught; one in a profile you aren't deploying is
not.

## The three modes
| Mode | Effect on a deploy |
|------|--------------------|
| **Off** | The rule is never evaluated. This is the default for every rule. |
| **Warn** | The deploy is paused and you are asked to confirm. Confirming runs it; the acknowledgement is audited. |
| **Block** | The deploy is refused. There is **no per-deploy override** — the only way past it is an admin changing the rule's mode (or fixing the project). |

Nothing is enabled out of the box, on purpose: most existing Compose files have
no healthchecks or limits, so defaulting to *Warn* would suddenly demand
confirmations nobody asked for. Turn on the rules you actually want enforced.
While **every** rule is Off, deploys skip the check entirely and pay no extra
time.

To change modes, pick **Off / Warn / Block** for each rule and press **Save
policy rules**. If the current rules can't be loaded the page shows an error and
disables saving, so a retry can never overwrite them with an empty set.

## What you see when deploying
From the project list or the project editor:

- A **Warn** violation opens **"Deploy has policy warnings"**, listing each
  rule, service and reason, with **Deploy anyway** (the deploy then goes ahead
  and is audited as acknowledged) or cancel.
- A **Block** violation opens **"Deploy blocked by policy"**, naming the rules
  and pointing at Policy rules in Settings. Nothing is started.

The check runs **before anything is changed**: for a project on a remote host,
no files are shipped to the host, and for a restore no project files are
replaced, until the policy has passed.

## Where the rules apply
The same check guards every way of deploying a managed project:

- **Deploy** from the UI or the REST API.
- **Restoring a revision** from the project's deploy history. The revision's
  files are checked in a staging copy before they replace the live ones, so a
  refused restore leaves the project exactly as it was. The deploy-history
  dialog doesn't offer a "restore anyway" step, so under **Warn** a restore that
  triggers a rule is refused; the REST API accepts `confirmPolicyWarnings` to
  acknowledge it.
- **MCP** — the `deploy_project` tool. **Block** refuses; a **Warn** violation
  is refused once with a message telling the caller to retry with
  `confirm_policy_warnings=true` (there is no dialog to click through, so the AI
  client confirms on your behalf). See [MCP](mcp.md).

Not covered: containers created by hand in the UI, and anything started outside
Docker Commander. The rules apply to deploys only — nothing already running is
re-evaluated when you change a rule. The read-only *deploy preview* (what a
deploy would change) does not evaluate them either.

## If the check itself fails
Policy checking **fails closed** where it matters:

- If the stored rules can't be read, the deploy is refused — every rule's mode
  is unknown, so it is treated as though a Block rule might apply.
- If the Compose model can't be resolved or evaluated **and at least one rule
  is on Block**, the deploy is refused with "policy check failed; refusing to
  deploy for safety".
- If it can't be resolved but every active rule is only **Warn**, the failure is
  logged and the deploy proceeds — a broken check never blocks a deploy that
  couldn't have been hard-blocked anyway, and it never waves through one that
  could.

## Audit
Changing the rules is audited as `policy.rules.update`. Deploys record:

| Situation | Deploy | Revision restore |
|-----------|--------|------------------|
| Refused by a Block rule | `project.deploy.policy_block` | `project.revision.restore.policy_block` |
| Warn confirmed | `project.deploy.policy_warn_ack` | `project.revision.restore.policy_warn_ack` |
| Check itself failed | `project.deploy.policy_check_failed` | `project.revision.restore.policy_check_failed` |

Each entry names the project and lists the `rule:service` pairs involved. A deploy
through MCP is recorded under `mcp.project.deploy` (including when it was
refused), without the dedicated policy actions. See the [audit log](audit.md).
