import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, KeyRound, CheckCircle2, XCircle, Loader2 } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { Registry } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";

type TestState = { ok: boolean; text: string } | "loading" | undefined;

export function Registries() {
  const dialogs = useDialogs();
  const [regs, setRegs] = useState<Registry[] | null>(null);
  const [tests, setTests] = useState<Record<number, TestState>>({});
  const [showForm, setShowForm] = useState(false);

  const load = useCallback(() => {
    api.registries().then(setRegs).catch(() => setRegs([]));
  }, []);
  useEffect(() => load(), [load]);

  const test = async (id: number) => {
    setTests((t) => ({ ...t, [id]: "loading" }));
    try {
      const r = await api.testRegistry(id);
      setTests((t) => ({ ...t, [id]: { ok: r.ok, text: r.ok ? "Login OK" : r.error ?? "login failed" } }));
    } catch {
      setTests((t) => ({ ...t, [id]: { ok: false, text: "request failed" } }));
    }
  };

  const del = async (rg: Registry) => {
    if (!(await dialogs.confirm({ title: "Delete registry", message: <>Delete the stored credentials for <code className="font-mono text-text">{rg.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    await api.deleteRegistry(rg.id);
    load();
  };

  if (!regs) return (<><PageHeader title="Registries" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="Registries" actions={<button className="btn-primary" onClick={() => setShowForm((v) => !v)}><Plus className="h-4 w-4" /> Add registry</button>} />
      <div className="p-6 space-y-4">
        {showForm && <RegistryForm onDone={() => { setShowForm(false); load(); }} />}
        {regs.length === 0 ? (
          <EmptyState title="No registries" hint="Add credentials to pull private images and push." />
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {regs.map((rg) => {
              const t = tests[rg.id];
              return (
                <div key={rg.id} className="card p-4">
                  <div className="flex items-start justify-between">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 font-medium">
                        <KeyRound className="h-4 w-4 text-accent" /> {rg.name}
                      </div>
                      <div className="text-xs text-muted font-mono mt-1 break-all">{rg.address}</div>
                      <div className="text-xs text-muted mt-0.5">user: {rg.username || "(anonymous)"}</div>
                    </div>
                    <div className="flex items-center gap-1">
                      <button className="btn-ghost px-2 py-1 text-xs" onClick={() => test(rg.id)}>Test</button>
                      <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(rg)}><Trash2 className="h-4 w-4" /></button>
                    </div>
                  </div>
                  {t && (
                    <div className={clsx("mt-3 text-xs flex items-center gap-1.5", t === "loading" ? "text-muted" : t.ok ? "text-ok" : "text-danger")}>
                      {t === "loading" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : t.ok ? <CheckCircle2 className="h-3.5 w-3.5" /> : <XCircle className="h-3.5 w-3.5" />}
                      {t === "loading" ? "Testing…" : t.text}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
        <p className="text-xs text-muted">
          The <strong>address</strong> is the registry host — e.g. <code>docker.io</code> for Docker Hub,
          <code> ghcr.io</code>, or <code>localhost:5000</code>. Credentials let Docker Commander pull private
          images from and push to that registry; the secret is <strong>encrypted at rest</strong> and never returned by the API.
        </p>
      </div>
    </>
  );
}

function RegistryForm({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [username, setUsername] = useState("");
  const [secret, setSecret] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(""); setBusy(true);
    try {
      await api.createRegistry({ name, address, username, secret });
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label className="label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Docker Hub" required />
        </div>
        <div>
          <label className="label">Address (registry host)</label>
          <input className="input font-mono" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="docker.io / ghcr.io / localhost:5000" required />
        </div>
        <div>
          <label className="label">Username</label>
          <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        </div>
        <div>
          <label className="label">Password / token</label>
          <input className="input" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="new-password" />
        </div>
      </div>
      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Saving…" : "Add registry"}</button>
      </div>
    </form>
  );
}
