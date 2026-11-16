import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, ChevronDown, ChevronRight, Loader2, MinusCircle, RefreshCw, Stethoscope, XCircle } from "lucide-react";
import { api } from "../lib/api";
import type { CheckResult, CheckStatus, DiagnosticsReport, InstanceDiagnostics } from "../lib/types";
import { useAuth } from "../auth/AuthContext";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner, StatCard } from "../components/ui";
import { getPref, setPref } from "../lib/prefs";

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

// What the user last chose for each check, remembered across reruns and
// reloads. Stored WITH the status it was chosen under and honoured only while
// that status is unchanged: collapsing an OK check stays collapsed, but a check
// that has since flipped to warn/fail reverts to its default (open) so a new
// problem is never hidden by an old choice.
type OpenChoice = { open: boolean; status: CheckStatus };
const OPEN_PREF = "troubleshooting.open";

export function initialOpen(check: CheckResult, choices: Record<string, OpenChoice>): boolean {
  const c = choices[check.id];
  return c && c.status === check.status ? c.open : check.status === "warn" || check.status === "fail";
}

// A check whose status needs attention (warn/fail) opens with its details
// already visible; an OK/skipped one collapses them by default — with dozens
// of checks on a healthy host, always-expanded details are most of why this
// page used to be a long scroll even when nothing was wrong.
function CheckRow({ check }: { check: CheckResult }) {
  const Icon = statusIcon[check.status];
  const hasDetails = !!check.details && check.details.length > 0;
  const [open, setOpen] = useState(() => initialOpen(check, getPref<Record<string, OpenChoice>>(OPEN_PREF, {})));
  const toggle = () => {
    const next = !open;
    setOpen(next);
    setPref(OPEN_PREF, { ...getPref<Record<string, OpenChoice>>(OPEN_PREF, {}), [check.id]: { open: next, status: check.status } });
  };
  const Chevron = open ? ChevronDown : ChevronRight;
  const Header = hasDetails ? "button" : "div";
  return (
    <div className="card p-3">
      {/* A real button when there is something to expand, so it is keyboard
          reachable and announces its state; a plain row otherwise. */}
      <Header
        className="flex items-start gap-3 w-full text-left"
        {...(hasDetails ? { type: "button" as const, onClick: toggle, "aria-expanded": open } : {})}
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

// One titled group of checks. The page shows Docker Commander's own checks
// (admins only) and the selected host's checks as two groups, each with its own
// error, so a host that can't be reached still leaves the instance checks
// readable, which is when they matter most.
function CheckGroup({ title, checks, error }: { title: string; checks?: CheckResult[]; error?: string | null }) {
  return (
    <section className="space-y-2" aria-label={title}>
      <h2 className="text-xs font-semibold uppercase tracking-wide text-muted">{title}</h2>
      {checks ? (
        checks.map((c) => <CheckRow key={`${c.id}:${c.status}`} check={c} />)
      ) : error ? (
        <EmptyState icon={<Stethoscope className="h-4 w-4" />} title="Could not run these checks" hint={error} />
      ) : null}
    </section>
  );
}

export function Troubleshooting() {
  // The instance checks name the binary, the service user and local paths:
  // admin only, like the update banner. Others never request them.
  const isAdmin = useAuth()?.user?.role === "admin";
  const [report, setReport] = useState<DiagnosticsReport | null>(null);
  const [instance, setInstance] = useState<InstanceDiagnostics | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [instanceError, setInstanceError] = useState<string | null>(null);

  const run = useCallback(() => {
    setBusy(true);
    setError(null);
    setInstanceError(null);
    const message = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);
    const host = api
      .runDiagnostics()
      .then(setReport)
      .catch((e) => setError(message(e, "could not run diagnostics")));
    const self = isAdmin
      ? api
          .instanceDiagnostics()
          .then(setInstance)
          .catch((e) => setInstanceError(message(e, "could not run diagnostics")))
      : Promise.resolve();
    Promise.allSettled([host, self]).finally(() => setBusy(false));
  }, [isAdmin]);

  useEffect(() => {
    run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loaded = report || instance;
  const allChecks = [...(instance?.checks ?? []), ...(report?.checks ?? [])];

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
        {!loaded && busy ? (
          <div className="flex items-center gap-2 text-muted">
            <Spinner /> Running diagnostics…
          </div>
        ) : error && !loaded ? (
          <EmptyState
            icon={<Stethoscope className="h-4 w-4" />}
            title="Could not run diagnostics"
            hint={error}
          />
        ) : loaded ? (
          <>
            <KPIStrip checks={allChecks} />
            {isAdmin ? (
              <div className="space-y-5">
                <CheckGroup title="Docker Commander" checks={instance?.checks} error={instanceError} />
                <CheckGroup title="Docker host" checks={report?.checks} error={error} />
              </div>
            ) : (
              <div className="space-y-2">
                {report?.checks.map((c) => (
                  <CheckRow key={`${c.id}:${c.status}`} check={c} />
                ))}
              </div>
            )}
          </>
        ) : null}
      </div>
    </>
  );
}
