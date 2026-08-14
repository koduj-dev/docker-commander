# Changelog

All notable changes to Docker Commander are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

### Added
- **Bulk restart/stop for containers.** Select several containers on the
  Containers page and restart or stop them together: a preview lists exactly
  which containers are targeted, the app's own confirm dialog gates the action
  (never one click), the calls run with bounded parallelism, and a
  per-container success/failure summary follows — not just a single toast.
  Reuses the existing `containers` section write permission; no new
  permission model. Pull and per-host RBAC scoping for bulk operations are not
  part of this pass — see `NEXT.md`.
- **Windows native service.** `--install-service` now registers dockercmd as a
  real Service Control Manager (SCM) service on Windows — auto-restart on
  crash, `services.msc`/`sc query` visibility — instead of failing with SCM
  error 1053 (a plain console exe never speaks the service protocol). The
  Task Scheduler installer (`deploy/install-windows.ps1`) remains available as
  a dependency-free alternative.
- **Collapsible sidebar groups.** Click a group heading (Workloads, Storage,
  Network, Observability, System) to fold/unfold it; state is remembered per
  browser. A collapsed group holding the current page auto-expands, so
  navigating there directly never hides where you are.

## [1.6.0] — 2026-08-07

### Security
- **Backups no longer carry symbolic links, and say what they skipped.** A link in
  the data dir was stored as a link, while its contents were never included —
  `filepath.Walk` does not follow links — so anyone who had pointed `projects/` at
  another disk held a backup that quietly omitted it. Links are now skipped
  outright and **named in the output**, next to the total size of what did go in,
  because the moment the backup is taken is the only cheap time to learn this.

  Restoring an archive that contains a symlink entry is refused. A symlink is a
  write path out of the data dir, and the class of bug that exploits one has
  already shown up here once; not creating them at all is cheaper than jailing
  them correctly. (Hard links are unaffected: a hard link *is* the file, so its
  data is backed up like any other file's.)

- **An MFA challenge is good for exactly one attempt.** After a correct password
  the server hands out a short-lived token, and the code is checked against it —
  but the token stayed valid for its full five minutes, so one password entry
  funded a burst of guesses. The token is now spent on the *first* attempt, right
  or wrong: a rejected code costs another password round trip, which is the
  expensive half. The rate limiter bounds guesses per window; this bounds them per
  password entry.

  Signing in reflects that — a rejected code returns you to the password step,
  rather than leaving you typing into a form that can no longer succeed.

- **Pairing a new authenticator now asks for your password.** Re-pairing replaces
  the second factor, and it needed only a session — so any session takeover (a
  shared machine, a token pasted into a URL) became a permanent authenticator
  takeover: the attacker pairs their own device, satisfies 2FA from then on, and
  the owner's app quietly stops working. The password check burns the same
  rate-limit budget a login does, so the endpoint can't be used as a password
  oracle. A first-time enrolment is unchanged — there is no factor to replace, and
  the first-run wizard walks straight into it.

- **Changing a password now ends the sessions issued before it.** A JWT is
  self-contained, so nothing about a reset reached the copy a browser or a script
  already held: an attacker whose access prompted the reset kept it for the rest of
  the token's twelve hours, handed to them by the very act meant to revoke it. Each
  account carries a session generation, tokens carry the one they were minted with,
  and a mismatch is refused — so the change takes effect on the next request rather
  than at the token's expiry. A deleted account's tokens stop working the same way,
  including on the routes that carry no section and therefore never reloaded the
  user.

- **A `.tar.gz` upload can no longer be a decompression bomb.** The extraction cap
  was applied only on the `.zip` branch, while the comment claimed it covered
  everything — so a ~10 MiB gzip of repetitive data expanded to ~10 GiB written
  into the container, i.e. onto the host's filesystem, where it fills the disk out
  from under every other container. Every branch is capped now, and the refusal is
  loud rather than a silent truncation.

- **CRLF in an alert rule's name no longer injects mail headers.** The rule name
  reaches the `Subject:` line, so a name containing a line break could append a
  `Reply-To:` pointing elsewhere or a `Content-Type:` that turns the alert into
  HTML the recipient's client renders. The envelope was never injectable
  (`net/smtp` refuses CR/LF in `MAIL`/`RCPT`), so this was header and content
  forgery — enough on its own. Line breaks in header values are flattened to
  spaces, so the rule name still reads as itself.

- **A file upload is no longer buffered whole in memory.** The tar header needs
  the size up front, and the answer had been to read the entire body with a 4 GiB
  ceiling — one request could drive the process into gigabytes of RSS (`io.ReadAll`
  doubles as it grows) and a few concurrent ones could OOM-kill it on an ordinary
  host. Uploads now spool to an unlinked temp file and stream out as tar, with a
  deliberate 2 GiB ceiling instead of an accidental 4.

- **A TOTP code can only be spent once.** It was validated, not consumed, so it
  kept working for its whole ~90-second window — one code observed over a shoulder,
  captured by a phishing proxy or screenshotted by malware could be used more than
  once, and the challenge token it satisfies lives for five minutes. The time step
  a code came from is now recorded and anything at or below it is refused, with the
  same answer a wrong code gets so a replay can't be told from a typo.

- **First-run setup can no longer be raced into two admins.** It was check-then-act
  — count the users, validate, insert later — so two requests arriving together
  both passed the count and both created an administrator. The condition now lives
  in the INSERT, where SQLite settles it. Small window, permanent payoff, and a
  fresh instance is precisely the one nobody is watching yet.

- **The login rate limiter no longer grows without bound.** Entries were removed
  only when the same key returned, so every distinct client address left one behind
  for good — a botnet, an IPv6 /64, or simply uptime. The same limiter backs the
  MCP OAuth throttle, whose keys are unauthenticated client IPs. Expired windows
  are now swept, in bounded batches, on the path that creates them.

- **The dashboard's data endpoints now require the `dashboard` section.**
  `/api/stats/overview`, `/api/stats/ports` and `/api/system/df` were deliberately
  ungated "for the shell", which meant an account with **no sections at all** could
  still enumerate every running container with its resource usage, read the host's
  disk breakdown, and — through the port map — have the server dial every published
  port and fingerprint what answered. They were host-authorized already; they are
  section-authorized now too, the way MCP has always treated them.

  The port scan additionally counts as a **write**: it opens a TCP connection to
  every published port, the same category as an image vulnerability scan, so a
  read-only account cannot launch it — or repeat it.

- **Signing out no longer leaves alerts behind for the next user.**
  `resetAlertStream()` existed and was documented as "call on logout" — but had no
  call sites, and a logout is an SPA navigation rather than a page load. So on a
  shared browser the next person to sign in inherited the previous user's unread
  badge and had their pending alerts replayed as toasts, naming containers on hosts
  the new user may not be scoped to. Session teardown now lives in one function, so
  the next thing that caches per-user data gets cleared by construction.

- **A crafted backup could write anywhere on the filesystem during `--restore`.**
  The jail refused a symlink whose target climbed out with `../`, but an
  **absolute** target slipped through: `filepath.Join(dir, "/etc/cron.d")` is
  `dir/etc/cron.d` — Join treats an absolute path as a relative component — so the
  validated form looked safely inside while `os.Symlink` then stored the original
  target. A following file entry was opened with `O_CREATE|O_TRUNC`, which follows
  the link, and the bytes landed wherever it pointed, as whoever ran the restore.

  Reproduced before fixing (a two-entry archive wrote a file outside the data dir
  and `Restore` returned no error), and the guard is mutation-tested: removing it
  puts the escape back. The existing test only covered the relative spelling of the
  same class, which is how this survived.

  Consequence worth knowing: an archive containing a symlink with an absolute
  target is now **refused entirely** rather than partially restored. Such a link
  was already a bad arrangement — the backup stores the link, not what it points
  at, so its contents were never captured.

- **Project routes addressed by id now authorize against the project's host.**
  Every `/api/projects/{id}/…` route except deploy/down/restart resolved the id and
  acted on it without asking which host the project targets. The permissions
  middleware could not have caught it: with no `?host=` it authorizes against host
  0, which every grant satisfies. So a role scoped to staging could read a
  production project's compose file and sidecars — credentials, in practice — and
  rewrite them. It could not deploy, but the next deploy by someone who could would
  run what it left behind.

- **The same hole existed on `/api/hosts/{id}`**, and the sweep below is what
  found it: those routes name the host in the *path*, so a role scoped to one host
  could rename, disable or delete another — and pin a new **SSH host key** for it,
  which is the one operation the trust-on-first-use design exists to make
  deliberate.

- **A systemic sweep now enumerates this class.** `TestEveryRecordAddressedRouteDecidesItsHost`
  walks the real router and fails on any record-addressed route without a host-scope
  decision, and `TestPentestRecordRoutes_OutOfScopeHostIsRefused` drives all 23 of
  them against an out-of-scope record. MCP has had this since the equivalent bugs
  were fixed there; REST did not, which is exactly why these two survived.

  Out of reach answers **404**, like missing, so the id space can't be walked to
  learn what exists elsewhere. Visible-but-read-only still answers **403** — an
  operator who can see a project shouldn't be told it vanished.

- **The 2FA step is rate limited and audited.** Verifying a TOTP code had no
  throttle, no attempt counter and no audit entry on failure, so an attacker who
  already had the password — the exact situation the second factor exists for —
  could guess six digits at line speed and leave no trace. Failures now burn the
  same budget the password step does, keyed on the client IP **and** on the
  account, so rotating source addresses doesn't buy a fresh one.

  A correct password no longer clears the failure window either. It used to reset
  it before the login was finished, which meant an attacker could refresh their
  own budget between guesses simply by authenticating again.

- **The localhost 2FA exemption no longer applies to proxied requests.** It is
  meant for someone sitting at the machine, and a proxy cannot vouch for that. A
  reverse proxy on the same host is itself loopback, so *every* request through it
  presented a loopback peer; and unless the operator set
  `$proxy_add_x_forwarded_for`, nginx forwards the client's own header untouched,
  so a remote client could simply claim `127.0.0.1`. Either way the exemption is
  now refused — the address still resolves for rate limits and audit, but it
  cannot skip the second factor. The documented nginx block sets both forwarded
  headers now.

- **The session cookie is marked `Secure` when the connection is HTTPS** — native
  TLS, or a trusted proxy reporting `X-Forwarded-Proto: https`. Without it, one
  plaintext request to the same host hands over twelve hours of Docker control;
  `SameSite=Strict` constrains cross-site requests, not the scheme. It stays off
  for a plain-HTTP loopback install, where the browser would otherwise refuse to
  send the cookie back at all.

- **A failed webhook could write its own URL — and any token in it — into the
  alert delivery record.** The record deliberately stores a webhook's *name and
  host* rather than its URL, because webhook URLs routinely carry a secret in the
  path or query and the record is readable by anyone holding the `alerts` section.
  Transport failures slipped past that: `net/http` wraps them in a `*url.Error`
  whose message embeds the full request URL, so a refused connection stored the
  secret verbatim. The cause is now recorded without the URL, which keeps the
  diagnostic value ("connection refused") and drops the credential. Found in review
  before the feature was ever released.

- **Removing an MCP OAuth client now revokes its access immediately.** Deleting a
  client purged its authorization codes and refresh tokens, which stopped it
  obtaining a *new* access token — but an access token is a **signed** credential,
  so nothing about deleting a database row reached the copy a connected tool
  already held. It kept working until it expired: up to 15 minutes of access after
  an admin pressed Revoke. Access tokens are now bound to the client that was
  issued them (a `dc_cid` claim) and every call requires that client to still be
  registered. The window was always bounded, so this is hardening rather than a
  hole — but "revoked" should mean now, and the admin dialog said so while the
  behaviour didn't. Tokens minted before this carry no binding and simply expire
  within one lifetime; rejecting them would have forced every connector to
  re-authorize on upgrade to buy at most fifteen minutes.

- **The stack editor's backup could be redirected through a symlink.** Saving a
  CLI-discovered stack's compose file keeps the previous version beside it as
  `<name>.dc-prev`, and that backup was written with a plain write — which follows
  a symlink already sitting at the destination. Anyone able to create files in the
  stack's directory (a deploy or CI account, *not* necessarily one with Docker
  access) could pre-place `compose.yml.dc-prev` as a link to, say, `/etc/cron.d/`
  and have Docker Commander — commonly root — write through it, with content they
  also controlled, since the backup is the previous compose file. Backups are now
  written with a temporary file plus a rename, which replaces a symlink instead of
  following it; the SSH path does the same with `mktemp`, where `cat >` and `cp`
  had the same weakness. Found in a review of the release, before any tagged build
  shipped it.
- **Redeploying a stack skipped the containment check that saving enforced.** A
  compose path outside the stack's working directory was refused for writes but
  still handed to `docker compose up -d`, which would deploy whatever definition
  sat there. Both operations now answer the same question in one place, which also
  means the UI stops offering an editor whose Save could only ever fail. Exploiting
  it required direct Docker API access (to set the labels), which is already
  root-equivalent — so this hardens a boundary rather than closing a path into the
  app.

- **The SMTP config is now admin-only, and lives under Settings → Email.**
  It is a *single instance-wide* outbound mail relay with a stored credential, but
  it was gated by the **alerts** section — so any non-admin with write access to
  alerts could repoint the whole installation's mail at a server they control and
  receive its notifications. (The password was never returned by the API, so this
  was about redirecting delivery, not reading the secret.) `/api/smtp` and
  `/api/smtp/test` now require admin.

  **This narrows an existing permission:** someone who managed alerts *including*
  their e-mail delivery will now need an admin to configure the relay. The rest of
  the alerts surface — feed, rules, webhooks — is unchanged for them, and alert
  rules can still opt into e-mail.
- **Raw `inspect` no longer readable without the owning section.**
  `GET /api/inspect/{kind}` returns the **raw** Docker inspect payload, which for a
  container includes `Config.Env` — database passwords, API keys. It was ungated,
  so **any signed-in account** (no sections granted at all, even read-only) could
  read the environment of any container on **any** host via `?host=`. It is now
  gated by the section that owns the kind: container→`containers`, image→`images`,
  volume→`volumes`, network→`networks`, with an unknown kind failing closed onto
  `containers`. This matches how the UI already uses it — the Inspect dialog only
  opens from pages those sections already guard — so nothing legitimate changes.
- **MCP tokens: a scope that narrowed to nothing produced an *unrestricted*
  token.** An empty stored scope means "inherit the owner's rights", so requesting
  a scope consisting only of sections the account doesn't hold was silently turned
  into a token with the owner's **full** access — asking for less returned more.
  Such a request is now refused. Token scopes are also matched against
  **effective** sections, so access granted through a role can be scoped to (it
  would otherwise have been dropped, hitting the same widening path). This was
  reachable before roles existed, by requesting only ungranted sections.
- **Remote Projects now support bind mounts.** Deploying a managed project to a
  remote host previously refused any compose file with a host-path bind mount,
  because the remote daemon can't see Docker Commander's data dir. Each bind
  whose source lives **inside the project folder** is now copied to a named
  volume on the target host (`dcseed-<project>-<hash>`, labelled with the
  project) and the mount is repointed at it via a generated compose override, so
  sidecar configs and scripts work remotely — including single-file mounts like
  `./nginx.conf:/etc/nginx/nginx.conf`, which mount out of the volume by
  subpath. `read_only` is preserved and the project's own named volumes are left
  untouched. The copy is a **snapshot taken at deploy time, not a live mount**:
  editing the files needs a redeploy, and writes inside the container stay on the
  remote host. The deploy output says so explicitly.
- Bind mounts pointing **outside** the project folder (e.g. `/etc/localtime`,
  `/var/run/docker.sock`, or anything reached through a symlink out of the
  folder) are still **refused** on a remote deploy, now with a message naming
  them: they address paths on the remote host, which won't be mounted blind.

- **Remote deploys can opt into host paths.** Bind mounts pointing outside the
  project folder are still refused by default, but a project's Settings now has
  **Allow host paths** to mount them from the remote host's own filesystem
  instead (contents are whatever exists there; nothing is copied, and the deploy
  output names them every time). Enabling it needs **write access to the Hosts
  section** — it is authority over the host, not the project — and is audited;
  turning it back off needs only project access, so a restricted user can always
  close a hole they cannot open.
- **Deleting a project offers to remove its seeded volumes.** A remote deploy
  leaves `dcseed-*` volumes on the target host, which used to accumulate
  silently. The delete flow now lists them and asks, since they hold data;
  declining keeps them for a later redeploy. Only volumes carrying that
  project's seed label are touched.

### Added
- **Kill a container, and build args in the image build dialog.** Both were
  supported by the API and named in the documentation, and neither had a control —
  so the docs described the backend rather than the app. Kill sends SIGKILL and
  goes through the app's confirm dialog, never one click: it is for a container
  that has stopped responding to Stop, and the difference between the two is
  exactly what a confirmation is for. Build args take one `KEY=VALUE` per line,
  with a note that they can end up in the image's history and are the wrong place
  for secrets.
- **Sign in with a passkey alone.** The login screen offers it next to the password
  form: no username, no password — the browser finds a credential for this server
  and that assertion is the whole login.

  This is defensible only because such a passkey is *itself* two factors: possession
  of the authenticator, and the PIN or fingerprint that unlocks it. That second half
  is **user verification**, and it is demanded of the browser *and* checked on the
  assertion that comes back — an authenticator may ignore the request, so the answer
  is what decides. Without it the key proves possession only, and that is refused
  with an explanation rather than a generic failure.

  **Off until you ask for it**, in *Profile → Security*, and turning it on costs
  your password. A passkey you paired as a *second* factor was accepted while the
  password still stood in front of it; making it the whole login changes what the
  account rests on, and that is not a change to make on your behalf because the app
  was updated. It matters most for a passkey that **syncs** between your devices:
  the PIN or fingerprint can then be satisfied wherever that credential reaches, so
  the account effectively rests on the platform account it syncs through. Reasonable
  for many people, wrong for others — hence a choice.

  **The password still works.** This is an addition, not a replacement, and
  deliberately so: this app gives admins no way to reset someone else's second
  factor, so if a passkey were the only way in, a lost phone would be a lost
  account. Signing in with a password and a second factor remains a valid route, and
  is the recovery path.

  Newly paired passkeys ask to be *discoverable* so the browser can offer them
  before anyone has said who they are. Hardware with no room to store one keeps
  working as a second factor and simply will not appear for passwordless sign-in.

  Accounts backed by **LDAP cannot use it**: the directory is their authority — what
  they may do, whether they are still enabled — and a passkey answers none of that.

- **`dockercmd --reset-password <user>`** — a way back into an instance whose
  password is lost. An admin can reset someone else's from the UI, but the *last*
  admin locking themselves out had nobody to ask, and this app deliberately gives
  nobody a way to reset another account's second factor. That state used to be
  terminal.

  It is local, against the data directory, and never over HTTP — the server does
  not need to be stopped. The password is
  read from the terminal rather than taken as an argument, because an argument
  lands in shell history and in `/proc/<pid>/cmdline` where any local user can read
  it. Every browser session for the account is ended — a reset that leaves a stolen
  session alive is the opposite of what someone reaching for this needs — and the
  reset is written to the audit log. API and MCP tokens are *not* revoked, and it
  says so: they are not sessions, and somebody resetting after a compromise needs
  to know to review them too.

  **The second factor is left alone.** Whoever holds the files can bypass it
  anyway: the token signing secret is a row in the same database, so anyone who can
  run this command could already mint an admin session directly. That is also why
  offering it costs nothing — the capability is the filesystem, not the command.
  But it should not do it for them, so "nobody resets another account's second
  factor" stays true.

- **Passkeys.** *Profile → Security → Add a passkey* pairs whatever the device
  already has — fingerprint, face, PIN, or a plugged-in security key — as a second
  factor, and sign-in then offers it instead of typing a code.

  Two properties make it worth having over TOTP. The private key never leaves the
  device's secure hardware, so there is nothing on our side to steal and nothing on
  yours to read out to a caller. And the signature covers the **origin** the browser
  saw, so a page that looks exactly like this one gets an assertion it cannot use —
  the part of phishing resistance a server can actually verify.

  It is an option, never a requirement: a passkey needs a secure context (HTTPS or
  `localhost`), so on a plain-HTTP deployment the button explains its absence rather
  than failing when pressed. Accounts can hold both kinds at once; sign-in offers
  whichever exist. A passkey counts as a second factor everywhere the app already
  counted them — including the rule that the last one cannot be removed.

  A signature counter that goes backwards is refused and audited: it means the key
  answered from two places, and the honest device and the copy are indistinguishable
  from here.

  Passkeys are offered only where a browser will actually accept one: HTTPS, or
  `localhost`. An **IP address is not a relying party** — no browser allows a
  passkey at `https://192.0.2.10/` or `http://127.0.0.1:8470/` — so the option says
  why it is missing there instead of failing when pressed. Reach the server by a
  hostname to use them.

  `mfaEnabled` joins `totpEnabled` in the profile and the admin user list, because
  the two stopped being the same question: an account protected by a passkey alone
  reported as having no second factor.

  **Pairing is authorised by the write that creates the factor, not by a check
  before it.** Adding a factor to an account that already has one needs the
  password; adding the first needs nothing, because there is nothing yet to protect.
  Those are decided minutes apart, so a half-finished enrolment carries the
  authority it was begun under, and an unauthorised one is admitted by the insert
  itself, conditional on the account still having no factor. Anything else leaves a
  gap the client controls — the WebAuthn library reads the request body, and a
  request whose body arrives slowly holds a handler open across it.

  In practice: start pairing an authenticator, pair a passkey in another tab, and
  the first flow asks you to begin again with your password rather than reporting a
  wrong code.

- **An account can hold several authenticators.** *Profile → Security* lists every
  paired one by a name you choose, with when it was added and last used, and lets
  you add or remove them. Pairing used to *replace*: the phone in your hand stopped
  working the moment you set up a new one, which made "add my tablet too" impossible
  and losing a device a support call.

  **The last one cannot be removed** — 2FA is mandatory here and there is no admin
  reset, so an account with no authenticator is one that cannot sign in. Pair the
  replacement first.

  Removing asks for your password, exactly as pairing does: both change what it
  takes to sign in as you, and a stolen session must not be able to strip an
  account's factors one at a time.

  The replay guard is now **per authenticator**. It used to be one watermark for the
  account, which with two paired devices would have let a code from one refuse the
  same time step on the other.

  An account holds at most ten, and pairing the same secret twice is refused by the
  database itself: two rows sharing a secret would give that authenticator two
  independent replay watermarks, so each of its codes could be spent twice.

  Note what this does *not* do: pairing no longer implicitly revokes anything. If
  you are replacing a lost phone, remove its entry as well — adding the new one is
  no longer enough.

  Existing installations migrate on start, in one transaction: the single stored
  authenticator becomes the first entry in the list, keeping the same secret and
  the same replay watermark, so nobody has to re-pair. Every legacy secret is
  cleared from the old column in the process — a live secret that nothing reads and
  nobody can remove is a credential nobody knows exists.

- **Removing a factor, pairing one, and spending a code are each atomic.** Found by
  an independent adversarial review of the change above, which reproduced all three:
  sixteen parallel confirmations of one enrolment produced **five** authenticators
  sharing a single secret (and therefore five replay watermarks); two concurrent
  removals both passed the "this is your last one" check and left the account with
  **none** — which is not a lockout but 2FA silently switched off, since it is
  derived from whether any factor exists; and one TOTP code minted **two or three**
  sessions when presented simultaneously, because the watermark write reported
  "nothing to update" as success. The count now lives inside the `DELETE`, pairing
  claims its enrolment with a compare-and-swap inside a transaction, and a burn that
  moves no row is an error. Each has a concurrency test that fails without the fix.

  Step-up password checks are bucketed **per session**. Per address (the original)
  meant anyone holding a session could stop everyone behind that address from
  signing in for fifteen minutes. Per account — the first attempt at fixing that,
  and caught by a second review round — merely aimed the same weapon at the victim:
  a stolen session could burn the budget every fifteen minutes, and the owner's
  *correct* password would then be refused for exactly the two things they need to
  recover (removing the attacker's authenticator, pairing a replacement) while
  logins kept working, so nothing looked broken. Per session, the stolen session
  spends its own budget and minting another needs the password.

  A spent budget now answers **429**, not "password required" — telling someone
  their own password is wrong while they are recovering an account is both false
  and cruel.

  A session token carrying no `jti` is refused outright. `jti` is optional in a
  JWT, so a signed token without one parsed cleanly and arrived with an empty id —
  and both the revocation row and the per-session rate-limit bucket key on it, the
  second of which would have collapsed back to per-account. Minting such a token
  needs the signing key, so this makes a property that held by accident hold by
  construction.

- **See what is signed in as you, and end it.** *Profile → Security* now lists every
  live session for your account — the device, the address, when it was last used and
  when it signed in, with the one you are using marked — and lets you sign out any of
  them, or all the others at once. Each row names the client (*Firefox on Linux*,
  *Safari on iPhone*, *curl*) with an icon for the kind of device, because the
  question this screen answers is "is that me?" and a raw user-agent string does not
  answer it. An agent we cannot place is shown verbatim rather than guessed at. Until now a session could only be ended by waiting for it to expire or
  by changing your password, and there was nowhere to look to find out one existed.

  Own sessions only, deliberately: an admin view over everyone's would be a record
  of when each person works and from where, which is surveillance rather than
  administration.

  This works because a session is now a **row** as well as a token: the token
  carries an id, the row is what the middleware checks, and revoking deletes it.
  Signing out deletes it too — previously logout only cleared the cookie, so the
  token itself kept working until it expired.

  **Upgrading signs everyone out once.** Tokens issued before this release have no
  id and therefore no row, so they are refused; one sign-in fixes it per person.

- **The browser tab now names the page you are on** — `Images · Docker Commander`,
  and a container's own name on its detail page. Every screen was titled just
  "Docker Commander", which is no label at all once a few tabs are open: the app
  name is the part they all share. The page comes first for the same reason, since
  a narrow tab shows the beginning of the title and nothing else. The sign-in and
  first-run screens name themselves too.

- **MCP tokens expire by default, and admins set the rules.** New tokens last 30
  days unless another lifetime is picked, and never-expiring ones are off until
  an admin enables them. Revocation already existed, but it needs somebody to
  remember — and the tokens most worth revoking are precisely the ones everyone
  has forgotten about. An expiry date is the only control that keeps working when
  nobody is paying attention.

  There is a **ceiling** as well (a year by default), because "no never-expiring
  tokens" means nothing if the same thing can be had by asking for 99999 days. All
  three settings live in **Settings → Security**, and the creation form only
  offers lifetimes the server will accept — though the server re-checks anyway,
  since a form is not a boundary.

  It governs what may be **minted**, not what exists: tightening the policy will
  not silently cut off a running integration. Existing never-expiring tokens are
  listed on the **MCP Admin** page and can be revoked there.

- **A ceiling on how fast MCP can change things.** Every other control in MCP
  answers *is this allowed* — the token's narrowing, the user's permissions, the
  per-host scope. None answered *how much, how fast*, and that is the question
  that matters when the caller is a model stuck in a loop or a token that has been
  stolen. Both look exactly like an authorized user: every call is permitted,
  there are just suddenly thousands of them. Changes are now capped at 30 per
  minute per user, which bounds an incident to a few containers instead of an
  estate.

  **Reads are deliberately not capped.** They change nothing, and throttling them
  would push an assistant toward acting without looking — the behaviour the cap
  exists to prevent. Hitting the ceiling writes **one** audit entry per episode
  rather than one per rejected call, so a runaway loop cannot flood the audit log
  and bury the evidence of itself, and a refused call says plainly that it did
  *not* happen, because a model told only "denied" may record it as done.

- **MCP can deploy projects that target a remote host.** Previously refused,
  because MCP tokens carried no per-host authorization. They do now: a remote
  project is checked against the `hosts` section and the token's host scope, the
  same rule the web UI applies.

  It resolves its compose environment through exactly the helper the UI's deploy
  uses, not the simpler one — so a remote deploy through MCP still ships the
  project's own bind mounts to the target host and still **refuses** binds
  pointing outside the project folder unless that project is explicitly opted in.
  Using the simpler helper would have deployed successfully, passed every existing
  test, and quietly mounted paths off the remote host's filesystem; a test now
  drives that refusal through the MCP path specifically.

- **`preview_deploy` — see what a deploy would do before doing it.** Which services
  would be created, which would be recreated with a different image, and which are
  running but no longer in the compose file. It compares against the **containers
  actually running** rather than a record of the last deploy: a record says what
  someone last asked for, the containers say what is there, and those differ exactly
  when it matters. An invalid compose file comes back as a result rather than an
  error, because that is the single most useful thing a preview can report.

  Two things it is careful not to claim: a service that builds locally has no image
  until it is built, so it is never reported as an image change; and an orphaned
  service is described as *left running*, because the app does not pass
  `--remove-orphans` and saying "will be removed" would be alarming and false.

- **MCP caught up with the rest of the app.** Stack lifecycle (`start_stack`,
  `stop_stack`, `restart_stack`), `scan_image` for Trivy vulnerability scans, and
  `alert_delivery` — whether an alert actually reached anyone, which is a different
  question from whether anyone responded. Each already existed in the UI and simply
  had no MCP surface, leaving an assistant able to reason about a problem but not
  finish the thought: it could restart a container but not the stack around it.

  Stack **remove** is deliberately not offered — force-removing containers and
  networks is destruction, not safe control — and a test now fails if any
  destructive verb appears in the tool list, so adding one has to be a decision.

- **MCP diagnostic tools that don't need a shell.** `container_processes` (what is
  running inside), `container_changes` (what has been written since it started) and
  `search_logs` (find a string or regex across a host's containers, for when you
  don't yet know which one to look at). These are the questions that create pressure
  to open `exec`; answering them read-only is how that pressure goes away.
  `list_alert_rules` joins them, because an alert message cannot be judged without
  the threshold behind it — it names the channels a rule notifies through but never
  the recipients or the webhook URL.

- **MCP can see the alerting engine.** Three tools: `list_alerts` (the history,
  with the same filters as the UI), `active_alert_conditions` (what is over
  threshold right now, and for how long) and `acknowledge_alert`. The first two are
  separate on purpose — now that an alert is a condition with a lifetime, "what
  happened" and "what is wrong now" are different questions, and an assistant asking
  the first when it meant the second reports problems that resolved an hour ago.

  Host scoping goes into the query rather than filtering afterwards, so omitting
  `host_id` cannot widen the answer past what the caller may reach; a token narrows
  it further still.

- **Endpoint traffic on the network detail.** Docker reports no per-network
  counters — its stats are per *interface* with no network identity on Linux, which
  is why `docker stats` itself only shows one aggregate column. The network detail
  therefore sums the attached containers, and is explicit about the two things that
  would otherwise make it a confident wrong number: it is **endpoint** traffic, so
  container-to-container traffic counts twice (once as each side's RX and TX); and a
  container on several networks cannot have its counters split between them, so
  those are listed but excluded from the totals, with the exclusion shown rather
  than hidden. A container on exactly one network is unambiguous, which is the
  common case.

- **Network counters are kept in metric history** (`netrx` / `nettx`, cumulative),
  so rates can be derived at read time whatever the sampling interval was.

- **Network in the dashboard's resource breakdown**, beside CPU and memory — as a
  host-wide **summary over time**, not a pie and not a live ranking. CPU and memory
  divide a real whole (the host), so a pie is honest for them; network's only
  "whole" is whatever happens to be moving, which would make a container at 100% of
  2 KB/s look identical to one at 100% of 800 MB/s. A live ranking is no better:
  throughput is bursty, so the order changes on every poll. It is a time series —
  which is how Portainer's stats view and the standard cAdvisor/Grafana panels
  present it — so the per-container series lives on the container page and the
  dashboard shows current RX/TX with a short trend. Summed across containers, so
  traffic between two of them counts twice; the card says so.

- **Dropped packets and interface errors are kept in history**, as `netdrops` and
  `neterrors`. Stored RX+TX combined rather than as four series: they are near-zero
  almost always, so four would double the write volume to store zeros, and what an
  operator acts on is "this container started losing packets", not the direction.
  The live view keeps the split.

- **Network history on the container detail** — a second view in the History card,
  since throughput and percentages cannot share an axis. It derives rates with the
  same helper the live chart uses, so a counter reset and an uneven sampling
  interval behave identically in both rather than through two rules that can drift.

- **Network throughput on the container detail page.** RX/TX were already sampled
  and sent to the browser, and displayed nowhere. There is now a live throughput
  chart alongside CPU and memory, plus totals since the container started with
  packets, **dropped** and **errors** — the last two usually zero, and on the day
  they are not, frequently the only visible sign of the problem. Multi-interface
  containers get a per-interface breakdown.

  The chart plots the **derived rate**, because Docker reports cumulative counters
  and a chart of a number that only ever rises says nothing. Rates are computed
  from the elapsed time between samples rather than an assumed interval, and a
  counter reset — the container was recreated — reads as zero rather than a
  negative rate or a phantom spike. Counters stay raw end to end so history can
  derive rates at read time whatever the sampling interval was.

  Docker names interfaces (`eth0`, `eth1`…) without saying which Docker network
  each belongs to, so the container view reports the aggregate and says so instead
  of showing a per-interface split that invites a question it cannot answer.

- **The alert feed is paged, filterable, and says whether alerts were actually
  delivered.** It previously rendered every event on one page with no filters, which
  stops being usable at the point alerting starts being useful. It now pages 50 at a
  time and filters by severity, lifecycle kind, rule, container and message text,
  with an *unacknowledged only* toggle — **in the database**, so the totals and paging
  describe the whole result set rather than the page in front of you. Host scoping
  moved into the query for the same reason: filtering a page after fetching it yields
  short pages and a total that counts events the viewer isn't allowed to see.

  **Every webhook call and e-mail send is now recorded against its alert** with the
  outcome, HTTP status and an excerpt of the response. A webhook returning 500, an
  SMTP server refusing the connection, or a rule with *e-mail* ticked while no
  recipient is configured anywhere all used to fail silently — the alert appeared in
  the feed and looked handled while nothing had left the building. The webhook's
  **name and host** are stored rather than its URL, because those routinely carry a
  token and this record is readable by anyone with the alerts section; response text
  is truncated so a remote endpoint can't write unbounded data into the database.
  There is no automatic retry yet: failures are recorded, not re-attempted.

  **The sidebar badge counts problems rather than events** — unacknowledged
  warnings and criticals only. It previously counted every unacknowledged event,
  including the `resolved` ones, so the number went *up* when a condition cleared.
  A badge that grows as problems fix themselves is one people learn to ignore.

  **Resolved alerts are stored already settled**, with no Acknowledge action: there
  is nothing to do about a condition that has ended, so it never sits in the
  outstanding list waiting for a click that means nothing. One rule at the point of
  writing, rather than a special case in the badge, the filter and bulk acknowledge.

  **`/metrics` gained the alert engine's state**: `dockercmd_alert_firing` (one per
  live condition, labelled by host, container, metric, severity and rule),
  `dockercmd_alerts_firing_count` and `dockercmd_alerts_outstanding`. The firing
  gauge is the one to page on — it disappears when the condition resolves, so
  Alertmanager needs no `for:` window to guess. Also **corrects the
  `dockercmd_container_cpu_percent` help text**, which claimed *host-relative*: it
  is the docker-stats per-core figure, so any dashboard built on that description
  read four times high on a four-core host. A new `dockercmd_container_cpu_cores`
  makes normalising possible.

  **Columns are sortable** (time, severity, rule, host, container), server-side so
  it orders the whole result set rather than the visible page. Severity sorts by
  importance rather than alphabetically. The sort key is mapped through a fixed
  whitelist, because `ORDER BY` cannot be a bound parameter.

  **Clicking an alert opens its detail** — full message, measured value, how long
  the condition lasted, a link through to the container, acknowledgement, and every
  delivery attempt with the endpoint's own response. The feed row can only ever show
  a truncated view of any of that.

  **Acknowledging an alert records who did it** and when, and **Ack all** clears
  everything matching the current filters behind a confirm that names which of the
  two it will do. Filters include the **host**.

  **A toast appears when an alert arrives while the app is open**, with a countdown
  bar that pauses on hover, and can be turned off per account under
  *Profile → Preferences*. The feed, the sidebar badge and the toasts share a single
  poll: they had separate timers at first, so a row could appear in the table
  seconds before the toast announcing it.

- **Edit and redeploy a stack that wasn't created here.** Stacks started with the
  host's `docker compose` CLI have always been visible and their compose file
  readable; now it can be changed. The viewer became an editor with **Save** (write
  the file back to the host) and **Redeploy** (`docker compose up -d`) as separate
  steps, so a half-finished edit doesn't restart anything.

  **The file is edited in place, not adopted into a managed project.** That is the
  whole design rather than an implementation detail: relative paths in a compose
  file — bind mounts, `env_file`, `build.context`, `include` — resolve against the
  project's working directory, so copying the file elsewhere silently repoints
  every one of them. `./nginx.conf` would stop meaning the operator's config and
  start meaning whatever sits beside the copy, which is usually nothing — and a
  missing bind source deploys as an *empty* file instead of failing. Redeploy
  therefore runs in the stack's original working directory. For SSH hosts that
  means compose runs **on the host**, so the host needs the plugin; plain-TCP hosts
  expose no filesystem and stay read-only, and the editor says so instead of
  silently not appearing.

  Guarded, because the compose path comes from a container **label** and labels are
  set by whoever started the container: a write must land **inside the stack's own
  working directory** (symlink-resolved locally) and on a `.yml`/`.yaml` path, or
  it is refused — otherwise a `containers` grant would have become an arbitrary
  file write as the account the app runs under. The replacement is validated with
  `docker compose config` **before** anything is replaced, the previous version is
  kept as `<name>.dc-prev`, the swap is a rename so an interruption can't leave a
  half-file, and the original file's permissions are preserved. `--remove-orphans`
  is deliberately not passed, so deleting a service never silently deletes a
  container.

- **A Docker version compatibility matrix, and a README that says which versions
  are tested.** The app talks to Docker two ways — the Engine API through the Go
  SDK and the `docker compose` CLI as a subprocess — and both move, while users run
  whatever their distro ships. Until now nothing said which versions that covers.

  A [nightly workflow](.github/workflows/compat.yml) re-runs the whole Docker
  integration suite against a pinned `docker:NN-dind` for **Engine 24 through 28**
  and reports the negotiated API version. It isn't a new set of tests: pointing
  `DC_COMPAT_DOCKER` at a daemon swaps the fixture's host, so every existing test
  follows along. The README now states the floor — **Engine API 1.43 (Docker 24)** —
  and a test fails if the app negotiates below it, so the table can't quietly drift
  from reality.

  All five majors pass: Engine 24.0.9 (API 1.43), 25.0.5 (1.44), 26.1.4 (1.45),
  27.5.1 (1.47) and 28.5.2 (1.51). The matrix pins the **Engine**; the Compose
  plugin is whichever one the runner ships, so it answers "which daemons work",
  not "which Compose releases work".

  **It caught a real difference on its first run.** On Engine 24, `/containers/json`
  reports a container as `running` for ~250 ms after it has stopped, while `inspect`
  already says `exited`; newer engines are consistent immediately. The app polls, so
  a user sees the right state on the next refresh — but the stack test asserted
  instantaneous consistency and was the one failure in the Engine 24 run. It now
  polls with a bounded wait.

- **Moving a project to another host can now actually move it.** Changing a
  deployed project's target host left the stack running on the old one — the app
  kept no record that anything was there, so you ended up with two live copies and
  only one of them visible as "the project's host". The settings dialog now offers
  to bring it down on the host it is leaving, ticked by default, with a danger
  confirm that says exactly what goes: containers stopped and removed, and the
  volumes seeded there for its bind mounts deleted. **Named volumes holding your
  data are left alone.** Unticking it keeps the old behaviour, and the dialog says
  what that means too.

  The teardown runs **before** the record moves, and a failure aborts the change —
  otherwise a stack would be left running on a host the app no longer points at,
  which is the same problem made invisible. The old host needs the same permission
  as the new one, so "move it away" can't become a way to stop workloads on a host
  you were scoped away from.

- **Per-host RBAC scoping** — a role can be limited to specific Docker hosts, so
  *"may restart containers"* can mean *"on staging, not production"*. This is
  phase 2 of [design/rbac-roles-and-host-scoping.md](design/rbac-roles-and-host-scoping.md).

  **This is new authorization, not a tightened check.** Until now a non-admin
  holding a section could act on **any** daemon by passing `?host=N` — including
  hosts they couldn't see on the Hosts page, which is gated by the separate
  `hosts` section. The check now lives in the permissions middleware that every
  host-targeting route already passes through, so all ~60 call sites are covered
  at once and a route added later is covered without anyone remembering.

  It is enforced on **every** surface a host can be named on: REST (`?host=`), the
  **WebSocket** subscribe frame, **MCP** tools' `host_id`, and a managed project's
  own target host (which lives in the project record, not the URL). MCP tokens can
  now be narrowed to a host subset too, and the audit log records which host an
  action happened on.

  Nothing changes on upgrade: **an empty host list means every host**, so every
  existing role and account keeps exactly its current reach, and a section granted
  directly on an account stays unscoped. The **local daemon is always in scope** —
  making it scopeable would let a single-host install lock itself out.

  **Scoping hides as well as blocks** (phase 3, landed in the same release). A host
  outside your scope is absent from the host list, its projects aren't listed, its
  alerts don't reach the feed *or the unread badge*, its entries don't appear in the
  audit log, and a container's metrics history is refused even if you know the
  container id.

  Three dashboard endpoints — `/api/stats/overview`, `/api/system/df` and
  `/api/stats/ports` — took `?host=` while belonging to **no section**, so the check
  above returned before it ever looked at the host and they served another host's
  counts, disk usage and published ports. Ungated means "no section required", not
  "any host you like"; a named host must now be one your grants reach.

  The metrics series is keyed by container id alone, so knowing an id used to be
  enough to read its CPU/memory history from any host. The monitor already knew
  which host each sample came from and was discarding it; it's now recorded and the
  series is authorized against it. An id with nothing recorded is treated as
  **unknown**, not as the local daemon, so a scoped caller can't probe ids to learn
  what exists.

  > The **alert engine** still watches every host, by design — it's background work
  > with no user context. A rule that lists you as a recipient can therefore mail you
  > about a host you can't see in the app. That's how you configure recipients, not
  > something the app decides per viewer.

- **A fallback role for LDAP mappings.** A mapping can name a role that has since
  been deleted; until now its members simply got nothing. Nominate a fallback in
  *Settings → LDAP* and they degrade to that baseline (**Viewer** being the obvious
  choice) instead. The nominated role can't be deleted while it holds the job, and
  the two built-in roles were never deletable, so the fallback itself can't go
  stale.

  It applies only to a mapping that **matched and then failed to resolve** — never
  to a user whose groups map to no role at all. That case is "not entitled", and
  granting a baseline there would hand a role to every account in the directory that
  can authenticate.

- **LDAP groups can grant named roles**, not just raw sections. A group mapping now
  carries roles, so directory membership drives access the same way it drives
  everything else in an AD shop: put someone in `cn=deployers`, they hold
  **Deployer** on their next login; take them out, they lose it. Roles are
  re-derived on **every login**, so revocation doesn't wait for a session to
  expire. This completes phase 1 of
  [design/rbac-roles-and-host-scoping.md](design/rbac-roles-and-host-scoping.md).

  Two limits are deliberate and tested: a mapping **can never grant admin** (only
  the admin group DN does that, and role management isn't reachable through any
  role), and a role id left behind by a **deleted role grants nothing** rather than
  failing the login.

  Upgrading changes nothing on its own — roles become directory-driven only once at
  least one mapping actually grants a role, so a config written before this release
  keeps whatever roles an admin assigned by hand. Sections keep their existing,
  stricter rule: any mapping at all makes LDAP authoritative for them.

- **A profile page** (person icon beside *Sign out*) with three tabs. **Account** —
  who the installation thinks you are (username, account type, whether you sign in
  locally or through LDAP, created and last-seen), and your **alert e-mail**.
  **Security** — 2FA status and *Pair a new authenticator*. **Access** — the roles
  you hold and a table of every section you can reach, whether you can change it,
  and **which role each permission came from**, which was previously invisible to
  anyone but an admin. It replaces the small e-mail dialog.

  All of it is self-service and reads only your own account; role management stays
  admin-only.

### Changed
- **Threshold alerts are conditions with a lifetime, not lines reprinted every
  minute.** Testing the engine against a real stack produced an alert log that was
  the same seven lines repeated every 60 seconds, and three separate problems were
  behind it. Every `resource` rule was evaluated independently, so a container over
  both a *warning >5%* and a *critical >10%* memory rule emitted **two** alerts for
  one fact. Nothing tracked whether a condition was already known, so the cooldown
  expiring re-announced it as if it were new. And nothing was ever emitted when a
  problem went away, so the log could not tell you whether it was still happening.

  A threshold alert is now one **condition per container + metric**, regardless of
  how many rules notice it, and only the most severe rule speaks for it. You get an
  event when it starts, when it **escalates** or **eases** to a different severity,
  when the re-notify interval elapses (`repeat`), and when it **resolves** —
  carrying how long it lasted. Escalation updates the existing condition rather
  than opening a second one, so the incident clock keeps running. Silence now means
  "unchanged", which is the whole point.

  Conditions are stored, so they survive a restart: without that, nothing would
  ever resolve across one. `state`, `log` and `restart` rules stay edge-triggered
  and keep the plain cooldown — a container that died has no later moment at which
  it stops having died.

- **CPU thresholds can be a percentage of the whole machine.** Docker's CPU figure
  is per-core — 100% is one core — so a container busy on four cores reads ~400%
  and a `> 80%` rule fires permanently on any multi-core host. Rules gained a
  **CPU % (of all cores)** metric that normalises by the core count, the editor now
  says which basis each option uses, and existing rules keep the per-core meaning
  they were written with.

- **Alert messages say what the number measures.** `MEM 61.9% > 5%` never revealed
  whether that was of host RAM or of the container's limit (it is the limit). It
  now reads `MEM 3.0 GB / 5.0 GB (61.9% of limit) > 5%`, and CPU messages name
  their basis and the core count.

- **Settings and MCP Admin are now tabbed**, using the same shared tab bar as
  Alerts, Templates and the container detail view: Settings splits into
  **Features / Security / LDAP / Email**, and MCP Admin into **API tokens / OAuth
  clients**. The SMTP form moved from Alerts → Settings → Email (see Security
  above); Alerts keeps Feed / Rules / Webhooks.
- **Changing a deployed project's target host now warns first.** It does not tear
  the project down on the old host, so you would end up with two live copies
  while the page showed only the new one. The host picker says so inline and asks
  for confirmation before saving.

### Fixed
- **The manual's screenshots showed an idle machine, and one of them was a
  duplicate.** They were re-shot against a daemon under real load, so the pictures
  now show what the pages are for: a log stream with three colour-coded sources, an
  events feed with something in it, a network graph with seven containers on it,
  and CPU history with actual peaks. Three faults in the generator came out in the
  process. `network_detail.png` and `network_detail_graph.png` had been
  byte-identical since the file was written — the graph toggle is an icon-only
  button and the selector matched on text, so the miss went unnoticed and the shot
  was taken in the unchanged state; a `prep` that cannot find its control now fails
  the shot instead of photographing the page anyway. Alert toasts covered whatever
  was underneath on any instance busy enough to be worth photographing, so they are
  hidden during a run. And the shots that need time — the events feed, the
  dashboard's network *rate* — now wait for it rather than capturing "Collecting…".
- **A busy container broke its own history chart.** CPU is reported the way
  `docker stats` reports it — 100% is *one core* — so a container working across
  several cores legitimately reads 300%. The chart's axis was pinned to `[0, 100]`,
  which recharts treats as a hint rather than a limit: once the data overflowed it
  rendered a five-digit top label (52348%) above gridlines spaced by 80, on the
  page that exists to show exactly that workload. The axis now rounds up to whole
  cores and never drops below 100, so an idle container still reads against a
  familiar 0–100 scale. Invisible until now because the demo instance was idle;
  the alert engine had the convention right all along ("CPU 272.0% of one core,
  16 cores available").
- **Nothing checked that the committed `web/dist` still matched `web/src`.** CI
  runs `make ui` and then tests the bundle it just built, so a stale committed one
  passes every check green. Binaries cut from a tag rebuild it as well — but
  `go install` embeds whatever is in the repository, so that channel, and only that
  channel, could ship a UI built from older source. CI now compares the two and
  fails with the command to fix it. Verified by changing a component without
  rebuilding: the step catches it.
- **`make ui` rewrote `package-lock.json` behind your back.** Its install step was
  `npm install`, which resolves afresh rather than installing the locked tree, and
  an npm older than the one that wrote the lockfile drops fields it does not know —
  30 lines of `libc` platform hints, most recently — leaving an unrelated edit
  staged in the next commit. It now runs `npm ci`, which is also what makes the
  bundle comparison above reproducible.
- **The guard on the README's test counts guarded one number out of three, and
  would have missed the drift it was written for.** It checked only the Go figure,
  so the README could claim 73 frontend tests and 7 adversarial cases against real
  counts of 147 and 115 with the suite green — both verified by falsifying them. Its
  tolerance was a quarter either way, which at 700 real tests accepts anything from
  525: the "~533 for five commits" drift cited in its own comment as the thing it
  would catch would have passed. It now checks all three figures at a tenth either
  way. Doing so surfaced a fourth error — the 115 pentest cases are a *subset* of
  the Go total, not a tier alongside it, so the README's phrasing implied 830 tests
  where there are 715. The unit figure is now the disjoint 600.
- **The profile page had no manual page, and the limits were nowhere.** The page
  where you manage your own second factors, sessions and sign-in options was
  documented as a section inside *Users & roles* — findable only if you already
  knew to look there — and its **Access** tab, which answers "why can I reach
  this?", was described nowhere at all. It is now [its own
  page](docs/profile.md), linked from the index.

  Alongside it, a [Limits](docs/limits.md) page: every cap you can actually hit,
  with the reason where the number is not obvious. Session lifetime, the sign-in
  lockout (5 per 15 minutes — the number the docs had never stated), upload and
  archive caps, project file counts, scan timeouts. Hitting a limit and finding no
  page that names it is a bad way to learn the app has one.
- **The audit log now documents itself.** `docs/audit.md` named seven actions out
  of 143, as prose ("e.g. `container.stop`, `image.pull`…"), which is a poor shape
  for the one page whose readers arrive asking *what can I look for?*. The full
  set is now listed by area — and generated from the source, with a test that
  fails when an audited action has no entry, when an entry names an action the
  code never writes, and when a new verb appears in one of the runtime-assembled
  families. The same treatment the CLI flags have had.
- **Documentation that described something other than the app.** An audit compared
  every claim in the manual against the code, and in the other direction too —
  what the app does that no page mentions. Corrected: the container network panel
  shows an interface *count*, not a per-interface breakdown (the code says why);
  Probe fingerprints TCP only and leaves UDP with its passive guess, and the list
  of protocols it recognises was a third of the real one; the container file
  browser uses `docker cp` for transfers but a direct `ls`/`mkdir`/`rm` for the
  rest, so only the Console needs a shell; force-removing a volume is not a way
  past "volume is in use"; the registry list carries no indication of whether a
  secret is stored, and a stored credential cannot be edited in place; the events
  filter is one box matching four fields, not three filters, and the feed is
  live-only; `-session-ttl` is the one option with no environment variable. The
  test counts in the README were five commits stale.
- **The events feed said "Live" over a dead connection.** Its WebSocket had no
  reconnect at all — unlike the stats/logs socket next door — so a server restart,
  a proxy idle timeout or a laptop waking from sleep left the page showing a
  pulsing green badge above a list that would never move again. That is the worst
  shape for the bug: an empty feed reads as "nothing is happening", so nobody looks
  closer. It now reconnects, and the badge reports the *connection* rather than
  just the pause toggle — it says **Reconnecting…** when it is not live.
- **The admin user list called a passkey-protected account "off".** The 2FA column
  answers "is this account protected?", and an admin auditing their users acts on
  it — but it read *"does this account have an authenticator app?"*, which stopped
  being the same question the moment passkeys existed. The server had been sending
  the right field since passkeys landed; the table was not reading it. It now shows
  `enabled` for an authenticator app, `passkey` for an account protected by one,
  and `off` only when there is genuinely no second factor.

- **Error messages no longer start with `auth:`.** Go puts a package prefix on every
  error, which is right for a log line and wrong on a screen — it reached one as
  `auth: passkeys need HTTPS (or localhost)` under a greyed-out button. Stripped on
  the way out, by an allowlist of this app's own prefixes rather than "everything
  before the first colon", because messages legitimately contain colons.

- **A request body can no longer be dribbled out to hold a handler open.** The
  server set `ReadHeaderTimeout` but no `ReadTimeout`, so once the headers arrived a
  client could take as long as it liked over the body — and Go runs the handler from
  the moment the headers land, not the moment the body finishes. That is a resource
  anyone could reserve for free, and it was what let a stalled WebAuthn registration
  straddle a change in the account's protection.

  Requests now have a 60-second read timeout. The routes that legitimately take
  minutes — loading or importing an image, sending a build context, uploading and
  extracting a file into a container or a volume, importing a project — swap it for
  a *rolling* one, extended each time data arrives, so the limit is "this upload
  went quiet", not "this upload took a while". A multi-gigabyte upload over a slow
  link is unaffected; a stalled one is dropped — the two file-upload routes answer
  it with a 408 that says so, the rest report it the way they report any other
  failed upload. The profiling listener gets the same treatment.

  WebSocket streams are not on this clock: `net/http` clears deadlines when a
  handler hijacks the connection. One side effect worth knowing: an idle
  keep-alive connection now closes after 60 seconds where it previously stayed
  open, because Go falls back to the read timeout when no idle timeout is set.

- **Leaving a page mid-stream could drop the whole WebSocket.** Stats and log frames
  were written under the *subscription's* context, and the websocket library
  registers a `context.AfterFunc` on the context given to `Write` that closes the
  entire connection. So unsubscribing while a frame was in flight — clicking away
  from a container whose stats are streaming — took every other subscription on that
  socket with it, and the client saw an abrupt disconnect with no error.

  Writes now run under the connection's own context; only the stream itself is
  cancelled by an unsubscribe.

  A cancelled stream's last few frames are dropped rather than delivered.
  Subscription ids are deterministic, so leaving a container page and coming
  straight back would otherwise hand the old stream's tail to the new subscription
  — a duplicated log line, or a stats sample from before the reset.

  This is what had been failing intermittently in CI as
  `TestHubResubscribeDoesNotCancelTheNewSubscription` ("failed to read frame header:
  EOF"): a loaded runner widened the window enough to hit it, and a quiet laptop
  never did in two thousand runs. The new test reproduces it deliberately by
  unsubscribing from a stream that never stops emitting.

- **A review pass over the last few merges.** None of these were reachable as an
  attack, but each was a statement the code no longer backed up:
  - The MFA challenge is now required to carry an expiry. `exp` is optional in a
    JWT, so a challenge token without one parsed cleanly and was then dereferenced
    for the one-use window — a nil pointer on the pre-2FA endpoint. It needs the
    signing secret to reach, which is why this is a guard rather than a hole.
  - **A successful step-up clears the rate-limit budget**, as a successful login
    already did. Without it, someone else's wrong guesses from the same address
    locked the real account holder out of *their own* re-pairing for the rest of
    the window, even with the right password.
  - Re-pairing with **no request body** is a failed step-up (403), not a malformed
    request (400) — it is the shape an older client sends, and the answer should
    describe what actually happened. A genuinely broken body is still a 400.
  - **The backup size line counts the database.** It is usually the largest thing
    in the archive, and leaving it out made "1.2 MiB" mean *everything except the
    part you care about most*. The units now say `MiB`/`GiB` too, matching the
    rest of the project instead of dividing by 1024 and printing `MB`.
  - Saving in **Settings no longer shows a failure in green.** "Save failed" was
    rendered in the success colour, which said the setting was live when it wasn't;
    it now carries the reason.
  - A **2FA code rejected by the network** no longer reads as a rejected code. A
    dropped request said "That code was not accepted", sending people hunting
    through an authenticator for a problem in the wire.
  - The session list clears a stale error when a reload succeeds, and offers
    **Try again** when the first load fails — previously that was a dead end.
  - Comment corrections where the prose had drifted from the code: a doc comment
    that had slid onto the wrong function, a breadcrumb naming a test that does not
    exist, a rationale about "recycled user ids" (ids are `AUTOINCREMENT` and never
    reused), a restore note describing symlink *target* checking that is now an
    outright refusal, and an SMTP test comment that said the opposite of what the
    test pins.

- **Re-subscribing to a live stream no longer cancels the wrong one.** The
  frontend reuses deterministic subscription ids, so leaving a container page and
  coming straight back replaced a subscription while the old stream was still
  winding down — and the old one's cleanup then removed the *new* entry. The new
  stream kept running with nothing able to stop it short of closing the socket,
  and the client was told its live subscription had ended. Subscriptions are
  numbered now, so a stream can tell whether the entry under its id is still its
  own.

- **A log stream no longer ends when the first of its two readers does.** Docker
  multiplexes stdout and stderr; returning as soon as either finished dropped
  whatever the other was still emitting, which on a plain log fetch typically
  meant losing the tail of stderr. Scanner errors were discarded too, so a line
  over the 1 MiB buffer looked like a clean end — the WebSocket client saw a normal
  close and a log-following alert rule stopped silently until the next reconcile,
  missing every match in between.

- **Three slow leaks.** A log follower that ended on its own forgot its cancel
  function instead of calling it, leaving a context attached to the monitor's root
  for the life of the process; restart timestamps were pruned only when a restart
  rule happened to read them, so a host with churn accumulated them for ever with
  no such rule configured; the in-memory metric history never forgot containers
  that stopped reporting, keeping their full retention window indefinitely.

- **A non-JSON error response is reported as its status.** The API client parsed
  the body before checking whether the request succeeded, so an error page from
  something other than this app — a reverse proxy's 502 — threw a SyntaxError
  instead of the error type every caller expects. The UI showed
  `Unexpected token '<'` rather than the status, and code branching on the error
  type took the wrong path.

- **An unreachable host no longer reports its alerts as resolved.** A failed or
  timed-out container listing left that host out of the stats snapshot, and the
  resolve sweep read the absence as recovery — so every live condition on it got a
  "back to normal" event, and then started over as a fresh incident, with the
  duration reset, when the host returned. On a flaky link that produced a
  resolve/fire pair every poll; disabling a host did the same. Silence is not
  recovery: only a host that was actually observed can end a condition.

- **A host that went away stayed "unreachable" until the app was restarted.** The
  Docker client is cached per host, and an SSH one captures its SSH connection in
  the transport's dialer — so once that connection died (the machine rebooted, the
  link dropped) every later call failed against the same dead object. Nothing
  evicted it: the cache was only cleared when a host was edited, deleted or
  re-trusted. The host alerted as offline and then never recovered, which is the
  behaviour that teaches people to ignore host alerts. A failed health probe now
  drops the cached client, so the next 30-second sweep dials afresh.

- **One unreachable host no longer freezes the others.** Building a client held a
  manager-wide lock, and for SSH hosts that includes a synchronous dial whose
  handshake isn't bounded by the request context. A single sleeping laptop could
  stall every Docker call in the app — the local host included — in ten-second
  bursts. The dial now happens outside the lock, with concurrent first-time callers
  still ending up on one shared connection.

- **Acknowledging an alert is now scoped to that alert's host**, over both the
  REST route and MCP. The feed and *ack all* were already scoped; acknowledging a
  single id was not, and alert ids are sequential integers — so a role confined
  to staging could clear production's alerts. Quietly, too: an acknowledged alert
  stops being surfaced, which makes it a suppression primitive rather than a
  nuisance. A missing alert and an out-of-reach one give the same answer, so the
  route cannot be used to discover which ids exist elsewhere.

  Deliberately **not** gated behind the `hosts` section. Host reach is derived
  from grants across all sections, so a user whose `alerts` grant is scoped to a
  remote host already sees that host's alerts; demanding `hosts` as well would
  show them an alert they could neither acknowledge nor trace. (The same
  correction applies to `alert_delivery`, which had been over-tightened when it
  was first scoped.) Over-tightening is not a safe default — it is a different
  bug that looks like caution, and it now has its own test.

- **`list_managed_projects` no longer names projects on out-of-scope hosts.** The
  REST list has always filtered these out — a project names its target host, so
  listing one discloses that host's workloads, and whether they are deployed, to
  someone who cannot reach it. The MCP tool returned every project regardless.
  It now applies the same filter, and answers with a shorter list rather than an
  error, since an error would itself confirm such a project exists.

- **`alert_delivery` now authorizes against the alert's host.** It took an alert
  id and checked only the `alerts` section, so a token confined to one host could
  walk the id space — alert ids are sequential integers — and read which webhooks
  fired for another host and whether they succeeded. The REST feed never had this
  problem: it only fetches deliveries for events a host-scoped query already
  returned. A missing alert and an out-of-reach one now give the same answer, so
  the tool cannot be used to discover which ids exist elsewhere.

- **An absurd token lifetime is refused rather than silently wrapped.**
  `time.Duration` is int64 nanoseconds, so days × 24h overflows above ~106,751
  days: asking for 200,000 days produced an expiry in 1989 — a token dead the
  moment it was issued — and larger values wrapped to arbitrary dates. Only
  reachable once an admin removed the ceiling, and it failed safe, but it
  answered the wrong question without saying so.

- **`preview_deploy` now re-checks the project's host.** While remote-host
  projects were refused outright, that refusal was the only thing gating this
  tool; allowing them without a per-host check let a token scoped to one host
  read back the services and images running on another. A preview being a *read*
  does not exempt it — listing what runs on a host is exactly what the per-host
  scope exists to withhold. It now authorizes against the project's actual host,
  the same way `deploy_project` and `down_project` do. The regression test drives
  the MCP **tool** rather than the helper it forgot to call, and asserts the
  preview closure is never reached for an out-of-scope host.

- **Opening the Alerts page made the app stop responding to navigation.** The URL
  changed and the screen did not. Moving *Ack all* into the page header published
  the handler through an effect that depended on the handler itself — a new closure
  every render, so the effect re-ran every render, set state in the parent, and
  re-rendered the page. An infinite render loop, which React does not report as an
  error: it simply never gets far enough to paint. Published through a ref now, so
  the only dependency is the stable setter.

- **A role scoped to only-invalid hosts became unscoped instead of being refused.**
  Sending `hostIds: [0]` was sanitised down to an empty list, which means *every
  host* — so a request to narrow a role quietly produced an unrestricted one, with
  an audit line describing a scope that wasn't there. An explicit scope that
  survives no validation is now a 400, matching the rule already applied to MCP
  token section scopes.
- **A non-positive `?host=` is the local daemon everywhere.** The Docker layer has
  always treated `hostID <= 0` that way, but the new host-scope check took it
  literally: `?host=-1` was served by the local daemon while being authorised and
  audited as host −1, so a scoped user was refused something they were allowed.
  Normalised at all three entry points — REST, the WebSocket subscribe frame and
  MCP tool arguments.
- **Deleting the LDAP fallback role could slip through** when the LDAP settings
  could not be read (corrupt JSON, transient DB error): the guard was skipped
  rather than failing closed, which is the one situation it exists for.
- **The profile page span forever if loading permissions failed** — "still loading"
  and "failed" were the same state. It now says what went wrong and offers a retry.

- **Redeploying a stack rebuilds its image too.** The same staleness bug as the
  project deploy below, on the other code path: `StackRedeploy` ran a plain
  `docker compose up -d`, so a CLI-discovered stack declaring `build:` kept running
  the image from its first deploy however much its Dockerfile or context changed on
  the host. Found while auditing the docs against the merged code, right after the
  project half was fixed — the two deploy paths are separate code and only one of
  them had been corrected.

- **Deploying a project no longer runs a stale image.** A project with a `build:`
  section was built on its first deploy and then never again: `docker compose up -d`
  builds a service only when its image is **missing**, so every later deploy reused
  the original image no matter what changed in the Dockerfile or its build context.
  Edit a Dockerfile in the project editor, hit Deploy, and the CLI reported
  `Container Running` — a wrong answer delivered as a success. Deploy now passes
  `--build`, which is a no-op for services that only pull an image, so an image-only
  project pays nothing. `POST` a deploy with `{"build": false}` to opt out. The MCP
  `deploy` tool matches, so the same project behaves the same way from either.

  While confirming this, one roadmap item turned out to rest on a wrong premise:
  **uploading `build:` contexts to a remote host already worked.** Docker's build
  API takes the context as a tar from the client, so a remote daemon receives the
  local folder and builds the image there — unlike a bind mount, which nothing
  uploads and which the app therefore seeds into a volume. Both are now covered
  together by an end-to-end test against a second daemon.

- **Starting a 2FA re-pair no longer disables the authenticator you already have.**
  Beginning enrolment overwrote the live secret and switched 2FA off, so abandoning
  the flow silently left the account without 2FA *and* invalidated the authenticator
  in the user's hand. Harmless while first-time enrolment was the only caller;
  the profile page's *Pair a new authenticator* makes it an everyday path. The new
  secret is now held aside and only takes over once a code from the new device is
  accepted — cancel, close the tab or type the wrong code and nothing changes.
- **Alert e-mails go where you want them.** An alert rule can now carry **its own
  recipients** instead of every rule mailing the one instance-wide address. Each
  account also gets an **alert e-mail** it can set itself (the person icon beside
  Sign out), which prefills as the recipient the first time you switch e-mail on
  for a rule — so the common "tell me about my own alerts" case needs no typing.

  Recipients resolve most-specific-first: the rule's own list, then the host's
  `alert_email` override, then the instance-wide SMTP *To*. Rules created before
  this have no list, so they deliver exactly as they did. When LDAP publishes a
  `mail` attribute it is synced to the account on login; a blank attribute never
  clears an address set by hand, since silently losing it would stop alerts
  arriving.
- **Backup & restore** — `dockercmd --backup <file>` writes a complete, portable
  snapshot of the installation (database + `projects/` + `project-templates/`), and
  `dockercmd --restore <file>` puts it back. The snapshot is taken through a live
  connection with `VACUUM INTO`, so it is **safe to run while the server is up** —
  copying the `.db` yourself is not, since it runs in WAL mode.

  Both secret keys live inside the database, so a backup restores onto a fresh
  machine as-is — and, for the same reason, the archive is equivalent to the
  plaintext of every stored secret. It is written `0600`, and **`--passphrase`
  encrypts it** (AES-256-GCM with an Argon2id-derived key). The passphrase is read
  from the terminal, or from stdin when piped so scheduled backups can be encrypted
  too; it is never passed as an argument, where it would land in shell history and
  `/proc/<pid>/cmdline`. Restore refuses to overwrite an existing installation
  without `--force`, and every archive entry is jailed to the data dir.
- **Named RBAC roles** — a role is a reusable bundle of section grants, so an
  admin no longer ticks thirteen checkboxes per account. Each section in a role is
  independently **read-only or writable**, which is finer-grained than the
  account-level read-only flag. Two roles ship built in and are immutable —
  **Viewer** (every section, read-only) and **Operator** (day-to-day work, but
  deliberately not `hosts` / `registries` / `audit`) — with **Duplicate** to make
  an editable copy, mirroring project templates. A user may hold several roles and
  still have per-account sections; effective access is the union.

  Existing accounts are unaffected: with no roles assigned, permissions resolve
  exactly as before. The precedence is explicit and tested — admins bypass; grants
  union; the **account-level read-only flag caps everything** so a writable role
  cannot lift it; and an app-wide **disabled section wins over any role**. Role
  management is **admin-only** (no combination of section grants reaches it) and
  every change is audited. Changes apply on the user's next request — nothing is
  cached in the session, so revoking a role is immediate.

  **Managed from the UI:** *Users* becomes **Users & roles** with an Accounts /
  Roles tab pair. The Roles tab lists each role with its grants, how many accounts
  hold it, and a built-in/yours badge; built-ins open read-only with **Duplicate**
  to make an editable copy. The role editor sets every section to *—* / *read* /
  *write* with all thirteen visible at once — no scrolling to check you didn't
  grant `hosts`. Accounts gained **Roles** and **Access** columns, roles are
  assignable in the create/edit forms, and an account whose grants are all
  read-only is labelled *view only* even without the read-only flag.

  _This is phase 1 of [design/rbac-roles-and-host-scoping.md](design/rbac-roles-and-host-scoping.md); per-host scoping is phase 2._

### Documentation
- **`docs/hosts.md` now covers two SSH-host requirements** that produced
  confusing failures. The remote `sshd` must allow forwarding
  (`AllowTcpForwarding`) because the Docker API is tunnelled over an SSH channel
  — Alpine ships it as `no`, and `sshd` honours the *first* occurrence of a
  keyword, so appending to `sshd_config` is silently ignored. This otherwise
  yields a half-working host: `docker compose` uses its own `dial-stdio` channel
  and needs no forwarding, so **Projects deploys succeed while monitoring
  fails**. Also noted that an agent holding several keys can exhaust `sshd`'s
  `MaxAuthTries` before the right key is offered.
- **New [How it's tested](docs/testing.md) page** — the app is pointed at real
  Docker daemons, so this spells out the five test tiers (unit, adversarial
  "pentests", runtime smoke, integration against a real daemon, and multi-daemon
  end-to-end over TCP and SSH), how to run each, and — deliberately — **what is
  not covered**: CI runs only the deterministic tiers, there's no browser/UI
  suite, and coverage is unit-only. Linked from the README and CONTRIBUTING.

### Removed
- The **Go Report Card badge**, which now renders "go report: retired" since the
  service shut down. It returned HTTP 200, so it displayed a misleading grade
  rather than failing visibly as a broken image.

## [1.5.1] — 2026-07-30

### Changed
- **Release signatures are now a cosign bundle.** The release workflow moved to
  cosign v3, which writes a single Sigstore bundle instead of a detached
  signature + certificate pair, so releases now ship `SHA256SUMS.bundle` in place
  of `SHA256SUMS.sig` / `SHA256SUMS.pem`. Verify with
  `cosign verify-blob --bundle SHA256SUMS.bundle … SHA256SUMS` (needs **cosign
  v3+**); releases up to v1.5.0 keep the old pair. The workflow now also asserts
  the bundle exists before publishing, so a signing regression fails the release
  instead of silently shipping unsigned assets.
- **Dependencies updated** — Go modules (incl. `golang.org/x/crypto`,
  `modernc.org/sqlite`, `go-chi`, `go-ldap`, `go-redis`, `coder/websocket`),
  frontend packages (incl. React, Vite, Recharts, `@xyflow/react`) and the
  pinned GitHub Actions.

### Fixed
- **`CHANGELOG.md` — restored the missing `[1.5.0]` heading.** The v1.5.0 entries
  had been left sitting under `[Unreleased]` with no version heading of their own,
  which also orphaned the existing `[1.5.0]` link definition.

## [1.5.0] — 2026-06-16

### Added
- **Image vulnerability scanning** — the Images page gains a **Scan** action that
  runs [Trivy](https://trivy.dev) against an image on the selected host and shows
  a severity summary plus a CVE table (package, installed vs. fixed version,
  advisory link). Trivy is an optional dependency probed at runtime; when it's
  absent the dialog says how to install it. Scans run live (not persisted). The
  image reference is validated before it reaches the CLI (no argument injection).
- **Remote Projects** — a managed Compose project can now target a **remote
  Docker host** (added under Hosts), not just the local daemon. Pick the host at
  create time or in the project's Settings; deploy/down/restart run
  `docker compose` against that host (TCP+TLS or SSH). Host TLS certs are written
  to a private, mode-0600 temp dir only for the duration of the command and wiped
  afterwards. First cut supports **images and named volumes**: a compose file
  that bind-mounts a local host path is blocked on remote deploy with a clear
  message (the remote daemon can't see local files — file sync is a follow-up).
- **Private-registry tag autocomplete** — image-tag suggestions in the editor and
  Create-container form now also list tags from a **configured private registry**
  (the Docker Registry v2 API, with the registry's stored credentials and a Bearer
  token handshake), not just Docker Hub. Only hosts you've added under Registries
  are ever contacted; the token realm is constrained (https, no internal address)
  to avoid SSRF.
- **LDAP group → section mapping** — Settings → LDAP can now map LDAP groups to
  RBAC sections, so a user's allowed sections follow their group membership. A
  user's sections are the union across every mapping whose group they're in. When
  any mapping is configured, LDAP is authoritative for non-admin users' sections
  (recomputed from group membership on each login); group DNs match on the full
  DN case-insensitively and unknown section names are ignored. The admin role
  stays sticky once granted (no auto-demote on group removal).
- **Schema-aware Compose autocomplete** — editing a Compose file now suggests
  keys at the right nesting level (top-level, service, and nested `build` /
  `healthcheck` / `deploy` / `logging` blocks) and known enum values (e.g.
  `restart:` → `always` / `unless-stopped` / …) as you type, complementing the
  existing image-name completion and server-side `docker compose config`
  validation. Suggestions only apply to Compose files, not arbitrary YAML.
- **Built-in self-signed TLS certs** — `dockercmd --make-certs [hostnames…]`
  generates an ECDSA self-signed certificate + key into `<data-dir>/tls/` (key
  mode 0600), covering localhost / 127.0.0.1 / ::1 plus any hostnames or IPs you
  pass, and prints the `DC_TLS_CERT` / `DC_TLS_KEY` to serve HTTPS — no `openssl`
  needed. For public hosts use a real CA / Let's Encrypt.
- **Alert rule import/export** — the Alerts → Rules tab can now export every rule
  to a portable JSON bundle and import rules from one, so rule sets can be
  version-controlled or moved between instances. Webhooks are referenced by name
  (never by internal id, and their URLs/secrets are never included); on import an
  unknown webhook name leaves the rule without a destination and is reported.
  Imported rules are validated (type, severity, config shape and size) and always
  created anew — an import never overwrites or deletes existing rules.
- **In-app one-tap update** — admins now get an "Update & restart" button on the
  update-available banner. It downloads the latest release, verifies its SHA-256
  (the same fail-closed check as `--self-upgrade`), atomically replaces the binary
  and restarts the process in place (re-exec), then the UI reconnects on the new
  version. Gated to admins and disabled with `DC_SELF_UPDATE=0` (the banner still
  shows). Not offered on Windows (restart the service manually).

### Changed
- **Consistent tab bars** — Templates now uses the same underline tab strip as
  Alerts and the container detail view (a shared `Tabs` component), instead of
  its own pill buttons. Icons and per-tab counts are preserved.

### Security
- **Host TLS private keys are encrypted at rest** — a TCP host's client private
  key (`hosts.tls_key`) was stored in plaintext in the database; it's now
  encrypted with the same AES-256-GCM cipher that protects registry/SMTP secrets
  (the CA and client certificate are public, so they're stored as-is). Existing
  plaintext keys are migrated transparently on the next start.

### Fixed
- **Windows build** — `internal/tlscert` used `syscall.O_NOFOLLOW`, which is
  undefined on Windows and broke the cross-compiled Windows release binary. The
  flag is now applied via a platform constant (`O_NOFOLLOW` on Unix, no-op on
  Windows).

## [1.4.4] — 2026-06-15

### Fixed
- **The APT repository is now actually published.** In v1.4.3 the apt job failed
  to import the signing key (`gpg: no valid OpenPGP data found`) because the
  armored key in the `APT_GPG_PRIVATE_KEY` secret had lost its armor lines on
  paste. The key is now stored base64-encoded and decoded in CI, so a multi-line
  block can't be mangled — `apt install dockercmd` works from the signed repo.

## [1.4.3] — 2026-06-15

### Fixed
- **Release pipeline now actually publishes the `.deb`/`.rpm` packages and the
  APT repository.** In v1.4.2 these were lost: the packaging/apt jobs tried to
  add assets to an already-created **immutable** release and hit
  `422 Cannot upload assets to an immutable release`, which failed packaging and
  skipped the apt publish. The packages are now built in the `release` job and
  uploaded in the single `gh release create`, and `apt` publishes from the
  finished release. (Binaries, signatures, the container image and the Homebrew
  tap were unaffected in v1.4.2.)

## [1.4.2] — 2026-06-15

### Added
- **Homebrew tap** — install via `brew install koduj-dev/tap/dockercmd` (macOS &
  Linux/Linuxbrew). The formula pulls the signed release binary for your
  OS/arch; a release job renders it from `deploy/homebrew/dockercmd.rb.tmpl` and
  pushes it to [koduj-dev/homebrew-tap](https://github.com/koduj-dev/homebrew-tap)
  on each tag (when the `HOMEBREW_TAP_TOKEN` secret is set).
- **Debian/Ubuntu `.deb` and Fedora/RHEL `.rpm` packages** — built per release
  (amd64 + arm64) with `nfpm` and attached to the GitHub release. Each installs
  the binary, a hardened systemd unit, the man page and an
  `/etc/docker-commander/commander.conf` conffile, and sets up the `dockercmd`
  service.
- **Signed APT repository** — `apt install dockercmd` from a GPG-signed repo
  served on GitHub Pages (<https://koduj-dev.github.io/apt>). A release job adds
  the new `.deb` (after verifying its provenance) to the
  [koduj-dev/apt](https://github.com/koduj-dev/apt) archive with `reprepro` and
  re-signs it (gated on the `APT_REPO_TOKEN` + `APT_GPG_PRIVATE_KEY` secrets).

## [1.4.1] — 2026-06-15

### Added
- **`--help`, `--version`, and a `man` page** — `dockercmd --help` (and `-h`)
  now prints a complete usage: a synopsis, the **standalone actions**
  (`--version`, `--self-upgrade`, and `--install-service` /
  `--uninstall-service` / `--service-status` — previously undiscoverable because
  they're parsed before the flag set) and every option. `dockercmd --version`
  (or `dockercmd version`) prints the build version. Installing as a service now
  also installs a **`man dockercmd`** page — embedded in the binary, kept in
  sync with the flags by a test, and written by both `--install-service` and the
  `install-linux.sh` / `install-macos.sh` scripts.
- **Official container image** — a multi-arch (amd64/arm64) image published to
  **`ghcr.io/koduj-dev/docker-commander`** on each release. Built from a
  distroless base (no shell/package manager), it runs as a non-root user and
  embeds the UI; mount the Docker socket and a `/data` volume to run it. See the
  README Quick start.
- **Signed releases, SBOM & build provenance** — release artifacts now carry a
  keyless **cosign** signature over `SHA256SUMS` (`SHA256SUMS.sig` / `.pem`), an
  SPDX **SBOM** (`dockercmd.sbom.spdx.json`), and per-binary SLSA build
  **provenance** (`gh attestation verify …`); the container image gets provenance
  and SBOM attestations and is cosign-signed too. Verification steps are in the
  README.
- **`go install` support** — documented `go install
  github.com/koduj-dev/docker-commander/cmd/dockercmd@latest` as an install path.

### Security
- **Supply-chain hardening of the release pipeline** — every GitHub Actions step
  is now **pinned by commit SHA** (a moved tag can't slip code into a run that
  holds `id-token` / `attestations` / `packages` write); the cosign signature
  over `SHA256SUMS` also **covers the SBOM**; the container image is signed
  **recursively** (`cosign sign -r`, so each per-platform manifest is signed, not
  just the index); and the README's `docker run` is hardened (`--read-only`,
  `--cap-drop ALL`, `--security-opt no-new-privileges`, localhost bind, digest
  pinning) with an explicit warning that mounting the Docker socket grants
  host-root-equivalent access.

## [1.4.0] — 2026-06-15

### Security
- **Client IP is no longer spoofable** — the app previously used chi's
  `middleware.RealIP`, which trusts `X-Forwarded-For` / `X-Real-IP` from **any**
  client (chi now deprecates it as spoofable). Because every IP-based control —
  login & OAuth **rate limits**, the **loopback 2FA exemption**, and **audit**
  records — keys on the client address, a remote attacker could forge it: claim
  `127.0.0.1` to skip 2FA, or rotate `X-Forwarded-For` values to evade
  brute-force throttling. Forwarded headers are now honoured **only** from
  proxies you explicitly trust via **`DC_TRUSTED_PROXIES`** (IPs/CIDRs); with
  none set (the default) the real TCP peer is used and forwarded headers are
  ignored. The client IP is also normalised to a bare IP, fixing previously
  port-keyed (ineffective) rate limiting on direct connections. **If you run
  behind a reverse proxy, set `DC_TRUSTED_PROXIES`** so the real client IP is
  recovered — see [deployment](docs/deployment.md).

### Added
- **Configurable stats sampling interval** (`DC_METRICS_INTERVAL`, default `15s`)
  — the monitor samples every running container's stats on this interval to feed
  the charts/history and resource alert rules. On a host with **many containers**
  the sweep can dominate CPU (on the app and the Docker daemon); raising the
  interval is the first lever. Previously hard-coded.
- **Optional profiling endpoints** (`DC_PPROF=1`) — serves Go's `net/http/pprof`
  on a **dedicated `127.0.0.1:6060` listener** (separate from the main port, so
  it's physically unreachable off-box and immune to `X-Forwarded-For` spoofing),
  for diagnosing CPU/allocation/goroutine issues with `go tool pprof`. Off by
  default. See [deployment → diagnosing high CPU](docs/deployment.md).
- **Per-host reachability monitoring + alerts** — the engine now pings every
  enabled host on an interval and tracks whether its Docker daemon is reachable.
  The **Hosts** page and the sidebar **host switcher** show a 🔴 *unreachable*
  badge, and a host going **offline** (or **recovering**) raises an alert in the
  in-app feed and, when SMTP is configured, an email — honouring the host's
  per-host alert recipient. This watch is automatic; it needs no alert rule. The
  first probe never alerts, so an already-down host at startup stays quiet until
  it actually changes state.
- **Section-gated live stream** — the shared `/api/ws` WebSocket that streams
  container **stats** and **logs** is now checked against the user's **live
  RBAC** per subscription. It was previously open to any authenticated user;
  both channels now require the `containers` section (every consumer needs it to
  resolve a container anyway), so a user without it can no longer stream a
  container's data. Backed by an adversarial test.
- **MCP Admin overview** — a new admin-only page (**System → MCP Admin**) giving
  a fleet-wide view of MCP credentials: **every user's** active API tokens
  (annotated with the owner) and all registered **OAuth clients**, with the
  ability to **revoke** any token or **remove** any client (the latter purges its
  codes and refresh tokens too). Secrets are never exposed — only metadata. Gated
  by the `__admin` section; backed by store/handler unit tests and adversarial
  pen tests (admin-gating, IDOR, secret-leak). Makes MCP team-ready.
- **Remote control from AI tools (MCP)** — an optional, **off-by-default**
  **Model Context Protocol** server (`DC_MCP_ENABLED`) so AI tools (**Claude
  Code**, **Claude Desktop**, **Cursor**) can monitor and *safely* operate Docker
  **as the authenticated user**. ~20 tools — read (containers, logs, images,
  projects, volumes, networks, stats, metrics history, events, audit) and *safe*
  control (start/stop/restart a container, deploy/down a managed project) — plus
  MCP **resources** (container inventory, compose files) and **prompts** (curated
  ops workflows). Two auth paths: a **bearer API token** from a self-service
  **MCP Access** page (Claude Code / scripts), or a self-contained **OAuth 2.1**
  server — PKCE, dynamic client registration, audience-bound access tokens with
  rotating refresh tokens — for Claude Desktop / Cursor (needs
  `DC_MCP_PUBLIC_URL`). Every call reuses the app's **RBAC**, and a token can only
  **narrow** its owner's rights (a section subset + read-only). Container env
  vars, audit detail and event attributes are kept out of tool output; there is
  deliberately **no exec, image export, volume-content read, prune or remove**.
  Backed by unit, end-to-end runtime-smoke, and adversarial pen tests. Serve
  behind HTTPS. See [docs/mcp.md](docs/mcp.md).
- **Docker image autocomplete** — typing an image reference now suggests names
  and tags: in the **compose editor** (on `image:` lines) and in the **Create
  container** form. Suggestions blend the host's locally-pulled images (instant,
  offline) with a **Docker Hub** repository search (proxied through the host
  daemon, so no credentials leave the process) and, after a `:`, Docker Hub's
  **tag** list. Everything degrades to local-only when offline.
- **Builder — service instances & clusters** — a block can now be added **more
  than once**, each as a service **instance** with its own editable key (`db`,
  `db-2`, …) and its own named volumes (auto-de-duplicated), so you can build a
  cluster of the same service. The live preview now also **validates** the
  assembled compose (`docker compose config`) and shows valid / warnings /
  invalid.
- **Builder — shared definitions (YAML anchors)** — the builder can now include
  reusable **top-level anchors** (e.g. `x-pg-common: &pg-common …`) emitted above
  `services:`, so a cluster of services can share one definition (security, cert
  mounts, …). Pick which services **merge** each anchor (a `<<: *pg-common` is
  injected per instance) — including into otherwise read-only built-in services.
  Pick built-in anchors (Service defaults, Secured Postgres) or save your own;
  they're managed on the Templates page like service blocks.
- **Templates management page** — a new **Templates** section (under Projects'
  permission) to manage presets, builder service blocks and shared definitions
  in one place: edit a user preset's files in the multi-file editor, rename it,
  add/edit/delete your own service blocks and shared definitions, inspect
  built-in ones read-only, and **duplicate** any preset, service block or shared
  definition (including built-ins) into a new editable copy. The **New project**
  dialog now shows a **live read-only preview** of the `compose.yml` a template
  or builder selection would produce, and the project editor opens wider.
- **Project templates & builder** — creating a project now offers three ways to
  scaffold it, all rendered server-side: **Template** (ready-made presets —
  Nginx static, Nginx + Postgres + Adminer, LEMP, Node + Postgres + Redis — with
  fill-in **variables** and auto-generated secrets), **Builder** (the *skládačka*:
  tick service blocks — Nginx, PHP, Node, Postgres, MySQL, Redis, Adminer — and
  they're merged into one compose), and **Import** (`.zip`). **Save as preset**
  snapshots a project into a reusable preset, and you can add your own service
  blocks to the builder. Built-in presets/blocks are embedded; user-saved ones
  live in the data dir (the catalog is structured for a future remote source).
- **Self-install as a service** — `dockercmd --install-service` sets the binary
  up as a **systemd** service (Linux) or a per-user **launchd** LaunchAgent
  (macOS), with `--uninstall-service` and `--service-status`. Equivalent
  idempotent installer scripts also ship in `deploy/` (`install-linux.sh`,
  `install-macos.sh`, and `install-windows.ps1` via a Scheduled Task).

### Changed
- **Sidebar navigation** — the *Compute* group is split into **Workloads**
  (Containers, Stacks, Projects, Templates) and **Storage** (Images, Volumes) so
  the menu stays scannable as the feature set grows.

### Fixed
- **Bind-mounted project files were unreadable in containers** — seeded and edited
  project files were written `0600` / dirs `0700` owned by the service user, so a
  container running as a non-root uid (e.g. Nginx's worker, PHP-FPM's www-data)
  got `Permission denied` on bind-mounted files — the `nginx-static`/LEMP presets
  failed to serve `./html` / `./app`. Project files are now `0644` and their dirs
  `0755` (confinement stays at the data dir, which is `0700`). Existing projects
  created before this fix need to be re-created to pick up the new permissions.
- **Compose / Projects under systemd** — the `docker compose` CLI was reported as
  unavailable (Deploy/Down disabled, with a warning) when Docker Commander ran
  under the hardened systemd unit. `ProtectHome=true` makes the service user's
  `~/.docker` inaccessible, which breaks the docker CLI's plugin discovery; the
  unit now sets `DOCKER_CONFIG` to a writable path so the compose plugin is found.

## [1.3.0] — 2026-06-08

### Added
- **Self-update** — an admin **"update available"** banner that checks the GitHub
  Releases API against the running version (cached; `DC_UPDATE_CHECK=0` disables
  the outbound call), plus a **`dockercmd --self-upgrade`** command that downloads
  the right OS/arch asset, **verifies its SHA-256**, and atomically replaces the
  running binary. `--self-upgrade --check` reports whether an update is waiting
  without installing it.
- **Volume file browser** — browse, upload, download, delete and create folders
  inside a named volume (via a short-lived helper container, so it works on
  local / TCP / SSH hosts). **Upload & extract** a `.zip` / `.tar` / `.tar.gz`
  into a volume or container, and **seed a new volume** from an archive.
- **Project editor — real code editing & validation:**
  - A **CodeMirror 6** editor with YAML / JSON / shell / Dockerfile / `.conf`
    highlighting (replacing the bare textarea).
  - **Live, inline validation** of the *unsaved* buffer: compose via
    `docker compose config` (anchors, merge keys, `${VAR}` interpolation and
    `extends`/`include` resolved) shown as diagnostics on the right line;
    instant YAML / JSON / `.env` syntax lint; **Dockerfile** lint via
    `docker build --check`.
  - Compose **warnings** (unset variables), a **Resolved** preview (the fully
    flattened compose), and a **Summary** overview (services / ports / volumes +
    a duplicate-host-port check).
  - **Binary/data files** can live alongside the compose file (raw upload,
    download-only in the tree).
  - **New-project templates** — Nginx, Nginx + PHP-FPM, Postgres + Adminer, Node.
- **Networks — full management** — **create** (driver, subnet, gateway, internal,
  attachable), **connect** / **disconnect** containers, and **prune** unused
  networks; plus search / status filter on the list.
- **Topology at scale** — a **Find container** search, **filter by compose
  stack**, a **force-directed 2D layout** (instead of one tall column), a compact
  **list view** (with published ports), and a node-count badge. The network
  detail reuses the same graph/list renderers.
- **Confirmation dialogs** for every destructive action (delete / remove /
  prune), app-wide, replacing one-click and `window.confirm`.

### Changed
- Topology defaults to **running containers only** and **hides empty networks**;
  its filters persist across reloads. Edges are straight, animated lines.
- Anonymous (hash-named) volumes are shown shortened (full name on hover); the
  in-app confirm/prompt dialog is wider.

### Fixed
- Deterministic ordering for the container / network / volume / image / topology
  lists (tie-break beyond a case-folded name).
- Project file sandbox rejects a symlink anywhere along the path; archive extract
  guards against zip-slip and zip-bombs; the extract endpoints bound the request
  body. `ComposeAvailable` no longer caches a transient failure for the process
  lifetime.

## [1.2.0] — 2026-06-05

### Added
- **Compose stacks (discover & manage)** — a Stacks view that groups containers
  by their `com.docker.compose.project` label (so stacks started with the
  `docker compose` CLI show up too), with start / stop / restart / remove for a
  whole stack and a read-only **view of the stack's compose file** (read from
  the host — directly for the local daemon, over SSH for ssh hosts). A status
  LED, filter (name / service / image, by state), collapse/expand, and a
  cursor-following hover card with ports.
- **Compose projects** — create and edit a managed project *folder* (a compose
  file plus sidecar configs / scripts / init files) in a built-in multi-file
  **tree editor**, then **deploy it with the host's `docker compose` CLI** —
  including selecting **compose profiles** to enable. Import/export a project as
  a `.zip`, redeploy, and bring it down. Deployed projects appear on the Stacks
  page (lifecycle + view-compose reused) and link back and forth. Targets the
  local Docker host; Deploy/Down are disabled when the compose CLI isn't present.
- **Disable a host** — toggle a host off so the monitor ignores it entirely (no
  events stream, no stats sampling) and it's dropped from the host switcher —
  e.g. for a laptop/host that's offline. The Hosts page shows a `disabled` badge
  and an enable/disable button.
- App-wide UX: in-app confirm / prompt / alert dialogs (replacing the browser
  ones), each page header shows its section icon, and the sidebar logo links to
  the Dashboard.

### Fixed
- UI slowness on hosts with many containers: the dashboard resource overview no
  longer re-samples every container on demand (each `docker stats` call costs
  ~1s) — it reads the monitor's background snapshot, and the stats sweep runs
  less often. Also, an unreachable host no longer stalls the stats poll or spams
  reconnects (timeouts + exponential backoff; disable it to skip it entirely).
- Stable, alphabetical ordering for Containers (running first, then A→Z),
  Images, Volumes, Networks and Topology — they previously came back in the
  daemon's arbitrary order (which shuffled on reload).
- Dashboard "Open ports" no longer shows ports of containers that have since
  stopped — the cached scan is filtered to the currently-running containers and
  refreshes on Docker lifecycle events.
- Restored the pointer cursor on buttons (Tailwind v4 had dropped it).

### Changed
- Upgraded the frontend stack to current majors: **React 19**, **Vite 8**,
  `@vitejs/plugin-react` 6, **React Router 7**, **Tailwind CSS 4** (and the
  GitHub Actions to v6). No behavioural changes intended.

### Project / infrastructure
- Community health files: Code of Conduct, contributing guide, security policy,
  and issue / pull-request templates.
- Dependabot for Go modules, npm and GitHub Actions (weekly, grouped).

## [1.1.0] — 2026-06-04

### Added
- **Host detail** — an info panel per host (hardware, OS/kernel, Docker engine
  config, container/image counts), with a note that Docker Desktop reports its
  Linux VM, not the desktop OS.
- **Discoverable host switching** — the sidebar "Viewing host" switcher is now
  prominent and every page header shows the active host, so multi-host views are
  clearly separated.
- **2FA choice at first-run setup** — enable 2FA now (enrollment follows) or
  defer it (localhost stays password-only; toggle it later in Settings).
- **Config file** — settings can live in `/etc/docker-commander/commander.conf`
  (`%ProgramData%\…` on Windows); override with `-config` / `$DC_CONFIG`.
  Precedence: flag → env → file → default.
- **Listen address as host + port** — `DC_HOST` / `DC_PORT` (and a `-p`
  shorthand); the full `DC_ADDR` is kept as a legacy override.
- **Native HTTPS** — set `DC_TLS_CERT` + `DC_TLS_KEY` to serve TLS directly,
  without a reverse proxy.
- **Dashboard: resource breakdown** — pie charts of each running container's
  share of the host CPU and memory.
- **Dashboard: open-ports scan** — a host-wide map of published ports with
  **active service fingerprinting** (SSH / HTTP(S) / SMTP / Redis / TLS /
  banner); SSH hosts are probed through their tunnel. Per-container probing is
  also available on the container detail page.
- **Health & version** — unauthenticated `GET /healthz` (alias `/health`) for
  load balancers / k8s; build version shown in the sidebar footer and at
  `GET /api/version`.
- **Alerts in the system log** — every fired alert is written to stderr as a
  structured line, so failures reach the journal / syslog, not just the in-app
  feed.
- **List filters** — status filters (running·stopped, in-use·unused) for
  Containers, Images and Volumes.
- **Per-user preferences** — list filters, status and page size are stored
  server-side, so they follow the account across browsers.
- **Audit pagination** — search + prev/next paging over the audit log.
- **Scroll restoration** — returning from a detail page lands where you were.
- **Near-real-time dashboard** — refreshes are driven by the Docker events
  stream, so containers starting/stopping show up almost immediately.

### Changed
- Default listen port `8080` → **`8470`** (less likely to collide).
- Configuration consolidated on the single config file (the separate systemd
  env example was removed).
- Stronger guidance for remote hosts: prefer **SSH**, and TLS/firewall warnings
  for exposing the Docker daemon over TCP.

### Fixed
- **SSH hosts now connect** — the Docker-over-SSH transport failed with
  `lookup docker.ssh … no such host` because `client.WithHost` clobbered the
  tunnel's `DialContext`; option order is fixed.
- **Dashboard no longer crashes** when no containers are running (Go `nil` slice
  → JSON `null` → `null.length`).
- **Remote port scans no longer hang** — the SSH-tunnelled dialer now honours a
  timeout.
- Dark-theme `<select>` dropdowns no longer render white-on-white.
- Pie-chart tooltip text is readable on the dark theme.
- The resource-usage section reserves its space (no layout jump) and shows
  errors in place of the charts.

### Tooling / tests
- Added a unit + integration test suite (~66% coverage). CI runs `go test
  -short` (deterministic); Docker/Redis/LDAP/SMTP integration tests are gated
  behind `testing.Short()`. GitHub Actions bumped to the Node-24 majors.

## [1.0.0] — 2026-06-02

Initial release: a single CGO-free Go binary with an embedded React UI.

- **Monitoring** — live CPU/memory graphs, historical charts (Redis/in-memory),
  aggregated logs with level detection, regex search and parse rules, events
  feed, diff/top/df, raw inspect.
- **Control** — full container lifecycle, in-container file browser (`docker
  cp`), images (pull/build/push/tag/save/load/import/history/prune), volumes &
  networks CRUD, interactive shell.
- **Multi-host** — local / TCP+TLS / SSH daemons with verified host keys.
- **Alerting** — state / resource / log / restart rules → webhooks, email
  (SMTP, per-host), in-app feed, Prometheus `/metrics`.
- **Security & admin** — Argon2id + TOTP 2FA, multi-user with roles /
  per-section permissions / read-only, feature flags, audit log, optional LDAP;
  secrets encrypted at rest.

[1.6.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.6.0
[1.5.1]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.5.1
[1.5.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.5.0
[1.4.4]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.4
[1.4.3]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.3
[1.4.2]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.2
[1.4.1]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.1
[1.4.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.4.0
[1.3.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.3.0
[1.2.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.2.0
[1.1.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.1.0
[1.0.0]: https://github.com/koduj-dev/docker-commander/releases/tag/v1.0.0
