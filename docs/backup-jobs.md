# Backup jobs

[← Manual index](README.md)

![Backup jobs](images/backup_jobs.png)

**Backup jobs** (sidebar → **Storage** → **Backup jobs**, admin only) run your
own backup command against a volume's or a project's data. They run on a
schedule or on demand and record whether the command worked.

Docker Commander is not a backup engine. You supply the tool (restic, borg,
`rsync`, a script) in a container image, already configured with its own
destination. Docker Commander mounts the data, runs the command and keeps the
result.

## How a run works
Each run starts a short-lived helper container on the job's host:

1. The image is pulled if the host does not have it.
2. The container starts with the volume(s) mounted and your environment set. It
   runs `sh -c "<command>"`, so the image needs `sh`.
3. Docker Commander waits for the exit, reads stdout and stderr, and removes
   the container, also after a failure or timeout.

The container is labelled `dc.backupjob=1`. It has no extra privileges and no
host bind mounts. Volumes are not mounted read-only.

| Scope | Runs on | Mounted |
|-------|---------|---------|
| **Single volume** | the host you pick (`local` or a remote host) | the volume at `/data` |
| **Project (all its volumes)** | the project's host | every named volume Compose created for the project, each at `/data/<volume name>` |

For a project, volumes are found through the `com.docker.compose.project`
label. The host is resolved on every run, so a project that moves is followed.
A project with no named volumes fails the run.

A run succeeds only on exit code 0. Any other exit code fails. So does anything
that stops the command from running: image pull failure, helper start failure
or an unresolvable target. These failures appear in the run history.

## Creating a job
**New job** opens the form:

- **Job name**.
- **Scope**: *Single volume* (pick the host, type the volume name) or *Project*.
- **Schedule (minutes, 0 = manual only)**.
- **Helper image**, for example `restic/restic`.
- **Command**, for example `restic backup /data`. In a project job the volumes
  are under `/data/<volume name>`, so `/data` covers all of them.
- **Environment**: one `KEY=VALUE` per line, for the command's credentials. A
  line without `=` is ignored.

New jobs are enabled.

### The environment is write-only
The environment is encrypted at rest and never returned by the list, the edit
form or the API. When you edit a job:

- A blank Environment box keeps the stored values.
- New lines replace the whole stored environment.
- **Clear stored environment** deletes the saved credentials. A blank box does
  not.

## Schedule and Run now
**Schedule** is an interval in minutes, not a cron expression. `0` means manual
only.

The scheduler checks once a minute. A job is due when it is enabled, has an
interval, and has never run or last finished at least one interval ago. A
failed run counts as a run. Due jobs run one after another.

- **Run now** (play button) runs the job at once, even if it is disabled.
  Closing the tab does not cancel the run.
- The **Enabled** toggle switches scheduled runs on and off.
- A run is stopped after 30 minutes, including the image pull. It is recorded
  as failed with a timeout error and no output.
- At most two backup helpers run at once across the instance. A third fails
  with "too many backup jobs running — try again shortly".

## The job list
Each row shows the name, the target (`volume:<name> @ <host>` or
`project:<name>`), the schedule, the last run (badge and time), the enabled
toggle and the actions **Run now**, **Run history & logs**, **Edit** and
**Delete**.

The badge reads **never run**, **ok** or **failed**. **ok** and **failed** open
the run history. Hover **failed** to see the error.

## Run history
**History** (or the status badge) opens **Run history**: one line per run with
result, start time, duration, who started it and exit code. The newest run is
expanded. Click a line to expand another.

An expanded run shows the error (for example `exit code 2`, a pull failure or a
timeout) and the captured output, or "The command printed nothing".

- **Trigger** is `scheduled` for scheduler runs and `by <username>` for
  **Run now**.
- A run under one second shows no duration.
- If a **Run now** fails, the history opens by itself. If it succeeds, it does
  not.

### What is stored
- Each run keeps the first 256 KiB of output. The rest is dropped.
- The latest 200 runs per job are kept. The dialog lists the newest 50.
- Deleting a job deletes its run history.

Output is stored in Docker Commander's database. Do not print secrets.
Environment values are not redacted if the command echoes them.

## Access and audit
Backup jobs are admin-only, on the page and on every `/api/backup-jobs`
endpoint. You cannot grant them per section to other roles.

Changes are recorded in the [audit log](audit.md): `backup_job.create`,
`backup_job.update`, `backup_job.enable`, `backup_job.disable`,
`backup_job.delete` and `backup_job.run` (a **Run now**). Scheduled runs are
not audit events. They appear in the run history. Environment values are never
audited.

## Tips
- Backup jobs cover volume data. The [recovery bundle](recovery.md) covers
  configuration and contains no volume data.
- The volume name is not checked on save. Docker creates a missing volume on
  mount, so a typo backs up an empty volume. Copy the name from
  [Volumes](volumes.md).
- Try the job with **Run now** and read the output before relying on the
  schedule.
- Use a volume's [file browser](volumes.md#file-browser) to check what a
  restore produced.
