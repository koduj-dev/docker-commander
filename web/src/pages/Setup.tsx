import { useState } from "react";
import { api, ApiError } from "../lib/api";
import { useAuth } from "../auth/AuthContext";
import { AuthShell } from "./AuthShell";

// First-run wizard: create the admin account and choose whether to enable 2FA
// right away (the next step walks through enrollment) or defer it (left
// optional on localhost; can be required later from Settings).
export function Setup() {
  const { setUser, refresh } = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [enable2fa, setEnable2fa] = useState(true);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    if (password.length < 10) return setErr("Password must be at least 10 characters.");
    if (password !== confirm) return setErr("Passwords do not match.");
    setBusy(true);
    try {
      const res = await api.setup(username, password, enable2fa);
      if (res.user) setUser(res.user);
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "Setup failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthShell title="Create your admin account" subtitle="This is the first run. Set up the administrator.">
      <form onSubmit={submit} className="space-y-4">
        <div>
          <label className="label">Username</label>
          <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoFocus required minLength={3} />
        </div>
        <div>
          <label className="label">Password</label>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </div>
        <div>
          <label className="label">Confirm password</label>
          <input className="input" type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
        </div>

        <div>
          <label className="label">Two-factor authentication</label>
          <div className="space-y-2">
            <TwoFAOption
              checked={enable2fa}
              onSelect={() => setEnable2fa(true)}
              title="Enable now (recommended)"
              desc="The next step sets up an authenticator app for this account."
            />
            <TwoFAOption
              checked={!enable2fa}
              onSelect={() => setEnable2fa(false)}
              title="Skip for now"
              desc="Don't require 2FA on localhost. You can enable it later in Settings."
            />
          </div>
        </div>

        {err && <p className="text-sm text-danger">{err}</p>}
        <button className="btn-primary w-full" disabled={busy}>
          {busy ? "Creating…" : "Create account"}
        </button>
      </form>
    </AuthShell>
  );
}

function TwoFAOption({
  checked,
  onSelect,
  title,
  desc,
}: {
  checked: boolean;
  onSelect: () => void;
  title: string;
  desc: string;
}) {
  return (
    <label
      className={`flex gap-3 rounded-lg border px-3 py-2.5 cursor-pointer transition-colors ${
        checked ? "border-accent bg-accent/10" : "border-border hover:bg-panel2"
      }`}
    >
      <input type="radio" name="enable2fa" className="mt-1 accent-accent" checked={checked} onChange={onSelect} />
      <span>
        <span className="block text-sm font-medium text-text">{title}</span>
        <span className="block text-xs text-muted mt-0.5">{desc}</span>
      </span>
    </label>
  );
}
