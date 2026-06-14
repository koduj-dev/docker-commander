import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, Server, CheckCircle2, XCircle, Loader2, ShieldAlert, ShieldCheck, KeyRound, Info, Power, PowerOff } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { Host } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { HostDetailModal } from "../components/HostDetailModal";
import { useDialogs } from "../components/Dialog";

type TestResult = {
  ok: boolean;
  text: string;
  untrusted?: boolean;
  mismatch?: boolean;
  fingerprint?: string;
  keyType?: string;
};
type TestState = TestResult | "loading" | undefined;

export function Hosts() {
  const [hosts, setHosts] = useState<Host[] | null>(null);
  const [tests, setTests] = useState<Record<number, TestState>>({});
  const [trusting, setTrusting] = useState<Record<number, boolean>>({});
  const [showForm, setShowForm] = useState(false);
  const [detail, setDetail] = useState<Host | null>(null);
  const dialogs = useDialogs();

  const load = useCallback(() => {
    api.hosts().then(setHosts).catch(() => setHosts([]));
  }, []);
  useEffect(() => {
    load();
    // Poll so the reachability badge tracks the monitor (it probes every ~30s).
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [load]);

  const test = async (id: number) => {
    setTests((t) => ({ ...t, [id]: "loading" }));
    try {
      const r = await api.testHost(id);
      setTests((t) => ({
        ...t,
        [id]: {
          ok: r.ok,
          text: r.ok ? `OK · Docker ${r.serverVersion} · ${r.containersRunning} running` : (r.error ?? "unreachable"),
          untrusted: r.untrusted,
          mismatch: r.mismatch,
          fingerprint: r.fingerprint,
          keyType: r.keyType,
        },
      }));
    } catch {
      setTests((t) => ({ ...t, [id]: { ok: false, text: "request failed" } }));
    }
  };

  const trust = async (id: number, fingerprint?: string) => {
    setTrusting((t) => ({ ...t, [id]: true }));
    try {
      const r = await api.trustHost(id, fingerprint);
      if (r.ok) {
        await test(id); // re-test now that the key is pinned
      } else {
        setTests((t) => ({ ...t, [id]: { ok: false, text: r.error ?? "could not trust host", mismatch: r.mismatch, fingerprint: r.fingerprint } }));
      }
    } catch {
      setTests((t) => ({ ...t, [id]: { ok: false, text: "trust request failed" } }));
    } finally {
      setTrusting((t) => ({ ...t, [id]: false }));
    }
  };

  const del = async (h: Host) => {
    if (!(await dialogs.confirm({ title: "Delete host", message: <>Remove the host <code className="font-mono text-text">{h.name}</code>? (Its containers aren't touched — only this connection.)</>, danger: true, confirmLabel: "Delete" }))) return;
    await api.deleteHost(h.id);
    load();
  };

  const toggleDisabled = async (h: Host) => {
    try {
      await api.setHostDisabled(h.id, !h.disabled);
    } catch (e) {
      dialogs.alert({ title: "Could not change host state", message: e instanceof Error ? e.message : "request failed" });
    } finally {
      load();
    }
  };

  if (!hosts) return (<><PageHeader title="Hosts" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="Hosts" actions={<button className="btn-primary" onClick={() => setShowForm((v) => !v)}><Plus className="h-4 w-4" /> Add host</button>} />
      <div className="p-6 space-y-4">
        {showForm && <HostForm onDone={() => { setShowForm(false); load(); }} />}
        {hosts.length === 0 ? (
          <EmptyState title="No hosts" />
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {hosts.map((h) => {
              const t = tests[h.id];
              return (
                <div key={h.id} className="card p-4">
                  <div className="flex items-start justify-between">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 font-medium">
                        <Server className={`h-4 w-4 ${h.disabled ? "text-muted" : "text-accent"}`} /> {h.name}
                        <span className="text-xs bg-panel2 rounded-sm px-1.5 py-0.5 text-muted">{h.kind}</span>
                        {h.disabled && <span className="text-xs bg-warn/15 text-warn rounded-sm px-1.5 py-0.5">disabled</span>}
                        {!h.disabled && h.reachable === false && (
                          <span className="text-xs bg-danger/15 text-danger rounded-sm px-1.5 py-0.5 flex items-center gap-1" title={h.unreachableSince ? `unreachable since ${new Date(h.unreachableSince).toLocaleString()}` : "the monitor can't reach this host's Docker daemon"}>
                            <span className="h-1.5 w-1.5 rounded-full bg-danger inline-block" /> unreachable
                          </span>
                        )}
                      </div>
                      <div className="text-xs text-muted font-mono mt-1 break-all">{h.address || "(local socket)"}</div>
                    </div>
                    <div className="flex items-center gap-1">
                      <button className="btn-ghost px-2 py-1 text-xs" title="Host detail" onClick={() => setDetail(h)}><Info className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1 text-xs" onClick={() => test(h.id)}>Test</button>
                      {h.kind !== "local" && (
                        <button className="btn-ghost px-2 py-1" title={h.disabled ? "Enable monitoring" : "Disable — the monitor ignores this host (no events/stats)"} onClick={() => toggleDisabled(h)}>
                          {h.disabled ? <Power className="h-4 w-4 text-ok" /> : <PowerOff className="h-4 w-4" />}
                        </button>
                      )}
                      {h.kind !== "local" && (
                        <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(h)}><Trash2 className="h-4 w-4" /></button>
                      )}
                    </div>
                  </div>
                  <HostAlertEmail host={h} onSaved={load} />
                  {t === "loading" ? (
                    <div className="mt-3 text-xs flex items-center gap-1.5 text-muted">
                      <Loader2 className="h-3.5 w-3.5 animate-spin" /> Testing…
                    </div>
                  ) : t ? (
                    <div className="mt-3 space-y-2">
                      {/* Untrusted SSH host key: show fingerprint + an explicit trust action (TOFU). */}
                      {t.untrusted ? (
                        <div className="rounded-md border border-warn/40 bg-warn/10 p-2.5 text-xs space-y-2">
                          <div className="flex items-center gap-1.5 text-warn font-medium">
                            <ShieldAlert className="h-3.5 w-3.5" /> Host key not trusted yet
                          </div>
                          <div className="text-muted">Verify this fingerprint out-of-band before trusting:</div>
                          <code className="block break-all bg-panel2 rounded-sm px-2 py-1 text-text">
                            {t.keyType} {t.fingerprint}
                          </code>
                          <button
                            className="btn-primary px-2.5 py-1 text-xs"
                            disabled={trusting[h.id]}
                            onClick={() => trust(h.id, t.fingerprint)}
                          >
                            {trusting[h.id] ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <ShieldCheck className="h-3.5 w-3.5" />}
                            {trusting[h.id] ? "Trusting…" : "Trust this host"}
                          </button>
                        </div>
                      ) : t.mismatch ? (
                        <div className="rounded-md border border-danger/50 bg-danger/10 p-2.5 text-xs space-y-2">
                          <div className="flex items-center gap-1.5 text-danger font-medium">
                            <ShieldAlert className="h-3.5 w-3.5" /> Host key CHANGED — possible MITM
                          </div>
                          <div className="text-muted">{t.text}</div>
                          {t.fingerprint && (
                            <code className="block break-all bg-panel2 rounded-sm px-2 py-1 text-text">{t.fingerprint}</code>
                          )}
                          <button
                            className="btn-ghost px-2.5 py-1 text-xs text-danger border border-danger/40"
                            disabled={trusting[h.id]}
                            onClick={() => trust(h.id, t.fingerprint)}
                            title="Only do this if you changed the host deliberately"
                          >
                            <KeyRound className="h-3.5 w-3.5" /> Re-trust new key
                          </button>
                        </div>
                      ) : (
                        <div className={clsx("text-xs flex items-center gap-1.5", t.ok ? "text-ok" : "text-danger")}>
                          {t.ok ? <CheckCircle2 className="h-3.5 w-3.5" /> : <XCircle className="h-3.5 w-3.5" />}
                          {t.text}
                        </div>
                      )}
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>
        )}
        <p className="text-xs text-muted">
          <strong>SSH hosts</strong> use the address <code>user@host[:port]</code> and authenticate with the
          server's SSH agent or <code>~/.ssh</code> keys (no keys are stored here). The daemon's <strong>host key</strong>
          is verified against <code>~/.ssh/known_hosts</code> or a key you explicitly trust on first connect; a changed
          key is refused as a possible MITM. <strong>TCP</strong> hosts use <code>tcp://host:2376</code> with optional TLS material.
        </p>
      </div>
      {detail && <HostDetailModal host={detail} onClose={() => setDetail(null)} />}
    </>
  );
}

// HostAlertEmail is an inline editor for a host's per-host alert recipient.
function HostAlertEmail({ host, onSaved }: { host: Host; onSaved: () => void }) {
  const [value, setValue] = useState(host.alertEmail ?? "");
  const [busy, setBusy] = useState(false);
  const dirty = value !== (host.alertEmail ?? "");
  const save = async () => {
    setBusy(true);
    try { await api.updateHostAlertEmail(host.id, value.trim()); onSaved(); } finally { setBusy(false); }
  };
  return (
    <div className="mt-3 flex items-center gap-2">
      <label className="text-[11px] text-muted shrink-0">Alert email</label>
      <input
        className="input py-1 text-xs"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="(use global SMTP recipient)"
      />
      {dirty && <button className="btn-primary px-2 py-1 text-xs" disabled={busy} onClick={save}>{busy ? "…" : "Save"}</button>}
    </div>
  );
}

function HostForm({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"tcp" | "ssh">("ssh");
  const [address, setAddress] = useState("");
  const [tlsCa, setTlsCa] = useState("");
  const [tlsCert, setTlsCert] = useState("");
  const [tlsKey, setTlsKey] = useState("");
  const [alertEmail, setAlertEmail] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(""); setBusy(true);
    try {
      await api.createHost({ name, kind, address, tlsCa, tlsCert, tlsKey, alertEmail });
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label className="label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div>
          <label className="label">Type</label>
          <select className="input" value={kind} onChange={(e) => setKind(e.target.value as "tcp" | "ssh")}>
            <option value="ssh">SSH</option>
            <option value="tcp">TCP (+TLS)</option>
          </select>
        </div>
        <div>
          <label className="label">Address</label>
          <input className="input" value={address} onChange={(e) => setAddress(e.target.value)} placeholder={kind === "ssh" ? "user@host" : "tcp://host:2376"} required />
        </div>
      </div>
      {kind === "tcp" && (
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          {[["CA cert", tlsCa, setTlsCa], ["Client cert", tlsCert, setTlsCert], ["Client key", tlsKey, setTlsKey]].map(([lbl, val, set]) => (
            <div key={lbl as string}>
              <label className="label">{lbl as string} (PEM, optional)</label>
              <textarea className="input font-mono text-[10px] h-20" value={val as string} onChange={(e) => (set as (s: string) => void)(e.target.value)} placeholder="-----BEGIN…" />
            </div>
          ))}
        </div>
      )}
      <div>
        <label className="label">Alert email (optional — overrides global SMTP recipient for this host)</label>
        <input className="input" value={alertEmail} onChange={(e) => setAlertEmail(e.target.value)} placeholder="ops-prod@example.com" />
      </div>
      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Saving…" : "Add host"}</button>
      </div>
    </form>
  );
}
