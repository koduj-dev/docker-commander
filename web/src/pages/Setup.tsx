import { useState } from "react";
import { api, ApiError } from "../lib/api";
import { useAuth } from "../auth/AuthContext";
import { AuthShell } from "./AuthShell";

// First-run wizard: create the admin account. 2FA enrollment follows
// immediately because the app mandates it.
export function Setup() {
  const { setUser, refresh } = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    if (password.length < 10) return setErr("Password must be at least 10 characters.");
    if (password !== confirm) return setErr("Passwords do not match.");
    setBusy(true);
    try {
      const res = await api.setup(username, password);
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
        {err && <p className="text-sm text-danger">{err}</p>}
        <button className="btn-primary w-full" disabled={busy}>
          {busy ? "Creating…" : "Create account"}
        </button>
      </form>
    </AuthShell>
  );
}
