import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, ChevronDown, ChevronRight, Loader2, MinusCircle, RefreshCw, Stethoscope, XCircle } from "lucide-react";
import { api } from "../lib/api";
import type { CheckResult, CheckStatus, DiagnosticsReport } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner, StatCard } from "../components/ui";

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

function countByStatus(checks: CheckResult[]): Record<CheckStatus, number> {
  const counts: Record<CheckStatus, number> = { ok: 0, warn: 0, fail: 0, skipped: 0 };
  for (const c of checks) counts[c.status]++;
  return counts;
}

// KPIStrip is the "3 of 7 OK" at-a-glance summary the page opens with, so the
// overall health is visible without reading a single check — the counts
// themselves link nowhere, this is a summary, not a filter.
function KPIStrip({ checks }: { checks: CheckResult[] }) {
  const counts = countByStatus(checks);
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
      <StatCard icon={<CheckCircle2 className="h-5 w-5 text-ok" />} label="OK" value={`${counts.ok} of ${checks.length}`} />
      <StatCard icon={<AlertTriangle className="h-5 w-5 text-warn" />} label="Warnings" value={counts.warn} />
      <StatCard icon={<XCircle className="h-5 w-5 text-danger" />} label="Failed" value={counts.fail} />
      <StatCard icon={<MinusCircle className="h-5 w-5 text-muted" />} label="Skipped" value={counts.skipped} />
    </div>
  );
}

// A check whose status needs attention (warn/fail) opens with its details
// already visible; an OK/skipped one collapses them by default — with dozens
// of checks on a healthy host, always-expanded details are most of why this
// page used to be a long scroll even when nothing was wrong.
// CheckRow is keyed by id AND status by the caller: a rerun that flips a check
// from OK to warn/fail (or back) remounts it, so the open/closed default
// follows the new status instead of keeping the previous run's.
function CheckRow({ check }: { check: CheckResult }) {
  const Icon = statusIcon[check.status];
  const hasDetails = !!check.details && check.details.length > 0;
  const [open, setOpen] = useState(check.status === "warn" || check.status === "fail");
  const Chevron = open ? ChevronDown : ChevronRight;
  const Header = hasDetails ? "button" : "div";
  return (
    <div className="card p-3">
      {/* A real button when there is something to expand, so it is keyboard
          reachable and announces its state; a plain row otherwise. */}
      <Header
        className="flex items-start gap-3 w-full text-left"
        {...(hasDetails ? { type: "button" as const, onClick: () => setOpen((o) => !o), "aria-expanded": open } : {})}
      >
        <Icon className={`h-4 w-4 mt-0.5 shrink-0 ${statusBadge[check.status].split(" ")[1]}`} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <div className="font-medium">{check.name}</div>
            <span className={`text-[10px] uppercase tracking-wide rounded-sm px-1.5 py-0.5 ${statusBadge[check.status]}`}>
              {statusLabel[check.status]}
            </span>
          </div>
          <div className="text-sm text-muted mt-0.5">{check.message}</div>
        </div>
        {hasDetails && <Chevron className="h-4 w-4 text-muted shrink-0 mt-0.5" />}
      </Header>
      {hasDetails && open && (
        <ul className="mt-2 ml-7 space-y-0.5 text-xs font-mono text-muted list-disc list-inside">
          {check.details!.map((d, i) => (
            <li key={i}>{d}</li>
          ))}
        </ul>
      )}
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
            <KPIStrip checks={report.checks} />
            <div className="space-y-2">
              {report.checks.map((c) => (
                <CheckRow key={`${c.id}:${c.status}`} check={c} />
              ))}
            </div>
          </>
        ) : null}
      </div>
    </>
  );
}
