import { createContext, useContext, useState, type ReactNode } from "react";
import { AlertTriangle, X } from "lucide-react";

type ConfirmOpts = { title: string; message?: ReactNode; confirmLabel?: string; cancelLabel?: string; danger?: boolean };
type PromptOpts = { title: string; label?: string; defaultValue?: string; placeholder?: string; confirmLabel?: string };
type AlertOpts = { title: string; message?: ReactNode };

type Pending =
  | { kind: "confirm"; opts: ConfirmOpts; resolve: (v: boolean) => void }
  | { kind: "prompt"; opts: PromptOpts; resolve: (v: string | null) => void }
  | { kind: "alert"; opts: AlertOpts; resolve: () => void };

type DialogApi = {
  confirm: (o: ConfirmOpts) => Promise<boolean>;
  prompt: (o: PromptOpts) => Promise<string | null>;
  alert: (o: AlertOpts) => Promise<void>;
};

const Ctx = createContext<DialogApi | null>(null);

// useDialogs returns app-styled replacements for window.confirm/prompt/alert.
export function useDialogs(): DialogApi {
  const c = useContext(Ctx);
  if (!c) throw new Error("useDialogs must be used within <DialogProvider>");
  return c;
}

export function DialogProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null);
  const [value, setValue] = useState("");

  const api: DialogApi = {
    confirm: (opts) => new Promise((resolve) => setPending({ kind: "confirm", opts, resolve })),
    prompt: (opts) => new Promise((resolve) => { setValue(opts.defaultValue ?? ""); setPending({ kind: "prompt", opts, resolve }); }),
    alert: (opts) => new Promise((resolve) => setPending({ kind: "alert", opts, resolve })),
  };

  // settle clears the dialog and resolves the original promise. `accept` is the
  // confirm/submit action; otherwise it's a cancel/dismiss.
  const settle = (accept: boolean) => {
    const p = pending;
    setPending(null);
    if (!p) return;
    if (p.kind === "confirm") p.resolve(accept);
    else if (p.kind === "prompt") p.resolve(accept ? value.trim() || null : null);
    else p.resolve();
  };

  return (
    <Ctx.Provider value={api}>
      {children}
      {pending && (
        <div className="fixed inset-0 z-[80] bg-black/60 grid place-items-center p-6" onClick={() => settle(false)}>
          <form
            className="card w-full max-w-xl flex flex-col"
            onClick={(e) => e.stopPropagation()}
            onSubmit={(e) => { e.preventDefault(); settle(true); }}
          >
            <div className="flex items-center gap-3 p-4 border-b border-border">
              {pending.kind === "confirm" && pending.opts.danger && <AlertTriangle className="h-4 w-4 text-danger shrink-0" />}
              <div className="font-medium">{pending.opts.title}</div>
              <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={() => settle(false)}><X className="h-4 w-4" /></button>
            </div>

            {(pending.kind === "prompt" || !!pending.opts.message) && (
              <div className="p-4 space-y-3">
                {pending.kind !== "prompt" && pending.opts.message && <div className="text-sm text-muted whitespace-pre-line">{pending.opts.message}</div>}
                {pending.kind === "prompt" && (
                  <label className="block">
                    {pending.opts.label && <span className="label">{pending.opts.label}</span>}
                    <input autoFocus className="input" value={value} placeholder={pending.opts.placeholder} onChange={(e) => setValue(e.target.value)} />
                  </label>
                )}
              </div>
            )}

            <div className="flex justify-end gap-2 p-4 border-t border-border">
              {pending.kind !== "alert" && (
                <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={() => settle(false)}>
                  {pending.kind === "confirm" ? pending.opts.cancelLabel ?? "Cancel" : "Cancel"}
                </button>
              )}
              <button type="submit" className={`px-3 py-1.5 text-sm ${pending.kind === "confirm" && pending.opts.danger ? "btn-danger" : "btn-primary"}`}>
                {pending.kind === "alert" ? "OK" : pending.kind === "prompt" ? pending.opts.confirmLabel ?? "Save" : pending.opts.confirmLabel ?? "Confirm"}
              </button>
            </div>
          </form>
        </div>
      )}
    </Ctx.Provider>
  );
}
