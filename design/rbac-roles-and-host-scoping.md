# Design note — named roles & per-host scoping

**Status:** **implemented — all three phases have shipped** (§7 records how each
one landed, including where the implementation departed from this sketch). The
document is kept as the record of *why* the model looks like this, and of the
decisions in §9 that should not be re-argued. Sections 1–6 describe the tree **as
it was before implementation** and are deliberately not rewritten — read them as
history, not as a description of the code today.
**Why this document:** it touches the app's single authorization gate, every
host-targeting operation, and the MCP surface, so the **model** and the **blast
radius** were agreed before anything was implemented.

---

## 1. Where we are today

Everything below was read out of the current tree, not recalled.

### The model

| Concept | Today |
|---|---|
| **Roles** | Exactly two, as a string on the user: `admin` and `user`. `admin` bypasses *all* section and read-only checks. |
| **Permissions** | A flat per-user **list of sections** (`users.sections`, JSON). 13 sections: `dashboard, containers, projects, images, volumes, networks, topology, logs, events, alerts, hosts, registries, audit`. |
| **Read-only** | A single per-user boolean, applied to every section the user has. |
| **Feature flags** | An admin can disable sections **app-wide** (`disabled_sections`); a disabled section denies everyone except admins. |
| **LDAP** | Groups map to **sections** (union across matching mappings), recomputed on each login; the `admin` role is sticky. |
| **MCP tokens** | Narrow only: a **subset** of the user's sections plus an optional stricter `read_only`. No host dimension. |

### The gate

`(*Server).checkAccess(ctx, u, section, write)` in
`internal/api/access_middleware.go` is the **single source of truth**, used by both
the REST middleware and the MCP dispatcher. That's a real asset: one place decides
section grants and read-only, so the model below extends one function rather than
many.

Routes map to sections by URL prefix in `sectionForPath`; `""` means ungated and
`"__admin"` means admin-only (users, settings, LDAP, update, mcp-admin).

### How hosts are handled today — the important part

This is the finding that shapes the whole proposal:

- **`resolveHostID(r)` is a plain parse of `?host=N` with no authorization at
  all.** It has **61 call sites across 11 files** (`docker_handlers.go` 20,
  `image_handlers.go` 12, `volume_files_handlers.go` 7, `files_handlers.go` 6,
  `volume_handlers.go` 4, `container_lifecycle_handlers.go` 4, `stack_handlers.go`
  3, plus registry/exec/events/server).
- **`requireHostAccess` exists, but only for Projects** — 5 call sites, all in
  `project_handlers.go` — and its rule is coarse: targeting *any* non-local host
  requires the `hosts` section.
- **WebSocket subscriptions carry `hostId`**, but the authorization callback in
  `ws_handler.go` receives only the *channel*, which it maps to the `containers`
  section. The host is not checked.
- **MCP tools take `host_id` as an input** (`tools_read.go`, `tools_read_more.go`,
  `tools_control.go`) and are gated by section only.
- **The audit log has no host column** (`AuditEntry`: user, action, target,
  detail, IP).
- **The alert engine iterates every host by design** (`monitor.go`), with no user
  context — it is background work, not a user-scoped read.

> **Consequence, stated plainly:** today a non-admin with the `containers` section
> can act on **any** host by passing `?host=N` — including hosts they cannot see
> in the Hosts page, because that page is gated by the separate `hosts` section.
> Per-host scoping is therefore **not** tightening an existing check. It is
> introducing authorization where there currently is none.

That is worth knowing before estimating: the "roles" half of this item is a small,
additive change; the "per-host" half is the actual project.

---

## 2. What's being asked for, split in two

**(A) Named roles.** Assignable bundles of sections — e.g. *Operator*,
*Viewer* — instead of ticking 13 checkboxes per user. Mostly additive.

**(B) Per-host scoping.** A grant can be limited to specific hosts, so "may
restart containers" can mean "on staging, not production".

They are independent. **(A) can ship alone and is useful alone.** (B) is where the
risk and the work are. I recommend treating them as separate phases, not one
release.

---

## 3. Proposed model

### (A) Roles

```
roles          (id, name, description, builtin)
role_sections  (role_id, section, write)      -- write=false ⇒ read-only for that section
user_roles     (user_id, role_id)
```

- A user's **effective sections** = union over their roles, minus app-wide
  disabled sections. `users.sections` stays as a per-user *override/addition* so
  existing accounts keep working untouched.
- `admin` stays a **role on the user**, not a row here. It is the lockout
  safety-valve and deliberately keeps bypassing everything; folding it into the
  table invites a migration that locks the operator out of their own instance.
- Ship two built-in roles (read-only, not editable), mirroring how Templates
  already distinguishes built-in vs. user-defined: **Viewer** (all sections,
  read-only) and **Operator** (everything except `hosts`, `registries`, `audit`).

**Note this changes read-only from a per-user boolean to per-section.** That is
strictly more expressive, and the existing boolean maps onto it cleanly (all
sections read-only), but it *is* a semantic change to an existing field — worth an
explicit yes/no.

### (B) Per-host scoping

```
role_hosts     (role_id, host_id)      -- empty set ⇒ all hosts
```

or, if scoping should be per-user rather than per-role, `user_hosts`. **This is
decision D3 below** and it changes the UI shape, so it needs settling first.

Semantics, chosen for backwards compatibility:

- **An empty host set means "all hosts".** Every existing user therefore keeps
  exactly today's reach after migration. The alternative (empty = none) is safer
  by default but breaks every account on upgrade.
- The **local host (`0`)** is always in scope. Treating it as scopeable would
  make a fresh single-host install able to lock itself out.
- Scoping is **enforcement plus filtering**: denying `?host=N` is not enough if
  the dashboard still aggregates that host's containers. Every aggregate read has
  to filter (see the impact map).

### The gate becomes

```go
checkAccess(ctx, u, section, write)                    // today
checkAccess(ctx, u, section, write, hostID int64)      // proposed
```

Threading a host through the existing single gate keeps one source of truth. The
REST side gets it at the `resolveHostID` chokepoint — which is the one piece of
luck here: 61 call sites funnel through **one** function, so the enforcement point
can be `resolveHostID` itself (renamed to something that makes the check obvious,
e.g. `authorizedHostID(r, section)`), rather than 61 hand-edited call sites.

---

## 4. Impact map

| Surface | What has to change | Size |
|---|---|---|
| `access_middleware.go` | `checkAccess` gains a host argument; `permissions` middleware needs the host, which means the section→host resolution happens before the gate | small, but it's the crux |
| `resolveHostID` (61 sites) | becomes the enforcement point; returns 403 instead of a parsed id | small if centralised, huge if not |
| `project_handlers.go` | `requireHostAccess` (5 sites) folds into the general mechanism; its coarse "non-local ⇒ needs `hosts`" rule is replaced | medium |
| Aggregate reads | dashboard, topology, alerts feed, events, stacks list, hosts list — must **filter** to in-scope hosts, not just deny | medium, easy to miss |
| `ws_handler.go` / `ws/hub.go` | the authorization callback must receive `hostId` from the subscribe frame | small |
| MCP (`internal/mcp`, `internal/api/mcp_*`) | tools take `host_id`; the dispatcher must gate on it. Token scope should gain a host subset (already a NEXT.md want) | medium |
| `store` | new tables + migrations; `users.sections` retained | medium |
| Audit | add a host column so a scoped action records *where* it happened | small |
| LDAP | groups currently map to **sections**; with roles they should map to **roles** (and possibly host scopes). Needs a migration story for existing mappings | medium |
| UI | role management page (Templates-style built-in vs. user), host-scope picker, user form rework | medium–large |
| Alert engine | stays global (background, no user context). But per-user *views* of the alert feed need filtering | small |

**Not in scope and worth saying so:** the alert engine watching all hosts is
correct and shouldn't become user-scoped; and `admin` continues to see everything.

---

## 5. Security invariants, and the tests that must prove them

Per the repo's own rule (adversarial tests for any new authorization surface),
this work is not done until pentests assert:

1. **No bypass via `?host=`** — an in-scope section plus an out-of-scope host is
   403, for every one of the 61 call sites' route families (parameterised test
   over routes, not one test per handler).
2. **No bypass via WebSocket** — subscribing to a container on an out-of-scope
   host is refused.
3. **No bypass via MCP** — a tool call with an out-of-scope `host_id` is an error,
   including when the token itself was minted before the scope narrowed.
4. **No leak via aggregates** — dashboard/topology/events/alerts responses contain
   nothing from out-of-scope hosts (this is the one most likely to be missed, and
   it leaks names, ports and images rather than granting actions).
5. **No privilege escalation through roles** — a non-admin cannot create or edit a
   role to widen their own reach, nor assign themselves a role.
6. **Migration cannot silently widen access** — a user's post-migration effective
   permissions are a subset-or-equal of their pre-migration ones.
7. **No lockout** — the last admin cannot be scoped or de-roled into being unable
   to administer the instance.

---

## 6. Migration

- Existing users: keep `users.sections` as-is, no roles assigned, empty host scope
  ⇒ **identical behaviour**. Roles are opt-in.
- LDAP group→section mappings keep working; group→role becomes an additional
  mapping kind rather than a replacement (a hard switch would silently change
  access on next login, violating invariant 6).
- The read-only-per-section change (if accepted) maps `users.read_only = true` to
  "every granted section read-only".

---

## 7. Suggested phasing

1. **Phase 1 — roles only.** ✅ **Shipped.** Tables, effective-section computation,
   built-in Viewer/Operator, role management UI, LDAP group→role. No host
   dimension. Ships independently; no change to how hosts are reached.

   One rule was decided while implementing group→role, refining D6: roles become
   directory-driven only once **at least one mapping actually grants a role**.
   Gating on "any mapping exists" (as sections do) would have stripped
   hand-assigned roles from every install whose mappings predate this release —
   exactly the silent access change invariant 6 forbids. The cost is that emptying
   the roles from *every* mapping stops role sync rather than revoking; removing a
   role from *one* mapping still revokes normally.
2. **Phase 2 — host scoping enforcement.** ✅ **Shipped.** Host-aware
   `checkAccess`, WS, MCP (tools and token host scope), audit host column, plus
   invariants 1, 2, 3, 5, 6 and 7 of §5 as pentests.

   One thing landed differently from the sketch above. The enforcement point is
   **not** `resolveHostID`; it is the `permissions` middleware, which every
   host-targeting route already passes through. `resolveHostID` stayed a plain
   parse. Enforcing in the middleware covers the ~60 call sites without editing
   them, and — more importantly — covers routes added later, which is exactly the
   failure mode that left `?host=` unauthorised in the first place. The two places
   a host is named outside the URL still need an explicit call: the WebSocket
   subscribe frame, and a managed project's own `host_id`.
3. **Phase 3 — aggregate filtering.** ✅ **Shipped.** Invariant 4 is now asserted.

   It turned out smaller than this note feared, and for an instructive reason: most
   of the "aggregates" aren't aggregates at all. Topology, the events feed, disk
   usage, stats overview and published ports are each **per-host reads taking
   `?host=`**, so phase 2's middleware check already covered the ones that map to a
   section. What actually leaked was narrower and sharper:

   - **Three dashboard routes map to no section** (`/api/stats/overview`,
     `/api/system/df`, `/api/stats/ports`), so the middleware returned before it
     looked at the host. Ungated means "no section required", not "any host you
     like" — a named host now has to be one the caller's grants reach *somewhere*
     (`Store.ReachableHosts`), since there is no single section to check against.
   - **List endpoints**: hosts, projects, the alert feed and the audit log are now
     filtered by the same predicate. The alert feed's **unread count** is computed
     after filtering — a badge that still counted hidden events would announce them.
   - **Metrics history** was the one real gap. The store keys by container id, so
     knowing an id was enough. `ContainerStat` already carried `HostID` and
     `recordHistory` was dropping it; `history.Sample` now keeps it and the series
     is authorised against it. An unrecorded id is **unknown**, not local, so ids
     can't be probed.

   Left deliberately global: the **alert engine**, which watches every host as
   background work with no user context. Documented in `docs/users.md`.
   *Could* land with phase 2, but it is the fiddliest and most leak-prone part, so
   splitting it keeps phase 2 reviewable.

Phase 2 without phase 3 is a defensible intermediate state (actions are
authorized, some read aggregation is still broad) **as long as that is documented**
rather than implied to be complete.

---

## 8. Decisions needed before implementation

- **D1 — scope of ambition.** Roles only (phase 1), or roles + host scoping?
- **D2 — read-only granularity.** Keep the per-user boolean, or move to
  per-section `write` as proposed?
- **D3 — where does the host scope live?** On the **role** (roles are
  "Operator on staging") or on the **user** (a user has roles *and* a host list)?
  Per-role is cleaner to reason about; per-user needs fewer roles in practice.
- **D4 — default for an empty host set.** All hosts (backwards compatible, chosen
  above) or none (safer, breaks every existing account on upgrade)?
- **D5 — built-in roles.** Are Viewer + Operator the right two, and should they be
  read-only like built-in templates, with **Duplicate** to customise?
- **D6 — LDAP.** Add group→role alongside group→section, or migrate to
  group→role only?
- **D7 — MCP token host scope.** Include in phase 2, or defer (NEXT.md currently
  lists it separately)?

Answering **D1–D4** is enough to start phase 1.

---

## 9. Decisions taken

| # | Decision | Notes |
|---|---|---|
| **D1** | **Phases 1 and 2.** Roles *and* host-scoping enforcement. **Phase 3 (aggregate filtering) is deferred.** | Superseded: phase 3 shipped shortly after, so the limitation below is historical. |
| **D2** | **Per-section `write`.** `role_sections` carries a write flag; `users.read_only = true` maps to "every granted section read-only", so no behaviour changes on migration. | |
| **D3** | **Host scope lives on the role.** A role is "Operator on staging". | Cleaner to reason about in the UI and in the audit log. |
| **D4** | **An empty host set means all hosts.** Backwards compatible: after migration every user keeps exactly today's reach. | Accepted risk: an unset scope is *no* restriction. Phase 1 UI should say so where a scope is empty. |
| **D5** | Built-in **Viewer** (all sections, read-only) and **Operator** (all except `hosts`, `registries`, `audit`), read-only with **Duplicate** to customise — matching how Templates already separates built-in from user-defined. | |
| **D6** | LDAP **group→role alongside** group→section, not replacing it. | A hard switch would silently change access on the next login, violating invariant 6. |
| **D7** | MCP token **host scope lands in phase 2**, since phase 2 is in scope. | |

### The deferred-phase-3 limitation, stated explicitly

> **Historical.** Phase 3 shipped, so this limitation no longer holds and
> invariant 4 is asserted. Kept because the *discipline* is the point: an
> intermediate state is defensible only while it is stated in these terms.

With phases 1–2 shipped and phase 3 not, the state was:

> **Actions are authorized per host; some aggregated reads are not.** A user
> scoped to one host cannot start, stop, exec into or deploy anything on another
> host — but aggregate views (dashboard, topology, events feed, alert feed) may
> still surface *names, images, ports and event text* from hosts outside their
> scope. That is an information leak, not an action bypass.

It landed in `docs/users.md` and the release notes in those terms. Shipping it
silently would have let an operator believe scoping was complete when it was not —
which is worse than not having scoping, because it invites relying on it.

Pentest invariant 4 (§5) was the entry criterion for phase 3 and now passes with
the other six.

### What the sweep afterwards found — the part worth carrying forward

Per-host authorization did not finish with phase 3. Three MCP tools —
`preview_deploy`, `alert_delivery` and `acknowledge_alert` — were later found
checking the right *section* against **host 0** while acting on a record belonging
to some other host, and every existing coverage test passed while they did.

The common shape: a tool takes an **integer id and no `host_id`**, so the host is
implied by the record and nothing in the arguments names it. "You need the id
first" is not an access control when ids are sequential integers. The fix that
matters is not the three patches but
`internal/mcp/tool_host_scope_coverage_test.go`, which detects that shape from the
advertised tool list and fails on any such tool it has no fixture for — so a new
one cannot be added without somebody deciding how it is host-scoped.

The mirror-image mistake is worth naming too: scoping the alert routes, demanding
the `hosts` section *as well* looked like caution and was a different bug. Host
reach is derived from grants across all sections, so a user whose `alerts` grant
already reaches a remote host would have been shown an alert they could neither
acknowledge nor trace. Over-tightening is not a safe default, and it now has its
own test.
