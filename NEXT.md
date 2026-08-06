# What's next

**This file is only about work that has not been done.** What already exists is
described where it belongs and nowhere else:

- **What shipped, and when** → [CHANGELOG.md](CHANGELOG.md)
- **What the app does today** → [README](README.md#-features) and [docs/](docs/)
- **Things that bit us** → [docs/gotchas.md](docs/gotchas.md)
- **How this machine is set up** → [docs/dev-environment.md](docs/dev-environment.md)

Keeping shipped features here made the roadmap grow instead of shrink, and made
it hard to see what was actually left. If an item below ships, delete it — the
changelog is the record.

## 🎯 What we want from it

A self-hosted Docker control panel that an operator can trust with production:
one binary, no external database, safe by default, and honest about what it does
and doesn't know.

The next step change is moving from *"I can perform an operation on Docker"* to
*"I know what changed, why it broke, who it affected, and how to put it back."*

---

## 🔭 Open work

### Alerting and incidents

The alert engine now tracks conditions with a lifetime (firing → escalated →
resolved), which is the foundation the next two items assume. Building them on
the old repeat-every-cooldown engine would have produced a timeline of noise.

- **Maintenance windows and silences.** Suppress notifications during planned work
  without turning monitoring off. Scope by host, project/stack, container, rule or
  severity; one-off and recurring; a reason and an author; audited. An incident
  should still be *recorded* while silenced — the point is to stop the paging, not
  the observing. Also: automatic silence during a deploy, with a configurable grace
  period afterwards. This is distinct from a **disabled host**, which is not
  monitored at all.
- **Incident timeline / correlation.** Join alerts, Docker events, deploys, log
  matches and metrics into one explainable incident: what changed, what broke, what
  it affected, and a link to the revision to roll back to. Feeds MCP tools
  (`diagnose_container`, `get_incident`, `list_active_incidents`).
- **Alert delivery retry.** Failures are now *recorded* but never re-attempted.
  Retry needs a queue and a backoff policy, and silently retrying a webhook that
  returns 500 for a good reason is its own hazard — worth doing deliberately.
- **Per-session MCP token revocation.** Revoking is currently per OAuth *client*:
  there is no "sign this one tool out", and a stolen token can't be killed
  individually short of removing its client. A `jti` denylist would buy that, at
  the cost of a lookup and a table to prune.

### Safe changes

- **Deployment revisions and rollback.** An immutable history of every project and
  edited-stack deploy — compose file, sidecar files, resolved config, profiles,
  target host, image references *and resolved digests*, validation result, output,
  author, reason — with diff, preview and restore. Notes that matter: roll back a
  mutable tag using the stored **digest**; a revision must identify its remote bind
  snapshots; rollback must not delete persistent named volumes; it should re-run
  validation before applying; and a CLI-discovered stack must keep its original
  working directory. **The highest-value item on this list.**
- **Policy checks before deploy.** Refuse or warn on privileged containers, host
  network/PID, docker-socket mounts, `:latest` in production, missing resource
  limits, missing healthchecks. Some pieces exist already — compose validation,
  the duplicate-host-port check, Dockerfile linting, Trivy scanning — but there is
  no policy engine tying them to a decision.
- **Controlled image updates.** Detect that a newer image exists for a running
  workload, show what would change, and update deliberately. (Distinct from
  self-update, which is about the Docker Commander binary and already ships.)

### Network statistics

Phases 1 and 2 have shipped — per-container throughput, totals, packets, drops,
errors and a per-interface breakdown; endpoint totals on a network's detail; a
host-wide summary on the dashboard — and so has phase 3's storage half: history
keeps the **raw cumulative counters** and derives rates at read time. What is left
is what you *do* with them.

- **Alert on network.** Rule metrics are still `cpu` / `cpu_total` / `mem` only.
  Throughput needs a rule of its own, and drops and errors should be alerted on by
  their **increase**, not their absolute value — a counter sitting at 12 since a
  bad afternoon last month is not an incident.
- **Top talkers.** Only readable over a window: point-in-time throughput reorders
  itself on every poll, which is why the dashboard shows a host-wide time series
  rather than a ranking. Rank over a stored interval, not over a sample.
- **Phase 4 (optional) — a Linux collector** via netlink/eBPF for flows, protocols,
  connections and retransmits. Keep it an optional capability, never a condition of
  running Docker Commander: it is Linux-only, awkward under Docker Desktop and
  rootless, and needs host namespaces.

_Interface-to-network mapping stays unsolved, and constrains all of the above:_
`ContainerStats` is keyed by `eth0`, `eth1`… and never says which Docker network
each belongs to (the API's `endpoint_id` field is filled in on Windows only).
Exact mapping needs MAC/namespace inspection via netlink — Linux-only and hostile
to remote hosts. So the app sums across interfaces and says so, rather than
publishing a per-network number that looks authoritative and is wrong.

### Identity and access

- **OIDC / SSO** — Google/Azure/Okta login. LDAP (including group→role) is step
  one. **Testable without any provider account**: run Dex or Keycloak in a
  container, the way LDAP is already tested against OpenLDAP. Only provider-specific
  quirks — Azure's `iss` shape, Google not returning `groups` without Directory API
  — need a real tenant, and those are the last mile rather than the blocker.

### Configuration and secrets

- **Secrets references** in compose — pull values from an internal store or an
  external provider rather than inlining them, with resolved previews that stay
  redacted.
- **Parameterized user templates.** Built-in presets support `{{.Var}}`;
  user-saved ones are literal snapshots. Add variables, validation and safe
  handling of generated secrets.
- **Per-instance isolation of file-backed mounts.** Two instances of the same
  service block de-duplicate *named volumes* but still share the block's sidecar
  paths (both nginx instances mount `./html`).
- **Remote template catalog.** The `source: builtin|user|remote` field exists; a
  provider needs catalog signing, item versions, a local cache, a trust policy and
  a preview before import.

### Smaller, well-scoped

- **Bulk operations** (restart/start/stop/pull across a selection) with preview,
  confirmation, bounded parallelism, per-host RBAC and a clear success/failure
  summary.
- **Log bookmarks** — save a time range plus filters, link it to an incident, share
  it with users who have the rights, export a small diagnostic bundle without
  secrets.
- **Native Slack / Teams / Discord notifications** as a UX layer over the generic
  webhook, which stays the base mechanism.
- **Host maintenance mode**, as distinct from `disabled`: monitoring continues,
  events are still recorded, notifications are suppressed or tagged, and write
  operations can optionally be blocked. Overlaps with silences above — design them
  together.
- **ACME / Let's Encrypt** for public hosts. Self-signed `--make-certs` ships;
  lower priority because production usually sits behind a reverse proxy. Testable
  locally against Pebble.
- **Windows native service.** `--install-service` covers systemd and launchd. A
  console exe is not a native service (SCM error 1053), so this needs
  `golang.org/x/sys/windows/svc`; the Task Scheduler script remains the supported
  path meanwhile.
- **Collapsible sidebar groups.** The sidebar is already grouped (Compute,
  Network, Observability, System); the groups just don't fold. Worth doing only if
  the list grows enough that folding beats scrolling.
- **Version matrix — narrower axes.** It pins Engine **majors**, so a regression in
  a specific patch isn't caught the moment it ships; and it does not pin the
  **Compose plugin**, which comes from the runner. Compose is the more likely one
  to bite, since the README claims "v2 or newer" while CI only ever exercises
  whatever it happens to have (v5 today).

---

## 🧭 Deliberately not doing

Recorded so they don't get re-proposed.

- **In-process Compose deploy engine.** `compose-go` cannot do this — it is a
  parser/loader that never creates anything. The only library that deploys is
  `github.com/docker/compose` (now v5), and importing it took the module graph from
  **114 → 409** modules; a hello-world that merely constructs the service is
  **24.9 MB**, against a 26.8 MB binary today. It builds CGO-free, so the
  single-binary property survives, but expect roughly **2× the size** to remove one
  runtime dependency whose real pain (`ProtectHome` plugin discovery) is already
  fixed.
- **Arbitrary MCP `exec` / file access / prune / remove / image export.** The tool
  surface is deliberately read + safe-control. Stopping things is offered;
  destroying them is not — that is a trip to the UI.

  If they are ever wanted, the shape is settled and should not be re-argued: an
  **opt-in the operator turns on in the UI**, off by default, in a separate risky
  toolset, audited, and constrained by both token and role. A test
  (`TestNoDestructiveToolsAreAdvertised`) fails on any destructive verb in the
  advertised list, so this cannot be eroded a tool at a time.
- **More plain Docker API CRUD wrappers.** The everyday management surface is
  covered. New work should aggregate, explain, protect a change, or make recovery
  possible.
