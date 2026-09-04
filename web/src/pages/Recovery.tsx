import { useEffect, useState } from "react";
import { Download, Upload, Loader2, AlertTriangle, CheckCircle2 } from "lucide-react";
import { api } from "../lib/api";
import type { Host, CompatibilityReport, RecoveryManifestSummary, RecoveryImportSummary } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { useDialogs } from "../components/Dialog";

// Portable recovery bundle: export everything the app knows (projects, hosts,
// registries, alert rules, image digests, and — opt-in — instance settings)
// into one file, and import it elsewhere. See NEXT.md's "Portable recovery
// bundle" and internal/api/recovery_bundle.go's package doc for the full
// design (why secrets are opt-in, why import never overwrites).

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export function Recovery() {
  const dialogs = useDialogs();
  const [hosts, setHosts] = useState<Host[]>([]);
  useEffect(() => { api.hosts().then(setHosts).catch(() => {}); }, []);

  return (
    <>
      <PageHeader title="Recovery bundle" />
      <div className="p-6 grid gap-6 max-w-3xl">
        <ExportPanel />
        <ImportPanel hosts={hosts} dialogs={dialogs} />
      </div>
    </>
  );
}

function ExportPanel() {
  const [includeSecrets, setIncludeSecrets] = useState(false);
  const [passphrase, setPassphrase] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const doExport = async () => {
    setBusy(true); setError("");
    try {
      const blob = await api.exportRecoveryBundle({ includeSecrets }, passphrase);
      downloadBlob(blob, `docker-commander-recovery-${new Date().toISOString().slice(0, 10)}.dcbundle`);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Export failed");
    } finally { setBusy(false); }
  };

  return (
    <section className="card p-5">
      <h2 className="text-sm font-semibold mb-1">Export</h2>
      <p className="text-sm text-muted mb-4">
        Every project&apos;s files, host and registry definitions, alert rules and image digests — no volume data.
      </p>
      <label className="flex items-center gap-2 text-sm mb-3">
        <input type="checkbox" checked={includeSecrets} onChange={(e) => setIncludeSecrets(e.target.checked)} />
        Include secrets (host TLS keys, registry passwords, webhook URLs, SMTP/LDAP passwords)
      </label>
      <p className="text-sm text-muted mb-3">
        Project files (e.g. <code>.env</code>) may carry their own secrets regardless of this
        checkbox — a passphrase is required whenever the export includes any project.
      </p>
      <label className="block text-sm mb-1">Passphrase (required unless the bundle carries no projects)</label>
      <input
        type="password"
        className="input w-full mb-4"
        placeholder="Required when the export includes project files or secrets"
        value={passphrase}
        onChange={(e) => setPassphrase(e.target.value)}
      />
      {error && <div className="text-sm text-danger mb-3">{error}</div>}
      <button className="btn btn-primary" disabled={busy} onClick={doExport}>
        {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
        Export bundle
      </button>
    </section>
  );
}

// InspectedKey identifies exactly what a compatibility check was run
// against — file identity + passphrase + target host. Import is only ever
// enabled when the CURRENT form state matches this exactly, so changing the
// target host (or the file, or the passphrase) after inspecting invalidates
// it rather than leaving a stale "looks fine" report on screen while
// importing against something that was never checked.
type InspectedKey = { file: File; passphrase: string; hostId: number | undefined };

function ImportPanel({ hosts, dialogs }: { hosts: Host[]; dialogs: ReturnType<typeof useDialogs> }) {
  const [file, setFile] = useState<File | null>(null);
  const [passphrase, setPassphrase] = useState("");
  const [hostId, setHostId] = useState<number | undefined>(undefined);
  const [applySettings, setApplySettings] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [inspection, setInspection] = useState<{ manifest: RecoveryManifestSummary; compatibility: CompatibilityReport } | null>(null);
  const [inspectedKey, setInspectedKey] = useState<InspectedKey | null>(null);
  const [summary, setSummary] = useState<{ result: RecoveryImportSummary; warnings: string[] } | null>(null);

  const reset = () => { setInspection(null); setInspectedKey(null); setSummary(null); setError(""); };

  const matchesInspection = !!inspectedKey && inspectedKey.file === file && inspectedKey.passphrase === passphrase && inspectedKey.hostId === hostId;

  const doInspect = async () => {
    if (!file) return;
    setBusy(true); setInspection(null); setInspectedKey(null); setSummary(null); setError("");
    try {
      const r = await api.inspectRecoveryBundle(file, passphrase, hostId);
      setInspection(r);
      setInspectedKey({ file, passphrase, hostId });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not read that bundle");
    } finally { setBusy(false); }
  };

  const doImport = async () => {
    if (!file || !matchesInspection) return;
    if (!(await dialogs.confirm({
      title: "Import recovery bundle?",
      message: "Hosts, registries, alert rules and projects are created new — an existing name/slug is skipped, never overwritten.",
      confirmLabel: "Yes, import", danger: true,
    }))) return;
    setBusy(true); setError("");
    try {
      const r = await api.importRecoveryBundle(file, passphrase, { hostId, applySettings });
      setSummary({ result: r.summary, warnings: r.warnings });
      setInspection(null);
      setInspectedKey(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Import failed");
    } finally { setBusy(false); }
  };

  return (
    <section className="card p-5">
      <h2 className="text-sm font-semibold mb-1">Import</h2>
      <p className="text-sm text-muted mb-4">
        Check compatibility against a target host before importing anything.
      </p>
      <input
        type="file"
        accept=".dcbundle"
        className="mb-3 block text-sm"
        onChange={(e) => { setFile(e.target.files?.[0] ?? null); reset(); }}
      />
      <label className="block text-sm mb-1">Passphrase (if the bundle is encrypted)</label>
      <input type="password" className="input w-full mb-3" value={passphrase} onChange={(e) => { setPassphrase(e.target.value); reset(); }} />
      <label className="block text-sm mb-1">Target host</label>
      <select
        className="input w-full mb-3"
        value={hostId ?? ""}
        onChange={(e) => { setHostId(e.target.value ? Number(e.target.value) : undefined); reset(); }}
      >
        <option value="">Local</option>
        {hosts.map((h) => (<option key={h.id} value={h.id}>{h.name}</option>))}
      </select>
      <label className="flex items-center gap-2 text-sm mb-4">
        <input type="checkbox" checked={applySettings} onChange={(e) => setApplySettings(e.target.checked)} />
        Also apply the bundle&apos;s instance settings (disabled sections, localhost 2FA exemption, SMTP/LDAP)
      </label>

      {error && <div className="text-sm text-danger mb-3">{error}</div>}

      <div className="flex gap-2 mb-4">
        <button className="btn" disabled={!file || busy} onClick={doInspect}>
          {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
          Check compatibility
        </button>
        <button className="btn btn-primary" disabled={!file || busy || !matchesInspection} onClick={doImport}>
          <Upload className="h-4 w-4" />
          Import
        </button>
      </div>

      {inspection && <CompatibilityView manifest={inspection.manifest} report={inspection.compatibility} />}
      {summary && <ImportSummaryView summary={summary.result} warnings={summary.warnings} />}
    </section>
  );
}

function CompatibilityView({ manifest, report }: { manifest: RecoveryManifestSummary; report: CompatibilityReport }) {
  const issues = [
    ...report.missingImages.map((s) => `image not found locally: ${s}`),
    ...report.missingVolumes.map((s) => `volume not found on target: ${s}`),
    ...report.unknownHosts.map((s) => `project references host "${s}", which isn't in this bundle or already configured`),
    ...report.warnings,
  ];
  return (
    <div className="rounded-lg border border-border p-3 text-sm">
      <div className="text-muted mb-2">
        Exported {new Date(manifest.exportedAt).toLocaleString()}
        {manifest.exportedBy && ` by ${manifest.exportedBy}`} — {manifest.projects} project(s), {manifest.hosts} host(s), {manifest.registries} registr{manifest.registries === 1 ? "y" : "ies"}, {manifest.alertRules} alert rule(s).
        {report.secretsExcluded ? " Store-managed secrets (host keys, registry/SMTP/LDAP passwords, webhook URLs) excluded." : " Store-managed secrets included."}
        {manifest.projects > 0 && " Project files may still carry their own secrets (e.g. .env), independent of the above."}
      </div>
      {issues.length === 0 ? (
        <div className="flex items-center gap-2 text-ok"><CheckCircle2 className="h-4 w-4" /> No compatibility issues found.</div>
      ) : (
        <ul className="space-y-1">
          {issues.map((s, i) => (
            <li key={i} className="flex items-start gap-2 text-warn">
              <AlertTriangle className="h-4 w-4 mt-0.5 shrink-0" /> {s}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function ImportSummaryView({ summary, warnings }: { summary: RecoveryImportSummary; warnings: string[] }) {
  return (
    <div className="rounded-lg border border-border p-3 text-sm">
      <div className="text-ok mb-2">
        Created {summary.projectsCreated} project(s), {summary.hostsCreated} host(s), {summary.registriesCreated} registr{summary.registriesCreated === 1 ? "y" : "ies"}, {summary.webhooksCreated} webhook(s), {summary.alertRulesCreated} alert rule(s).
      </div>
      {warnings.length > 0 && (
        <ul className="space-y-1">
          {warnings.map((w, i) => (
            <li key={i} className="flex items-start gap-2 text-warn">
              <AlertTriangle className="h-4 w-4 mt-0.5 shrink-0" /> {w}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
