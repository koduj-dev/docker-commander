import { useEffect, useState } from "react";
import { Loader2, Mail, Send } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { SmtpConfig } from "../lib/types";
import { Spinner } from "./ui";

// EmailConfig edits the instance-wide SMTP relay. It lives under Settings (not
// Alerts) because it is one global outbound-mail credential for the whole
// installation — the same category as the LDAP bind password — and is used for
// system notifications beyond alert rules. Admin-only, enforced server-side.
export function EmailConfig() {
  const [cfg, setCfg] = useState<SmtpConfig | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState<"" | "save" | "test">("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  useEffect(() => {
    api.smtp().then(setCfg).catch(() => setCfg({ host: "", port: 587, username: "", from: "", to: "", tls: false }));
  }, []);

  if (!cfg) return <div className="p-2 flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;

  const patch = (p: Partial<SmtpConfig>) => setCfg({ ...cfg, ...p });

  const save = async () => {
    setBusy("save"); setMsg(null);
    try {
      await api.setSmtp({ ...cfg, password });
      setPassword("");
      setMsg({ ok: true, text: "Saved." });
      api.smtp().then(setCfg).catch(() => {});
    } catch {
      setMsg({ ok: false, text: "save failed" });
    } finally { setBusy(""); }
  };

  const test = async () => {
    setBusy("test"); setMsg(null);
    try {
      const r = await api.testSmtp({ ...cfg, password });
      setMsg(r.ok ? { ok: true, text: "Test email sent." } : { ok: false, text: r.error ?? "test failed" });
    } catch {
      setMsg({ ok: false, text: "test failed" });
    } finally { setBusy(""); }
  };

  return (
    <div className="card p-5 space-y-4 max-w-2xl">
      <div className="flex items-center gap-2 font-medium"><Mail className="h-4 w-4 text-accent" /> SMTP server</div>
      <p className="text-xs text-muted">
        Configure a mail server, then enable <strong>“Also send an email”</strong> on any alert rule. The password is
        encrypted at rest and never returned by the API.
      </p>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="md:col-span-2">
          <label className="label">Host</label>
          <input className="input font-mono" value={cfg.host} onChange={(e) => patch({ host: e.target.value })} placeholder="smtp.example.com" />
        </div>
        <div>
          <label className="label">Port</label>
          <input className="input" type="number" value={cfg.port} onChange={(e) => patch({ port: +e.target.value })} placeholder="587" />
        </div>
        <div>
          <label className="label">Username</label>
          <input className="input" value={cfg.username} onChange={(e) => patch({ username: e.target.value })} autoComplete="off" />
        </div>
        <div>
          <label className="label">Password {cfg.hasPassword && <span className="text-ok">(stored)</span>}</label>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={cfg.hasPassword ? "•••••• (unchanged)" : ""} autoComplete="new-password" />
        </div>
        <label className="flex items-center gap-2 text-sm pb-2 self-end">
          <input type="checkbox" checked={cfg.tls} onChange={(e) => patch({ tls: e.target.checked })} /> Implicit TLS (port 465)
        </label>
        <div>
          <label className="label">From</label>
          <input className="input" value={cfg.from} onChange={(e) => patch({ from: e.target.value })} placeholder="alerts@example.com" />
        </div>
        <div className="md:col-span-2">
          <label className="label">To (comma-separated)</label>
          <input className="input" value={cfg.to} onChange={(e) => patch({ to: e.target.value })} placeholder="ops@example.com, oncall@example.com" />
        </div>
      </div>
      {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
      <div className="flex justify-end gap-2">
        <button className="btn-ghost" onClick={test} disabled={busy !== ""}>
          {busy === "test" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />} Send test
        </button>
        <button className="btn-primary" onClick={save} disabled={busy !== ""}>
          {busy === "save" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Mail className="h-4 w-4" />} Save
        </button>
      </div>
    </div>
  );
}
