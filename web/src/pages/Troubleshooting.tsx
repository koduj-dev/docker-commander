import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, Loader2, MinusCircle, RefreshCw, Stethoscope, XCircle } from "lucide-react";
import { api } from "../lib/api";
import type { CheckResult, CheckStatus, DiagnosticsReport } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";

const statusBadge: Record<CheckStatus, string> = {
  ok: "bg-ok/15 text-ok",
  warn: "bg-warn/15 text-warn",
  fail: "bg-danger/15 text-danger",
  skipped: "bg-panel2 text-muted",
};

const statusIcon: Record<CheckStatus, typeof CheckCircle2> = {
  ok: CheckCircle2,
  warn: AlertTriangle,
  fail: XCircle,
  skipped: MinusCircle,
};

const statusLabel: Record<CheckStatus, string> = {
  ok: "OK",
  warn: "Warning",
  fail: "Failed",
  skipped: "Skipped",
};

function summarize(checks: CheckResult[]): string {
  const counts: Record<CheckStatus, number> = { ok: 0, warn: 0, fail: 0, skipped: 0 };
  for (const c of checks) counts[c.status]++;
  return `${counts.ok} OK · ${counts.warn} warning${counts.warn === 1 ? "" : "s"} · ${counts.fail} failed · ${counts.skipped} skipped`;
}

function CheckRow({ check }: { check: CheckResult }) {
  const Icon = statusIcon[check.status];
  return (
    <div className="card p-3">
      <div className="flex items-start gap-3">
        <Icon className={`h-4 w-4 mt-0.5 shrink-0 ${statusBadge[check.status].split(" ")[1]}`} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <div className="font-medium">{check.name}</div>
            <span className={`text-[10px] uppercase tracking-wide rounded-sm px-1.5 py-0.5 ${statusBadge[check.status]}`}>
              {statusLabel[check.status]}
            </span>
          </div>
          <div className="text-sm text-muted mt-0.5">{check.message}</div>
          {check.details && check.details.length > 0 && (
            <ul className="mt-2 space-y-0.5 text-xs font-mono text-muted list-disc list-inside">
              {check.details.map((d, i) => (
                <li key={i}>{d}</li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

export function Troubleshooting() {
  const [report, setReport] = useState<DiagnosticsReport | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = useCallback(() => {
    setBusy(true);
    setError(null);
    api
      .runDiagnostics()
      .then(setReport)
      .catch((e) => setError(e instanceof Error ? e.message : "could not run diagnostics"))
      .finally(() => setBusy(false));
  }, []);

  useEffect(() => {
    run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <>
      <PageHeader
        title="Troubleshooting"
        actions={
          <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy} onClick={run}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />} Run diagnostics
          </button>
        }
      />
      <div className="p-6 space-y-3">
        {!report && busy ? (
          <div className="flex items-center gap-2 text-muted">
            <Spinner /> Running diagnostics…
          </div>
        ) : error && !report ? (
          <EmptyState
            icon={<Stethoscope className="h-4 w-4" />}
            title="Could not run diagnostics"
            hint={error}
          />
        ) : report ? (
          <>
            <div className="text-sm text-muted">{summarize(report.checks)}</div>
            <div className="space-y-2">
              {report.checks.map((c) => (
                <CheckRow key={c.id} check={c} />
              ))}
            </div>
          </>
        ) : null}
      </div>
    </>
  );
}
