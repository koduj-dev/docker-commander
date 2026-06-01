import { useEffect, useState } from "react";
import { api, ApiError, type Enrollment } from "../lib/api";
import { useAuth } from "../auth/AuthContext";
import { AuthShell } from "./AuthShell";

// Mandatory 2FA enrollment shown after setup/login when TOTP is not yet enabled.
export function Enroll2FA() {
  const { refresh } = useAuth();
  const [enr, setEnr] = useState<Enrollment | null>(null);
  const [code, setCode] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .totpSetup()
      .then(setEnr)
      .catch(() => setErr("Could not start enrollment"));
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      await api.totpEnable(code);
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "Invalid code");
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthShell title="Enable two-factor authentication" subtitle="Scan the QR code with your authenticator app, then enter a code to confirm.">
      <div className="space-y-4">
        <div className="grid place-items-center">
          {enr ? (
            <img src={enr.qrDataUri} alt="TOTP QR code" className="rounded-lg bg-white p-2" width={200} height={200} />
          ) : (
            <div className="h-[200px] w-[200px] rounded-lg bg-panel2 animate-pulse" />
          )}
        </div>
        {enr && (
          <div className="text-center">
            <div className="text-xs text-muted">Or enter this secret manually</div>
            <code className="text-xs font-mono break-all text-text">{enr.secret}</code>
          </div>
        )}
        <form onSubmit={submit} className="space-y-3">
          <input
            className="input text-center tracking-[0.5em] text-lg font-mono"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
            inputMode="numeric"
            placeholder="000000"
          />
          {err && <p className="text-sm text-danger">{err}</p>}
          <button className="btn-primary w-full" disabled={busy || code.length !== 6}>
            {busy ? "Confirming…" : "Confirm & enable"}
          </button>
        </form>
      </div>
    </AuthShell>
  );
}
