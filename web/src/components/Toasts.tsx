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

// Tone colours the LEFT EDGE and the countdown bar, never the panel itself.
// Tinting the background (bg-danger/10) replaced `card`'s opaque bg-panel with a
// translucent colour, so whatever was behind the toast showed through and the
// text became hard to read over a busy page.
const toneStyle: Record<ToastTone, string> = {
  critical: "border-l-danger",
  warning: "border-l-warn",
  info: "border-l-muted",
  ok: "border-l-ok",
};

const toneBar: Record<ToastTone, string> = {
  critical: "bg-danger",
  warning: "bg-warn",
  info: "bg-muted",
  ok: "bg-ok",
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
  // Hovering pauses the countdown: a toast that vanishes while you are reading
  // it is worse than one that lingers. The CSS bar and the JS timer are paused
  // together, so the bar never disagrees with when it actually goes.
  const [paused, setPaused] = useState(false);
  const remaining = useRef(TTL_MS);
  const startedAt = useRef(Date.now());

  useEffect(() => {
    if (paused) return;
    startedAt.current = Date.now();
    const timer = setTimeout(onDismiss, remaining.current);
    return () => {
      clearTimeout(timer);
      remaining.current = Math.max(0, remaining.current - (Date.now() - startedAt.current));
    };
  }, [paused, onDismiss]);

  const inner = (
    <div
      className={clsx(
        "card border border-l-4 shadow-lg relative overflow-hidden",
        // bg-panel comes from `card` and must stay: the panel is opaque so the
        // page behind never bleeds through the text.
        toneStyle[toast.tone],
      )}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
    >
      <div className="p-3 flex items-start gap-2.5">
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
      <div
        className={clsx("absolute bottom-0 left-0 h-0.5 w-full origin-left", toneBar[toast.tone])}
        style={{
          animation: `dc-toast-timer ${TTL_MS}ms linear forwards`,
          animationPlayState: paused ? "paused" : "running",
        }}
      />
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
