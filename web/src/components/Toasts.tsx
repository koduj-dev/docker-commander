import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, CheckCircle2, X } from "lucide-react";
import clsx from "clsx";

// Transient corner notifications.
//
// This exists for one job: when an alert fires while somebody is looking at the
// app, they should learn about it without having to be on the Alerts page. The
// feed and the nav badge both require you to go and look; a toast comes to you.
//
// It is deliberately NOT a general notification centre — the alert feed is the
// record, and a toast is a nudge that disappears. Anything that must be kept is
// already stored server-side.

export type ToastTone = "info" | "warning" | "critical" | "ok";

export interface Toast {
  id: number;
  tone: ToastTone;
  title: string;
  body?: string;
  /** Optional in-app link, e.g. straight to the alert feed. */
  to?: string;
}

interface ToastAPI {
  push: (t: Omit<Toast, "id">) => void;
}

const Ctx = createContext<ToastAPI | null>(null);

export function useToasts(): ToastAPI {
  const ctx = useContext(Ctx);
  // A no-op fallback rather than a throw: a missing provider should never be
  // able to take down a page over a notification.
  return ctx ?? { push: () => {} };
}

const TTL_MS = 8000;
const MAX_VISIBLE = 4;

const toneStyle: Record<ToastTone, string> = {
  critical: "border-danger/50 bg-danger/10",
  warning: "border-warn/50 bg-warn/10",
  info: "border-border bg-panel2",
  ok: "border-ok/50 bg-ok/10",
};

const toneIcon: Record<ToastTone, React.ReactNode> = {
  critical: <AlertTriangle className="h-4 w-4 text-danger shrink-0" />,
  warning: <AlertTriangle className="h-4 w-4 text-warn shrink-0" />,
  info: <AlertTriangle className="h-4 w-4 text-muted shrink-0" />,
  ok: <CheckCircle2 className="h-4 w-4 text-ok shrink-0" />,
};

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((cur) => cur.filter((t) => t.id !== id));
  }, []);

  const push = useCallback((t: Omit<Toast, "id">) => {
    const id = nextId.current++;
    // Cap the stack: an alert storm must not paper over the whole screen, and
    // the feed is where the full list lives anyway.
    setToasts((cur) => [...cur.slice(-(MAX_VISIBLE - 1)), { ...t, id }]);
  }, []);

  const api = useMemo(() => ({ push }), [push]);

  return (
    <Ctx.Provider value={api}>
      {children}
      <div className="fixed bottom-4 right-4 z-[70] flex flex-col gap-2 w-[22rem] max-w-[calc(100vw-2rem)]">
        {toasts.map((t) => (
          <ToastCard key={t.id} toast={t} onDismiss={() => dismiss(t.id)} />
        ))}
      </div>
    </Ctx.Provider>
  );
}

function ToastCard({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onDismiss, TTL_MS);
    return () => clearTimeout(timer);
  }, [onDismiss]);

  const inner = (
    <div className={clsx("card border p-3 flex items-start gap-2.5 shadow-lg", toneStyle[toast.tone])}>
      {toneIcon[toast.tone]}
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium truncate">{toast.title}</div>
        {toast.body && <div className="text-xs text-muted break-words">{toast.body}</div>}
      </div>
      <button
        type="button"
        className="btn-ghost px-1.5 py-1 shrink-0"
        title="Dismiss"
        onClick={(e) => {
          // Stop the click reaching the wrapping link — dismissing is not
          // "take me to the alerts page".
          e.preventDefault();
          e.stopPropagation();
          onDismiss();
        }}
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  );

  return toast.to ? (
    <Link to={toast.to} onClick={onDismiss} className="block">
      {inner}
    </Link>
  ) : (
    inner
  );
}
