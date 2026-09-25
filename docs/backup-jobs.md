# Backup jobs

[← Manual index](README.md)

![Backup jobs](images/backup_jobs.png)

**Backup jobs** (sidebar → **Storage** → **Backup jobs**, admin only) trigger
*your own* backup command against a volume's or a project's data, on a schedule
or on demand, and record whether it worked.

This is **not a backup engine.** Docker Commander has no repository, retention
or storage logic of its own. You bring the tool (restic, borg, `rsync`, a shell
script — anything that runs in a container image and is already pointed at its
own destination); Docker Commander mounts the data, runs the command, and keeps
the result.

## How a run works
Each run starts a short-lived **helper container** on the job's host:

1. The **image** is pulled if it isn't already present on that host.
2. The container is created with the target volume(s) mounted, your
   **environment** set, and your **command** run as `sh -c "<command>"` — so the
   image must contain `sh`.
3. Docker Commander waits for it to exit, reads its combined stdout/stderr, and
   removes the container (also after a failure or a timeout).

The container is labelled `dc.backupjob=1` and uses the daemon's defaults —
nothing else is configured on it (no extra privileges, no host bind mounts).
The volumes are mounted without a read-only flag.

What gets mounted depends on the job's **scope**:

| Scope | Runs on | Mounted |
|-------|---------|---------|
| **Single volume** | the host you pick (`local` or a remote host) | the volume at `/data` |
| **Project (all its volumes)** | the project's own host | every named volume Compose created for the project, each at `/data/<volume name>` |

For a project, the volumes are found through Compose's own
`com.docker.compose.project` label, so the names are the real, runtime-prefixed
ones. The host is resolved fresh on every run, so a project that later moves to
another host is followed. A project with no named volumes has nothing to back
up: the run fails and says so.

**Success means exit code 0.** Any other exit code is a failed run, and so is
anything that stops the command from running at all (image pull failure, helper
container could not start, target could not be resolved). Those failures are
recorded in the run history too, so "why didn't this ever run" is answered in
the same place.

## Creating a job
**New job** opens the form:

- **Job name**.
- **Scope** — *Single volume* (pick the **host** and type the **volume name**)
  or *Project* (pick the project).
- **Schedule (minutes, 0 = manual only)** — see below.
- **Helper image** — for example `restic/restic`.
- **Command** — for example `restic backup /data`. For a project-scoped job
  the volumes are under `/data/<volume name>`; pointing the tool at `/data`
  covers all of them.
- **Environment** — one `KEY=VALUE` per line, for the credentials the command
  needs (for example `RESTIC_PASSWORD` and `RESTIC_REPOSITORY`). A line
  without `=` is ignored.

New jobs are created **enabled**.

### The environment is write-only
The environment is **encrypted at rest** and **never shown again** after
saving — the list, the edit form and the API never return it; only the runner
reads it. Consequently, when you **edit** a job:

- leaving the Environment box **blank keeps** the stored values, so changing an
  unrelated field never wipes your credentials;
- typing new lines **replaces** the whole stored environment;
- ticking **Clear stored environment** removes all saved credentials for the job
  (the box is disabled while it is ticked). Leaving the field blank does *not*
  clear it — the checkbox is the only way.

## Schedule and Run now
The **Schedule** field is a plain interval in minutes; `0` means the job only
runs when you trigger it (shown as *manual*; otherwise *every Nm*). This is an
interval, not a cron expression.

The scheduler checks once a minute. A job is due when it is **enabled**, has an
interval, and either has never run or its **last run finished** at least one
interval ago. A failed run counts as a run, so the next attempt comes one
interval later. Due jobs run one after another.

- **Run now** (the play button) runs the job immediately, ignoring the schedule.
  It works even when the job is disabled. The button shows a spinner until the
  run finishes, and the request is not cancelled if you close the tab — the
  run and its recorded result carry on independently.
- The **Enabled** toggle switches the *scheduled* runs on and off.
- A run is stopped after **30 minutes** (this covers the image pull, the
  command and reading its output). A timed-out run is recorded as failed with a
  timeout error and no output.
- At most **two** backup helpers run at once across the instance; a run that
  finds both slots busy fails immediately with "too many backup jobs running —
  try again shortly".

## The job list
Each row shows the name, the target (`volume:<name> @ <host>` or
`project:<name>`), the schedule, the **last run** (a status badge plus the
time), the enabled toggle, and the actions: **Run now**, **Run history & logs**,
**Edit**, **Delete**.

The badge reads **never run**, **ok** or **failed**. The **ok** and **failed**
badges are clickable and open the run history; hovering **failed** shows the
error, and clicking it gives you the full log.

## Run history
The **History** button (or the status badge) opens a job's **Run history**: one
line per run with the result, start time, duration, who started it and the exit
code. The newest run starts expanded; click a line to expand any other.

Expanding a run shows:

- the **error** (for example `exit code 2`, a pull failure, or a timeout), and
- the **captured output** — everything the command printed to stdout and stderr,
  or "The command printed nothing".

Other details:

- **Trigger** — `scheduled` for a run started by the scheduler, otherwise
  `by <username>` for a **Run now**.
- **Duration** is derived from second-resolution timestamps, so a run shorter
  than one second shows no duration.
- A **Run now** that fails does not vanish: the history opens by itself to show
  the log. That covers both a command that exited non-zero (the request itself
  succeeds, and the failure is read from what was recorded) and a run that could
  not be executed at all (the error is shown above the list). A successful
  **Run now** does not open the history.

### What is stored, and for how long
- Each run keeps its output up to **256 KiB** — the *beginning* of the output;
  anything beyond that is dropped. (Capture from the container itself stops at
  1 MiB.)
- Only the **latest 200 runs per job** are kept; older ones are pruned each time
  a run is recorded, so even a one-minute interval cannot grow the history
  without bound. The dialog lists the most recent 50 of them.
- **Deleting a job deletes its run history** too (the confirmation says so).
- The job row also holds the last run's time, result and error, so the list
  loads without reading the history.

Output lands in Docker Commander's database, so avoid commands that print
secrets. Environment values are *not* redacted from the output if the command
echoes them.

## Access and audit
Backup jobs are **admin-only** — the page, and every `/api/backup-jobs`
endpoint. A job is arbitrary command execution against your data plus a stored
credential blob, so it is treated like [Settings](settings.md) and users
management, and cannot be granted per-section to other roles.

Job changes are recorded in the [audit log](audit.md):
`backup_job.create`, `backup_job.update`, `backup_job.enable`,
`backup_job.disable`, `backup_job.delete` and `backup_job.run` (a **Run now**).
Scheduled runs are not audit events — they are recorded in the run history
instead. Environment values are never written to the audit log.

## Tips
- Backup jobs back up *volume data*. Configuration (projects and their files,
  hosts, registries, alert rules) is covered by the
  [recovery bundle](recovery.md), which carries no volume data — the two
  complement each other.
- The volume name is typed, not picked from a list, and is not checked when you
  save. Docker creates a volume that doesn't exist when it is mounted, so a typo
  would back up an empty volume rather than fail — copy the name from
  [Volumes](volumes.md).
- Test the command once with **Run now** and read the output before relying on
  the schedule.
- A volume's [file browser](volumes.md#file-browser) is a quick way to check
  what a restore produced.
