import clsx from "clsx";
import { Inbox } from "lucide-react";
import type { ReactNode } from "react";
import { stateColor } from "../lib/format";

export function Spinner({ className }: { className?: string }) {
  return (
    <div
      className={clsx(
        "animate-spin rounded-full border-2 border-border border-t-accent",
        className ?? "h-5 w-5"
      )}
    />
  );
}

export function StateBadge({ state, label }: { state: string; label?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium">
      <span className={clsx("h-2 w-2 rounded-full", dotColor(state))} />
      <span className={stateColor(state)}>{label ?? state}</span>
    </span>
  );
}

function dotColor(state: string): string {
  switch (state) {
    case "running":
      return "bg-ok shadow-[0_0_8px] shadow-ok/60";
    case "paused":
      return "bg-warn";
    case "exited":
    case "dead":
      return "bg-danger";
    default:
      return "bg-muted";
  }
}

export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={clsx("card p-5", className)}>{children}</div>;
}

export function StatCard({
  label,
  value,
  sub,
  icon,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="card p-4 flex items-center gap-4">
      {icon && <div className="text-accent">{icon}</div>}
      <div className="min-w-0">
        <div className="text-xs uppercase tracking-wide text-muted">{label}</div>
        <div className="text-xl font-semibold truncate">{value}</div>
        {sub && <div className="text-xs text-muted truncate">{sub}</div>}
      </div>
    </div>
  );
}

/**
 * EmptyState is the "nothing here yet" placeholder.
 *
 * It is a bounded, dashed-outline panel rather than bare centred text: on a wide
 * (QHD+) screen the content area is enormous, and two lines of muted text floating
 * in the middle of it read as a rendering failure rather than as a deliberate
 * empty list. The outline gives the eye something that obviously belongs there.
 *
 * `icon` and `action` are optional — pass an action when the page has an obvious
 * next step, so the placeholder does the job the user came for.
 */
export function EmptyState({
  title,
  hint,
  icon,
  action,
}: {
  title: string;
  hint?: ReactNode;
  icon?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="rounded-xl border border-dashed border-border bg-panel2/25 px-6 py-10">
      <div className="mx-auto flex max-w-md flex-col items-center gap-2 text-center">
        <div className="grid h-9 w-9 place-items-center rounded-lg border border-border bg-panel2 text-muted">
          {icon ?? <Inbox className="h-4 w-4" />}
        </div>
        <div className="text-sm font-medium text-text">{title}</div>
        {hint && <div className="text-xs text-muted">{hint}</div>}
        {action && <div className="mt-2">{action}</div>}
      </div>
    </div>
  );
}
