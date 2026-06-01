import clsx from "clsx";
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

export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="text-center py-16 text-muted">
      <div className="text-sm font-medium">{title}</div>
      {hint && <div className="text-xs mt-1">{hint}</div>}
    </div>
  );
}
