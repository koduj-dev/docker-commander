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
  `diagnose_container` specifically has a shape worth being deliberate about:
  read logs/events/inspect for a container and return a **suggested**
  root cause and fix — never apply it. Docker's own Gordon agent does roughly
  this (inspects an OOM-killed container, traces exit 137 to the specific
  process, proposes a fix the human must approve). It fits the MCP server's
  existing safe-control philosophy exactly, since the output is advice, not
  an action.
- **External / synthetic checks.** Everything DC alerts on today is
  container-internal (state, resource thresholds, log patterns). It has no
  outside-in check — hitting a URL over HTTP(S)/TCP and alerting if it's
  down, times out, or its TLS cert is close to expiring, the way Uptime Kuma
  does. It's the single most commonly paired tool with Portainer/Dockge in
  self-hosted stack write-ups specifically because that gap exists. Worth
  scoping narrowly (a new alert-rule *source*, reusing the existing
  rule/lifecycle/notification machinery) rather than growing into a general
  monitoring tool.
- **Alert delivery retry.** Failures are now *recorded* but never re-attempted.
  Retry needs a queue and a backoff policy, and silently retrying a webhook that
  returns 500 for a good reason is its own hazard — worth doing deliberately.
- **Per-session MCP token revocation.** Revoking is currently per OAuth *client*:
  there is no "sign this one tool out", and a stolen token can't be killed
  individually short of removing its client. A `jti` denylist would buy that, at
  the cost of a lookup and a table to prune.

### Safe changes

These items — drift detection and revisions/rollback — share one underlying
question with the deployment plan/diff that already shipped (see CHANGELOG):
*what does the compose definition say, what is actually running, and what's
the difference?* They're designed to reuse that same state-diff engine
(`internal/docker/preview.go` + `deployfields.go`: `ServiceSpec`/
`ServiceChange`/`BuildDeployPreview`/`ExtendServiceComparison`) rather than
re-deriving it — that engine already resolves a project's compose against
what's running and reports added/removed/image/digest/env/ports/volumes/
networks/restart/resources/healthcheck differences, with a per-change
`recreates` (downtime-risk) flag, behind both a REST endpoint
(`GET /api/projects/{id}/preview`) and the `preview_deploy` MCP tool.

- ~~**Drift detection (desired vs. running state).**~~ **Shipped** (see
  CHANGELOG): the deploy preview *is* this comparison, with three of its four
  named actions — **view the full diff** (the preview itself), **reconcile**
  (a "Reconcile now" button in that view, which is just Deploy from that
  context), and **explicitly ignore a drift** (per service+kind, persisted,
  reversible, excluded from the active count but never hidden). Still open:
  **adopt** — write the running container's actual config back into the
  compose file, the one action with no shipped equivalent. Scoped out on
  purpose: it means generating a YAML edit against a file that may carry
  anchors/comments/formatting a human wrote, which is a materially different
  (and riskier) problem than reading and reporting a diff.
- ~~**Deployment revisions and rollback.**~~ **Shipped for managed Projects**
  (see CHANGELOG) — was the highest-value item on this list. Every successful
  deploy records an immutable revision: compose file + every sidecar file (a
  zip snapshot; resolved config is derived from it on demand rather than
  stored twice), profiles, target host, image references *and* the digest
  actually running, validation state, output, author and reason. Diff reuses
  the plan/diff engine directly — a revision vs. what's running now, or
  against another revision — including an env diff (key and value; asked
  for as "redacted" originally, reversed after real use showed that made
  the diff unable to answer its own question, for no actual protection —
  the values are already visible in the compose file and the existing
  Resolved preview at the same permission level; a real secrets store, when
  one exists, is where redaction belongs). Restore re-validates the
  snapshot in a scratch dir *before*
  touching anything live, pins any service with a recorded digest so a
  mutable tag can't quietly change what comes back, redeploys with the
  revision's own profiles, and becomes a new revision itself rather than
  rewriting history. Never touches named volumes (the only Docker operation
  is `up`). Two things this deliberately does NOT cover: a **CLI-discovered
  Stack** (edited-stack deploys) has no revision history yet — only
  Projects do; and a remote deploy's revision records which host it targeted
  but not a snapshot of what was copied into seeded bind volumes at the
  time, so "restore" on a remote project rebuilds them from the restored
  compose file rather than reverting them to their exact prior contents.
- **Controlled image updates.** Detect that a newer image exists for a running
  workload, show what would change, and update deliberately. (Distinct from
  self-update, which is about the Docker Commander binary and already ships.)
  Should include a **poll schedule** (periodic registry check, not just
  on-demand), a per-container / per-project **opt-in** for auto-apply once
  policy checks pass, and a **notification** on update — reusing the existing
  alert delivery channels rather than inventing a new one. Arcane ships
  roughly this shape (scheduled polling + auto-update toggle + Discord/email
  notification) — worth using as a reference, not a spec. Round it out with
  per-image controls (ignore this version / ignore major versions / ignore
  this image entirely, and a **minimum-age/cooldown gate** so auto-apply
  doesn't grab a tag the moment it's published — Arcane's issue tracker asks
  for exactly this), an optional **auto-rollback on failed healthcheck**,
  and post-deploy verification (healthcheck result, restart count, crash
  loop, alert status) before calling an update "kept". The "update
  available" notification should **link the image's changelog/release
  notes** where discoverable, not just announce a version bump.
  **Auto-prune the superseded image** is a natural extension once "kept" is
  reliable — a requested feature this list didn't have yet. It's a real
  break from how this app treats destructive operations elsewhere (every
  prune/remove today goes through an in-app confirm dialog, never
  unattended — see docs/gotchas.md), so it needs its own guardrails, not
  just a flag: **off by default**, opt-in **per project/stack** (its own
  setting, not a global toggle), scoped to **only the specific image
  version this redeploy just replaced** (never a general `docker image
  prune`), gated behind the post-deploy verification above succeeding first
  (so a bad update still has its old image to roll back to), and it must
  write an **audit log** entry exactly like a manual prune would.
- **Self-update auto-apply policy.** Self-update (banner + one-tap +
  `--self-upgrade`, SHA-256-verified atomic replace) already ships, but only
  as something an admin triggers by hand. The same poll/policy/audit/notify
  shape as **Controlled image updates** above, applied to DC's own binary
  instead of a workload: an admin opts in and picks a granularity (major /
  minor / patch — patch-and-minor-only is the common WordPress-style
  default, not "auto-apply everything"), the existing update check already
  running server-side applies the release automatically when it matches,
  the event lands in the audit log the same way a manual `update.apply`
  does today, and the next admin to log in sees a "you're now on vX.Y.Z —
  applied automatically on <date>" notice rather than discovering it
  silently. Off by default, same spirit as the image-update opt-in.

### GitOps and Compose sources

- **GitOps stack deploy.** Deploy a stack from a git repository instead of a
  host-backed compose folder — pull compose (and any files it references) from
  a repo/ref/path, redeploy on a poll or a webhook when the ref advances.
  Portainer and Arcane both support this; our Stacks/Projects model is always
  filesystem-backed on the host today. Needs repo credentials (encrypted at
  rest, matching the registry/SMTP secret pattern) — an **SSH deploy key** is
  the expected auth path, matching how self-hosters already grant read access
  to a private repo — a fetch step ahead of the existing validate → deploy
  pipeline, and a decision on whether a git-sourced project stays editable in
  the built-in editor or is read-only between pulls. Consider a
  **Renovate-style mode** alongside direct-apply: instead of redeploying
  straight away, open a PR against the repo bumping the image tag and let a
  human merge it — treats an available update as a diffable change to
  review, not just a notification to act on.
  **Build-from-source is this, not a separate feature:** once the full repo
  is pulled (not just `compose.yml`), a service with `build: context: ./app`
  already builds correctly through the existing `--build` / remote-build
  machinery — no new capability needed. The only new rule is the refusal
  case: a repo with neither a compose file nor a Dockerfile at the given path
  gets rejected outright. Deliberately **no AI-driven "figure out how to run
  this repo"** — DC decides how to run something from an explicit compose
  file or Dockerfile, never by having something infer it, which would be a
  much bigger trust boundary than anything the MCP surface allows today.
- **Monorepo-aware mapping.** Self-hosters commonly keep one repo for the
  whole homelab (`infra/host-a/immich`, `infra/host-b/monitoring`…), not one
  repo per stack. Clone the repo once and map multiple projects/hosts to
  subdirectories of that single checkout, rather than a separate clone per
  stack — plus shared files across projects (a common `.env`, shared compose
  fragments via `include`/`extends`). Depends on GitOps stack deploy above;
  design it in from the start rather than bolting it on, since "one repo, one
  checkout, many project paths" is a different data model than "one project,
  one repo".
- **Lightweight webhook redeploy.** A single authenticated per-stack/project
  webhook URL that just re-pulls and redeploys — no git required. Smaller and
  more common a want than full GitOps (people who keep compose on the host,
  not in a repo, still want "redeploy on push from CI"). Complements, doesn't
  replace, GitOps deploy above.

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

- **Project secrets.** A GitHub-Actions-secrets-style store: name a value once
  (`DB_PASSWORD`, `API_TOKEN`…), reference it from compose/`.env` instead of
  inlining it, and it never appears in plaintext again — not in the compose
  file, not in a git-sourced repo, not in the resolved-config preview, not in
  a deployment plan/diff, not in logs, not in a normal API response. Matters
  more now that GitOps/build-from-source deploy is planned: a repo pulled
  from git must never need a real secret committed to it to run. RBAC on who
  can view/edit which secrets; audit *that* a secret changed, never its
  value; detect a likely-secret pasted directly into a compose editor and
  offer to move it. Storage stays DC's own encrypted-at-rest store for now
  (matching the registry/SMTP/LDAP secret pattern already in place) — `.env`
  import and an external provider (SOPS+age, Docker Secrets, Vault…) are
  later, optional backends, not a prerequisite. Don't try to become Vault.
- **Parameterized user templates.** Built-in presets support `{{.Var}}`;
  user-saved ones are literal snapshots. Add variables, validation and safe
  handling of generated secrets.
- **Per-instance isolation of file-backed mounts.** Two instances of the same
  service block de-duplicate *named volumes* but still share the block's sidecar
  paths (both nginx instances mount `./html`).
- **Remote template catalog.** The `source: builtin|user|remote` field exists; a
  provider needs catalog signing, item versions, a local cache, a trust policy and
  a preview before import.

### Multi-host organization

- **Aggregated cross-host dashboard.** Every view today rebinds to one
  selected host; there is no single screen listing containers/images/
  volumes/networks across *all* reachable hosts at once. Close to a
  necessity once a deployment has more than a handful of hosts and
  containers — Arcane's top open feature request
  ([#634](https://github.com/getarcaneapp/arcane/issues/634), 38 👍) asks for
  exactly this, citing it as the reason people stay on flatter multi-agent
  tools. Read-only aggregation over data already fetched per host; no new
  connectivity model needed.
- **Host groups / tags.** Organize the flat host list into groups (env, team,
  region…) — filter the host switcher, scope alert rules and RBAC by group
  instead of per-host, and show a group rollup on the dashboard. Consider
  letting the same grouping also scope **stacks/projects**, not just hosts —
  a "project" as a saved, bounded set of stacks with its own optional
  resource quota, the way Dokploy scopes teams/environments. This is grouping
  for hosts we already directly reach; it's a smaller, self-contained step
  distinct from the multi-instance federation ("edge agents") idea below.

### Reverse proxy and ingress

- **Per-container domain + TLS.** An optional embedded reverse proxy
  (Go-native, e.g. Caddy-as-a-library — no new external process) that maps a
  domain to a container's host port with automatic Let's Encrypt issuance,
  so a deployed stack doesn't need a hand-rolled Traefik/nginx-proxy-manager
  sitting next to it. This is the single most-cited draw pulling people from
  Portainer/Dockge-class tools toward Coolify/CapRover, and it's asked for
  directly against Dockge too
  ([discussion #292](https://github.com/louislam/dockge/discussions/292),
  [#553](https://github.com/louislam/dockge/issues/553)). DC already does
  ACME/native HTTPS for *itself*; this is the same capability, scoped
  per-deployed-container instead of per-DC-instance.

### Multi-instance federation

Resolves the "edge agents" open question below: yes, worth building, mainly
for the **security** shape, not just the reach. Today's model is one DC
instance reaching *out* to every daemon it manages (TCP/TLS or SSH) — that
instance ends up holding credentials to everything at once, and it can't
reach a host behind a NAT/firewall it isn't allowed to open toward. Splitting
into a hub + lightweight agents inverts that:

- **Agent-initiated, outbound-only connections.** Each remote host runs an
  agent (same binary, a mode flag) that opens an outbound connection to the
  hub (websocket / long-poll) and never needs to be reachable itself — NAT
  traversal falls out for free, and no port needs opening on the agent side.
- **Smaller blast radius.** The hub never holds per-host SSH keys/TLS certs;
  an agent only knows its own host and its own pairing credential. Compromise
  one agent, and the attacker has that host — not the whole estate the way a
  stolen hub credential would today.
- **Pairing/enrollment.** A trust-on-first-use or one-time-code flow to
  attach a new agent to a hub, similar in spirit to the passkey pairing
  already in the product — needs a UX, not just an API.
- **Offline handling.** The hub must decide what "this agent hasn't checked
  in" means for monitoring (alert? just mark stale?) and for pending actions
  (queue until it reconnects? reject?).
- **Scope discipline:** this is aggregation and reachability, not workload
  orchestration — it does not decide *where* a container runs the way Swarm
  or Kubernetes would. The **aggregated cross-host dashboard** above is the
  natural first consumer once agents exist, and **host groups/tags** should
  be designed to apply uniformly whether a host is reached directly or
  through an agent.

Expect this to be a genuinely non-trivial implementation — new auth model,
new connection lifecycle, a pairing UX — but it's judged worth the cost for
the security property alone, independent of the NAT-traversal convenience.

### Smaller, well-scoped

- **Bulk operations — remaining scope.** Restart/stop/start and pull across a
  multi-selection all shipped (preview, confirmation, per-container
  success/failure summary; reuses the existing `containers` section write
  permission — bulk pull resolves container ids to images itself, so it never
  needs the `images` section). Still open: **per-host RBAC** granularity for
  bulk actions specifically (today a bulk request is scoped the same way any
  single container action is — the section-level grant, not a separate
  per-host bulk permission).
- **Log bookmarks** — save a time range plus filters, link it to an incident, share
  it with users who have the rights, export a small diagnostic bundle without
  secrets.
- **Log forwarding** — push matched/filtered lines to an external endpoint
  over WS or a webhook, for people who want lines to land somewhere other
  than reading the `.log` file by hand (the plain per-view download already
  ships). Bigger scope than the download button was, so it stayed separate.
- **Sub-path / base-path deployment** — run Docker Commander itself under a
  path prefix behind a reverse proxy (`https://host/dockercmd/`), not just
  on its own (sub)domain. Three independent asks for this shape across
  Arcane, Dockge (a `trustProxy`-style setting) and Portainer (whose own
  locale JSON loading breaks under a path prefix — a concrete failure mode
  to test for, not just a routing nicety). Touches the frontend's asset/API
  base path and cookie path, not just a config flag.
- **Archive/hide inactive stacks and projects** from the main list — filed
  independently twice against Portainer. Archiving only pulls its weight if
  there's also a way back: an archive view and a restore action, not just a
  one-way hide.
- **Native Slack / Teams / Discord notifications** as a UX layer over the generic
  webhook, which stays the base mechanism.
- **Host maintenance mode**, as distinct from `disabled`: monitoring continues,
  events are still recorded, notifications are suppressed or tagged, and write
  operations can optionally be blocked. Overlaps with silences above — design them
  together.
- **Standalone compose visualizer.** Paste or upload a bare `docker-compose.yml`
  — no host, no Docker connection needed — and render it as an architecture
  diagram: services classified by image name into a small icon set (database,
  cache, queue, reverse-proxy/web, generic fallback), with networks and
  `depends_on` drawn as edges. Reuses the Topology view's existing machinery
  (`TopoGraph.tsx`'s React Flow + d3-force layout, same node/edge look) rather
  than a new charting library or a hand-rolled SVG renderer — just a new node
  "kind" with a swapped icon. Needs the compose parser extended past today's
  `ServiceSpec` (name+image only) to also read networks/volumes/depends_on/ports.
  Scope v1 to a single static file, best-effort — full multi-file/`extends`/
  `profiles` resolution is more compose surface than a visualizer needs.
---

## 📦 Backlog

Plausible, deliberately not prioritized — different from 🧭 below, which is a
closed question. Revisit if the reasoning changes, not on a timer.

- **Docker Swarm support.** Real feature, real (shrinking) audience — usage
  keeps moving toward plain Compose on one side and Kubernetes on the other,
  and Swarm needs a genuinely new object model (services/tasks/nodes,
  `docker stack deploy`, overlay networks, Swarm-native secrets) rather than
  an incremental add. Not worth the build for a shrinking slice of users
  right now. Parked here instead of rejected outright, since "shrinking" can
  reverse.

---

## ❓ Open questions

Not decided yet — sitting here until they get triaged into 🔭 Open work,
📦 Backlog or 🧭 Deliberately not doing. Recorded so the question itself
doesn't get lost or re-asked from scratch.

- **Automation API + CLI** (`dockercmd host list`, `dockercmd project plan
  <name>`, `dockercmd project deploy <name>`, a declarative
  `dockercmd config apply infrastructure.yaml`…). DC has REST + MCP but
  nothing conventional for CI/CD, Ansible or shell scripts to call without
  speaking MCP. Not yet discussed with the user — surfaced from a separate
  product-direction note, not from the forum research above.
- **"Existing containers → Compose project."** Select running (e.g.
  `docker run`-created) containers and derive a compose project from their
  inspect data — images, ports, volumes, env, networks, restart policy,
  resource limits, labels — flagging secret-looking env vars and anonymous
  volumes along the way. A migration/onboarding path for exactly the
  "everything is `docker run` and label-discovered Stacks" users DC already
  targets.
  Overlaps with a narrower, independently-requested ask (Arcane, 24👍):
  **editing a standalone container's env/ports/volumes in place**, which
  needs recreation either way — worth deciding whether that's a cut-down
  version of this same flow or its own smaller feature before scoping
  either. Container Settings today already covers live rename and
  limits/restart-policy without recreation; this is specifically the
  recreate-required fields.
- **Compose Watch / development mode.** Expose `develop.watch` (sync-on-change
  / rebuild-on-change) as a live UI panel — positions DC as a local dev
  cockpit, not just an ops tool. A bigger scope question than the others
  here: worth deciding whether "local dev workflow" is a direction DC wants
  at all before designing it. Not yet discussed with the user.

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

---

## 🗺️ Working priority order

**Agreed 2026-08-27** — done, all shipped in 1.7.0:
- [x] Deployment plan / diff
- [x] Drift detection (view/reconcile/ignore shipped; "adopt" deliberately deferred, see below)
- [x] Deployment revisions and rollback (Projects only; CLI-discovered Stacks and remote seeded-volume snapshots still open, see below)
- [x] Portable recovery bundle
- [x] Policy checks before deploy
- [x] Volume data: trigger-and-status wrapper

**Agreed 2026-09-04** — final feature set for 1.7.0, security/RBAC first. Check
items off as they ship; revisit the order deliberately if priorities change,
don't just silently reshuffle it. (Original numbering from the candidate list
kept in parentheses for traceability.)

1. [ ] Per-session MCP token revocation (#20)
2. [ ] Project secrets (#10)
3. [ ] Alert delivery retry (#12)
4. [ ] Maintenance windows / silences (#6)
5. [ ] Controlled image updates (#11) — together with self-update auto-apply
   policy (#19), same poll/policy/audit/notify shape, one applied to
   workloads and the other to DC's own binary
6. [ ] Per-container domain + TLS / embedded reverse proxy (#13)
7. [ ] Network alerting / top talkers (#21)

**Not yet ordered**, full ranked candidate list (original numbering kept as-is —
this is everything not pulled into a bundle above, in descending priority, no
agreed commitment yet, revisit before reshuffling):

5. Incident timeline / correlation
9. External / synthetic checks
14. GitOps stack deploy
15. Lightweight webhook redeploy
16. OIDC / SSO
17. Aggregated cross-host dashboard
18. Host groups / tags
22. Monorepo-aware mapping (depends on #14)
23. Multi-instance federation
24. Smaller well-scoped items (bulk-action per-host RBAC, log bookmarks/forwarding,
    sub-path deployment, archive/hide stacks, Slack/Teams/Discord notifications,
    host maintenance mode, standalone compose visualizer)
25. Parameterized user templates / per-instance mount isolation / remote template catalog
26. Backlog: Docker Swarm support
27. Open questions to triage: automation API + CLI, "existing containers →
    Compose project", Compose Watch / dev mode
