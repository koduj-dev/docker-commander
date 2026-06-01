import { useState } from "react";
import { api, ApiError } from "../lib/api";
import { useAuth } from "../auth/AuthContext";
import { AuthShell } from "./AuthShell";

// Two-step login: password, then (if enabled) a TOTP code.
export function Login() {
  const { setUser, refresh } = useAuth();
  const [step, setStep] = useState<"password" | "2fa">("password");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [mfaToken, setMfaToken] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submitPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      const res = await api.login(username, password);
      if (res.mfaRequired && res.mfaToken) {
        setMfaToken(res.mfaToken);
        setStep("2fa");
      } else if (res.user) {
        setUser(res.user);
        await refresh();
      }
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "Login failed");
    } finally {
      setBusy(false);
    }
  };

  const submitCode = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      const res = await api.verify2fa(mfaToken, code);
      if (res.user) {
        setUser(res.user);
        await refresh();
      }
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "Invalid code");
    } finally {
      setBusy(false);
    }
  };

  if (step === "2fa") {
    return (
      <AuthShell title="Two-factor authentication" subtitle="Enter the 6-digit code from your authenticator app.">
        <form onSubmit={submitCode} className="space-y-4">
          <input
            className="input text-center tracking-[0.5em] text-lg font-mono"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
            inputMode="numeric"
            autoFocus
            placeholder="000000"
          />
          {err && <p className="text-sm text-danger">{err}</p>}
          <button className="btn-primary w-full" disabled={busy || code.length !== 6}>
            {busy ? "Verifying…" : "Verify"}
          </button>
        </form>
      </AuthShell>
    );
  }

  return (
    <AuthShell title="Sign in" subtitle="Welcome back. Sign in to continue.">
      <form onSubmit={submitPassword} className="space-y-4">
        <div>
          <label className="label">Username</label>
          <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoFocus required />
        </div>
        <div>
          <label className="label">Password</label>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </div>
        {err && <p className="text-sm text-danger">{err}</p>}
        <button className="btn-primary w-full" disabled={busy}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </AuthShell>
  );
}
