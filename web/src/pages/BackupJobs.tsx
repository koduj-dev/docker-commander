import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, Pencil, Play } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { BackupJob, BackupJobInput, Host, Project } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";

// Volume backup jobs: a trigger-and-status wrapper around a user-supplied
// backup command (their own restic/borg/etc, already pointed at its own
// repository), run against a volume's or project's data via a short-lived
// Docker helper container — never a backup engine of our own. Modeled
// directly on Alerts' Rules tab (list + inline create/edit form + enabled
// toggle + edit/delete), the closest existing "list of per-item configs with
// schedule + status" surface.

function Loading() {
  return <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;
}

function statusBadge(job: BackupJob) {
  if (!job.lastRunAt) return <span className="text-xs bg-panel2 text-muted rounded-md px-2 py-0.5">never run</span>;
  return job.lastRunOk ? (
    <span className="text-xs bg-ok/15 text-ok rounded-md px-2 py-0.5">ok</span>
  ) : (
    <span className="text-xs bg-danger/15 text-danger rounded-md px-2 py-0.5" title={job.lastRunDetail}>failed</span>
  );
}

function scheduleLabel(job: BackupJob): string {
  return job.intervalMinutes > 0 ? `every ${job.intervalMinutes}m` : "manual";
}

export function BackupJobs() {
  const [jobs, setJobs] = useState<BackupJob[] | null>(null);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState<BackupJob | null>(null);
  const [running, setRunning] = useState<Set<number>>(new Set());
  const dialogs = useDialogs();

  const load = useCallback(() => {
    api.backupJobs().then(setJobs).catch(() => setJobs([]));
    api.hosts().then(setHosts).catch(() => {});
    api.projects().then((r) => setProjects(r.projects)).catch(() => {});
  }, []);
  useEffect(() => load(), [load]);

  const toggle = async (j: BackupJob) => {
    await api.toggleBackupJob(j.id, !j.enabled);
    load();
  };
  const del = async (j: BackupJob) => {
    if (!(await dialogs.confirm({ title: "Delete backup job", message: <>Delete the backup job <code className="font-mono text-text">{j.name}</code>? Its run history will be deleted too.</>, danger: true, confirmLabel: "Delete" }))) return;
    await api.deleteBackupJob(j.id);
    load();
  };
  const runNow = async (j: BackupJob) => {
    setRunning((prev) => new Set(prev).add(j.id));
    try {
      await api.runBackupJob(j.id);
    } finally {
      setRunning((prev) => { const n = new Set(prev); n.delete(j.id); return n; });
      load();
    }
  };

  const hostName = (id: number) => hosts.find((h) => h.id === id)?.name ?? (id ? `#${id}` : "local");
  const projectName = (id: number) => projects.find((p) => p.id === id)?.name ?? `#${id}`;

  if (!jobs) return <Loading />;

  return (
    <>
      <PageHeader
        title="Backup jobs"
        actions={
          <button className="btn-primary" onClick={() => { setEditing(null); setShowForm((v) => !v); }}>
            <Plus className="h-4 w-4" /> New job
          </button>
        }
      />
      <div className="p-6 space-y-4">
        <p className="text-sm text-muted">
          Trigger your own backup command (restic, borg, or anything else already pointed at its own repository)
          against a volume's or project's data, on a schedule or on demand. Not a backup engine — Docker Commander
          only runs the command and records whether it succeeded.
        </p>
        {(showForm || editing) && (
          <BackupJobForm
            key={editing?.id ?? "new"}
            hosts={hosts}
            projects={projects}
            existing={editing}
            onDone={() => { setShowForm(false); setEditing(null); load(); }}
          />
        )}
        {jobs.length === 0 ? (
          <EmptyState title="No backup jobs" hint="Create one to start tracking backup status for a volume or project." />
        ) : (
          <div className="card overflow-hidden">
            <table className="w-full text-sm">
              <thead className="text-muted text-xs uppercase tracking-wide">
                <tr className="border-b border-border">
                  <th className="text-left font-medium px-4 py-3">Name</th>
                  <th className="text-left font-medium px-4 py-3">Target</th>
                  <th className="text-left font-medium px-4 py-3">Schedule</th>
                  <th className="text-left font-medium px-4 py-3">Last run</th>
                  <th className="text-center font-medium px-4 py-3">Enabled</th>
                  <th className="px-4 py-3"></th>
                </tr>
              </thead>
              <tbody>
                {jobs.map((j) => (
                  <tr key={j.id} className="border-b border-border/50">
                    <td className="px-4 py-2.5 font-medium">{j.name}</td>
                    <td className="px-4 py-2.5 font-mono text-xs text-muted">
                      {j.scope === "volume" ? `volume:${j.volumeName} @ ${hostName(j.hostId)}` : `project:${projectName(j.projectId)}`}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-muted">{scheduleLabel(j)}</td>
                    <td className="px-4 py-2.5">
                      <div className="flex items-center gap-2">
                        {statusBadge(j)}
                        {j.lastRunAt && <span className="text-xs text-muted">{new Date(j.lastRunAt).toLocaleString()}</span>}
                      </div>
                    </td>
                    <td className="px-4 py-2.5 text-center">
                      <button onClick={() => toggle(j)} className={clsx("relative w-9 h-5 rounded-full transition-colors", j.enabled ? "bg-accent" : "bg-border")}>
                        <span className={clsx("absolute top-0.5 h-4 w-4 rounded-full bg-white transition-all", j.enabled ? "left-4" : "left-0.5")} />
                      </button>
                    </td>
                    <td className="px-4 py-2.5 text-right">
                      <div className="flex items-center justify-end gap-1">
                        <button className="btn-ghost px-2 py-1" title="Run now" disabled={running.has(j.id)} onClick={() => runNow(j)}>
                          {running.has(j.id) ? <Spinner className="h-4 w-4" /> : <Play className="h-4 w-4" />}
                        </button>
                        <button className="btn-ghost px-2 py-1" title="Edit" onClick={() => { setShowForm(false); setEditing(j); }}><Pencil className="h-4 w-4" /></button>
                        <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(j)}><Trash2 className="h-4 w-4" /></button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

// envToText / textToEnv round-trip the env map through a plain "KEY=VALUE per
// line" textarea — the simplest editable shape for a handful of secrets, and
// consistent with env being write-only (a saved job never comes back with a
// prefilled textarea; editing means retyping it, same as a registry password).
function textToEnv(text: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const i = trimmed.indexOf("=");
    if (i <= 0) continue;
    env[trimmed.slice(0, i).trim()] = trimmed.slice(i + 1);
  }
  return env;
}

function BackupJobForm({
  hosts, projects, existing, onDone,
}: { hosts: Host[]; projects: Project[]; existing?: BackupJob | null; onDone: () => void }) {
  const [name, setName] = useState(existing?.name ?? "");
  const [scope, setScope] = useState<"volume" | "project">(existing?.scope ?? "volume");
  const [volumeName, setVolumeName] = useState(existing?.volumeName ?? "");
  const [hostId, setHostId] = useState<number>(existing?.hostId ?? 0);
  const [projectId, setProjectId] = useState<number>(existing?.projectId ?? projects[0]?.id ?? 0);
  const [image, setImage] = useState(existing?.image ?? "restic/restic");
  const [command, setCommand] = useState(existing?.command ?? "");
  const [intervalMinutes, setIntervalMinutes] = useState(existing?.intervalMinutes ?? 0);
  const [envText, setEnvText] = useState("");
  const [clearEnv, setClearEnv] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const body: BackupJobInput = {
        name, scope, image, command, intervalMinutes,
        volumeName: scope === "volume" ? volumeName : undefined,
        hostId: scope === "volume" ? hostId : undefined,
        projectId: scope === "project" ? projectId : undefined,
        env: clearEnv ? undefined : textToEnv(envText),
        clearEnv,
      };
      if (existing) await api.updateBackupJob(existing.id, body);
      else await api.createBackupJob({ ...body, enabled: true });
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save the backup job.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label className="label">Job name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div>
          <label className="label">Scope</label>
          <select className="input" value={scope} onChange={(e) => setScope(e.target.value as "volume" | "project")}>
            <option value="volume">Single volume</option>
            <option value="project">Project (all its volumes)</option>
          </select>
        </div>
        <div>
          <label className="label">Schedule (minutes, 0 = manual only)</label>
          <input className="input" type="number" min={0} value={intervalMinutes} onChange={(e) => setIntervalMinutes(+e.target.value)} />
        </div>
      </div>

      {scope === "volume" ? (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="label">Host</label>
            <select className="input" value={hostId} onChange={(e) => setHostId(+e.target.value)}>
              <option value={0}>local</option>
              {hosts.filter((h) => h.id !== 0).map((h) => (
                <option key={h.id} value={h.id}>{h.name}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="label">Volume name</label>
            <input className="input" value={volumeName} onChange={(e) => setVolumeName(e.target.value)} required placeholder="my_app_data" />
          </div>
        </div>
      ) : (
        <div>
          <label className="label">Project</label>
          <select className="input" value={projectId} onChange={(e) => setProjectId(+e.target.value)} required>
            {projects.length === 0 && <option value={0}>— no projects —</option>}
            {projects.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </select>
          <span className="block text-xs text-muted mt-1">
            Backs up every named volume Docker Compose actually created for this project.
          </span>
        </div>
      )}

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label className="label">Helper image</label>
          <input className="input" value={image} onChange={(e) => setImage(e.target.value)} required placeholder="restic/restic" />
        </div>
        <div>
          <label className="label">Command (runs as <code className="text-xs">sh -c</code>)</label>
          <input className="input font-mono text-xs" value={command} onChange={(e) => setCommand(e.target.value)} required placeholder="restic backup /data" />
        </div>
      </div>

      <div>
        <label className="label">Environment (one KEY=VALUE per line, e.g. RESTIC_PASSWORD/RESTIC_REPOSITORY)</label>
        <textarea
          className="input font-mono text-xs h-24"
          value={envText}
          onChange={(e) => setEnvText(e.target.value)}
          disabled={clearEnv}
          placeholder="RESTIC_PASSWORD=...\nRESTIC_REPOSITORY=..."
        />
        <span className="block text-xs text-muted mt-1">
          Encrypted at rest and never shown again after saving — {existing ? "leave blank to keep the stored values" : "required if the command needs credentials"}.
        </span>
        {existing && (
          <label className="flex items-center gap-2 mt-2 text-xs text-muted">
            <input type="checkbox" checked={clearEnv} onChange={(e) => setClearEnv(e.target.checked)} />
            Clear stored environment (removes all saved credentials for this job — leaving the field above blank does
            <span className="font-medium"> not</span> clear it)
          </label>
        )}
      </div>

      {error && <p className="text-sm text-danger">{error}</p>}

      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Saving…" : existing ? "Save changes" : "Create job"}</button>
      </div>
    </form>
  );
}
