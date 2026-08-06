import { useState } from "react";
import { api, ApiError } from "../lib/api";
import { useAuth } from "../auth/AuthContext";
import { AuthShell } from "./AuthShell";
import { describePasskeyError, passkeysSupported, usePasskey } from "../lib/webauthn";

// Two-step login: password, then (if enabled) a TOTP code.
export function Login() {
  const { refresh } = useAuth();
  const [step, setStep] = useState<"password" | "2fa">("password");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [mfaToken, setMfaToken] = useState("");
  // Which second factors this account actually has. Offering a passkey button to
  // someone who has none is a dead end, and offering the code box to someone who
  // only has a passkey is worse — they would sit there hunting for an app.
  const [methods, setMethods] = useState<string[]>([]);
  // Having a passkey and being able to use it are different facts: the browser
  // needs a secure context, and the account may be reached over plain HTTP.
  const [passkeyReady, setPasskeyReady] = useState(false);
  const [passkeyReason, setPasskeyReason] = useState("");
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
        setMethods(res.methods ?? ["totp"]);
        setPasskeyReady(res.passkeyReady ?? false);
        setPasskeyReason(res.passkeyReason ?? "");
        setStep("2fa");
      } else if (res.user) {
        await refresh(); // loads prefs, then sets the user
      }
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "Login failed");
    } finally {
      setBusy(false);
    }
  };

  // Signing in with the passkey alone. Offered alongside the password, never
  // instead of it: a lost key must not be a lost account, and this app gives
  // admins no way to reset someone else's second factor.
  const submitPasswordless = async () => {
    setErr("");
    setBusy(true);
    try {
      const { ceremonyId, publicKey } = await api.passwordlessBegin();
      const credential = await usePasskey({ publicKey });
      const res = await api.passwordlessFinish(ceremonyId, credential);
      if (res.user) {
        await refresh();
      }
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : describePasskeyError(e));
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
        await refresh(); // loads prefs, then sets the user
      }
    } catch (e) {
      // A challenge token is good for one attempt, so a wrong code cannot be
      // retried here — the server has already spent it. Sending the user back to
      // the password step is the honest thing: staying on a form that can no
      // longer succeed would just produce a second, more confusing error.
      setStep("password");
      setMfaToken("");
      setMethods([]);
      setCode("");
      setPassword("");
      // Distinguish "the server said no" from "the request never got there":
      // telling someone their code was wrong when the network dropped sends them
      // hunting through their authenticator for a problem that isn't there.
      setErr(
        e instanceof ApiError
          ? (e.status === 429 ? e.message : "That code was not accepted. Sign in again to get a new one.")
          : "Could not reach the server. Check your connection and sign in again.",
      );
    } finally {
      setBusy(false);
    }
  };

  const submitPasskey = async () => {
    setErr("");
    setBusy(true);
    try {
      const options = await api.passkeyLoginBegin(mfaToken);
      const credential = await usePasskey(options);
      const res = await api.passkeyLoginFinish(mfaToken, credential);
      if (res.user) {
        await refresh();
        return;
      }
      setErr("That passkey was not accepted.");
    } catch (e) {
      // A dismissed prompt leaves the challenge token spent, exactly as a wrong
      // code does, so there is nothing to retry here — back to the password step.
      setStep("password");
      setMfaToken("");
      setMethods([]);
      setCode("");
      setPassword("");
      setErr(`${describePasskeyError(e)} Sign in again to try once more.`);
    } finally {
      setBusy(false);
    }
  };

  if (step === "2fa") {
    const hasPasskey = methods.includes("passkey") && passkeyReady && passkeysSupported();
    const hasCode = methods.includes("totp");
    // The account has a passkey but this connection (or browser) cannot use it,
    // and there is no code to fall back on. Saying so beats an empty box.
    const stuck = !hasCode && !hasPasskey;
    return (
      <AuthShell
        title="Two-factor authentication"
        subtitle={hasCode
          ? "Enter the 6-digit code from your authenticator app."
          : "Use your passkey to finish signing in."}
      >
        <div className="space-y-4">
          {hasCode && (
            <form onSubmit={submitCode} className="space-y-4">
              <input
                className="input text-center tracking-[0.5em] text-lg font-mono"
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
                inputMode="numeric"
                autoFocus
                placeholder="000000"
              />
              <button className="btn-primary w-full" disabled={busy || code.length !== 6}>
                {busy ? "Verifying…" : "Verify"}
              </button>
            </form>
          )}

          {hasPasskey && (
            <>
              {hasCode && (
                <div className="flex items-center gap-3 text-xs text-muted">
                  <span className="h-px flex-1 bg-border" /> or <span className="h-px flex-1 bg-border" />
                </div>
              )}
              <button type="button" className="btn-ghost w-full justify-center" onClick={submitPasskey} disabled={busy}>
                Use a passkey
              </button>
            </>
          )}

          {stuck && (
            <div className="space-y-2 text-sm">
              <p className="text-danger">
                This account is protected by a passkey, and this connection cannot use one.
              </p>
              <p className="text-xs text-muted">
                {passkeyReason || "Passkeys need HTTPS (or localhost)."} Reach this server over
                HTTPS — or over <code className="font-mono">localhost</code> — and sign in again.
              </p>
            </div>
          )}

          {err && <p className="text-sm text-danger">{err}</p>}
        </div>
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

      {passkeysSupported() && (
        <div className="mt-4 space-y-4">
          <div className="flex items-center gap-3 text-xs text-muted">
            <span className="h-px flex-1 bg-border" /> or <span className="h-px flex-1 bg-border" />
          </div>
          <button
            type="button"
            className="btn-ghost w-full justify-center"
            onClick={submitPasswordless}
            disabled={busy}
          >
            Sign in with a passkey
          </button>
        </div>
      )}
    </AuthShell>
  );
}
