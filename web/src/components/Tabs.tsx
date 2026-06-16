import clsx from "clsx";
import type { ReactNode } from "react";

export interface TabItem<K extends string = string> {
  key: K;
  label: string;
  icon?: ReactNode;
  count?: number; // optional badge (e.g. how many items in that tab)
}

// Tabs is the app's one tab bar: an underline-style strip shared by Alerts,
// the container detail view and Templates, so sub-view switching looks the same
// everywhere. Items may carry an icon and a small count badge.
export function Tabs<K extends string>({
  tabs,
  active,
  onChange,
  className,
}: {
  tabs: TabItem<K>[];
  active: K;
  onChange: (key: K) => void;
  className?: string;
}) {
  return (
    <div className={clsx("flex gap-1 border-b border-border overflow-x-auto", className)}>
      {tabs.map((t) => {
        const on = t.key === active;
        return (
          <button
            key={t.key}
            onClick={() => onChange(t.key)}
            className={clsx(
              "flex items-center gap-2 px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors whitespace-nowrap",
              on ? "border-accent text-accent" : "border-transparent text-muted hover:text-text",
            )}
          >
            {t.icon}
            {t.label}
            {t.count !== undefined && (
              <span
                className={clsx(
                  "text-xs rounded-full px-1.5 py-0.5 leading-none",
                  on ? "bg-accent/15 text-accent" : "bg-panel2 text-muted",
                )}
              >
                {t.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}
