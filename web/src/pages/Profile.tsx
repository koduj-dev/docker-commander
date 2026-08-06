import { useCallback, useEffect, useState } from "react";
import { Loader2, Mail, ShieldCheck, IdCard, KeyRound, Check, RefreshCw, X, SlidersHorizontal, Monitor, LogOut, Smartphone, Tablet, Terminal as TerminalIcon, HelpCircle, Plus, Trash2 } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { Enrollment } from "../lib/api";
import type { MyAccess, Session, AuthFactor } from "../lib/types";
import { sectionLabel } from "../lib/sections";
import { PageHeader } from "../layout/Shell";
import { Tabs } from "../components/Tabs";
import { getPref, setPref } from "../lib/prefs";
import { EmptyState, Spinner } from "../components/ui";
import { useAuth } from "../auth/AuthContext";
import { useDialogs } from "../components/Dialog";
import { describeClient, sinceLabel, type ClientKind } from "../lib/userAgent";
import { createPasskey, describePasskeyError, passkeysSupported } from "../lib/webauthn";

type Tab = "account" | "security" | "access" | "prefs";

/**
 * The signed-in user's own page: who we think they are, where their alerts go,
 * their authenticator, and — the part that is otherwise invisible — exactly which
 * sections they can reach and which role each permission came from.
 *
 * Everything here is self-service and reads only this account.
 */
export function Profile() {
  const { user, refresh } = useAuth();
  const [tab, setTab] = useState<Tab>("account");
  const [access, setAccess] = useState<MyAccess | null>(null);
  // Separate from `access` being null: that meant both "still loading" and
  // "the request failed", so a failure left the page spinning forever.
  const [accessErr, setAccessErr] = useState("");

  const load = useCallback(() => {
    setAccessErr("");
    api.myAccess().then(setAccess).catch((e) => {
      setAccess(null);
      setAccessErr(e instanceof Error ? e.message : "could not load your permissions");
    });
  }, []);
  useEffect(() => load(), [load]);

  if (!user) return (<><PageHeader title="My profile" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  // No badge for an admin (their access isn't a computed list) and none while it
  // is still loading — a hard-coded section count would silently drift the day a
  // section is added.
  const grantCount = access && !access.admin ? (access.effective ?? []).length : undefined;

  return (
    <>
      <PageHeader title="My profile" />
      <div className="p-6 space-y-4">
        <Tabs
          active={tab}
          onChange={setTab}
          tabs={[
            { key: "account", label: "Account", icon: <IdCard className="h-4 w-4" /> },
            { key: "security", label: "Security", icon: <ShieldCheck className="h-4 w-4" /> },
            { key: "access", label: "Access", icon: <KeyRound className="h-4 w-4" />, count: grantCount },
            { key: "prefs", label: "Preferences", icon: <SlidersHorizontal className="h-4 w-4" /> },
          ]}
        />

        {tab === "account" && <AccountTab onSaved={refresh} />}
        {tab === "security" && <SecurityTab onChanged={refresh} />}
        {tab === "access" && <AccessTab access={access} error={accessErr} onRetry={load} />}
        {tab === "prefs" && <PrefsTab />}
      </div>
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline gap-3 text-sm">
      <span className="w-36 shrink-0 text-muted">{label}</span>
      <span className="min-w-0">{children}</span>
    </div>
  );
}

function AccountTab({ onSaved }: { onSaved: () => Promise<void> }) {
  const { user } = useAuth();
  const [email, setEmail] = useState(user?.email ?? "");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      await api.setMyEmail(email.trim());
      await onSaved();
      setMsg({ ok: true, text: "Saved." });
    } catch (err) {
      setMsg({ ok: false, text: err instanceof Error ? err.message : "could not save" });
    } finally { setBusy(false); }
  };

  const fmt = (v?: string) => (v && !v.startsWith("0001-01-01") ? new Date(v).toLocaleString() : "—");

  return (
    <div className="space-y-4 max-w-2xl">
      <div className="card p-5 space-y-3">
        <div className="flex items-center gap-2 font-medium"><IdCard className="h-4 w-4 text-accent" /> Who you are</div>
        <div className="space-y-1.5">
          <Field label="Username"><span className="font-medium">{user?.username}</span></Field>
          <Field label="Account type">
            {user?.role === "admin"
              ? <span className="text-accent">admin</span>
              : user?.readOnly ? <span className="text-warn">user · read-only</span> : "user"}
          </Field>
          <Field label="Signs in with">
            {user?.authSource === "ldap"
              ? <>LDAP directory <span className="text-xs text-muted">— your password is verified there, not stored here</span></>
              : "a password stored here"}
          </Field>
          <Field label="Account created">{fmt(user?.createdAt)}</Field>
          <Field label="Last sign-in">{fmt(user?.lastLoginAt)}</Field>
        </div>
      </div>

      <form onSubmit={save} className="card p-5 space-y-3">
        <div className="flex items-center gap-2 font-medium"><Mail className="h-4 w-4 text-accent" /> Alert e-mail</div>
        <input className="input" type="email" value={email} placeholder="you@example.com"
          onChange={(e) => setEmail(e.target.value)} />
        <p className="text-xs text-muted">
          Prefilled as the recipient when you switch on e-mail for an alert rule. Leave it empty and those
          rules fall back to the instance-wide recipient instead. Nothing else uses this address.
          {user?.authSource === "ldap" && " Your directory may overwrite it at sign-in if it publishes one."}
        </p>
        {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
        <div className="flex justify-end">
          <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Save
          </button>
        </div>
      </form>
    </div>
  );
}

// SecurityTab shows 2FA status and pairs a new authenticator. Starting the flow is
// safe: the new secret is held aside server-side and only replaces the working one
// once a code from the new device is accepted, so abandoning this leaves the
// authenticator you already have untouched.
function SecurityTab({ onChanged }: { onChanged: () => Promise<void> }) {
  const { user } = useAuth();
  const [enr, setEnr] = useState<Enrollment | null>(null);
  const [code, setCode] = useState("");
  const [deviceName, setDeviceName] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState<"" | "start" | "confirm">("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [factors, setFactors] = useState<AuthFactor[] | null>(null);
  const [factorsErr, setFactorsErr] = useState("");
  const [passkeys, setPasskeys] = useState<{ available: boolean; reason: string } | null>(null);
  const [passkeyBusy, setPasskeyBusy] = useState(false);

  const loadFactors = useCallback(() => {
    setFactorsErr("");
    api.factors().then(setFactors).catch((e) =>
      setFactorsErr(e instanceof Error ? e.message : "could not load your authenticators"));
  }, []);
  useEffect(() => loadFactors(), [loadFactors]);
  useEffect(() => {
    // Whether the connection is a secure context is the server's answer; whether
    // the browser has the API at all is ours. Both have to be true.
    if (!passkeysSupported()) {
      setPasskeys({ available: false, reason: "This browser does not support passkeys." });
      return;
    }
    api.passkeySupport().then(setPasskeys).catch(() => setPasskeys(null));
  }, []);

  const addPasskey = async () => {
    setPasskeyBusy(true); setMsg(null);
    try {
      // The password is required exactly when pairing anything else is: once the
      // account already has a second factor.
      const options = await api.passkeyRegisterBegin(user?.totpEnabled || (factors?.length ?? 0) > 0 ? password : undefined);
      const credential = await createPasskey(options);
      await api.passkeyRegisterFinish(deviceName.trim() || "Passkey", credential);
      setPassword(""); setDeviceName("");
      await onChanged();
      loadFactors();
      setMsg({ ok: true, text: "Passkey added. Everything you already had paired still works." });
    } catch (e) {
      setMsg({ ok: false, text: describePasskeyError(e) });
    } finally { setPasskeyBusy(false); }
  };

  const start = async (e?: React.FormEvent) => {
    e?.preventDefault();
    setBusy("start"); setMsg(null);
    try {
      // Adding an authenticator while one already works is a step-up, so the
      // server asks for the password; a first enrolment has nothing to protect.
      setEnr(await api.totpSetup(user?.totpEnabled ? password : undefined));
      setCode(""); setPassword("");
    } catch (e) {
      setMsg({ ok: false, text: e instanceof Error ? e.message : "could not start" });
    } finally { setBusy(""); }
  };

  const confirm = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy("confirm"); setMsg(null);
    try {
      await api.totpEnable(code.trim(), deviceName.trim());
      await onChanged();
      loadFactors();
      setEnr(null); setCode(""); setDeviceName("");
      setMsg({ ok: true, text: "Authenticator paired. Anything you had paired before still works." });
    } catch (err) {
      setMsg({ ok: false, text: err instanceof Error ? err.message : "that code was not accepted" });
    } finally { setBusy(""); }
  };

  return (
    // Sessions first: it is the part of this page you come to *read* — the 2FA
    // card is a control you touch when something changes.
    <div className="grid gap-4 xl:grid-cols-2 items-start">
      <SessionsCard />

      <div className="card p-5 space-y-3">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 font-medium"><ShieldCheck className="h-4 w-4 text-accent" /> Two-factor authentication</div>
          {user?.totpEnabled
            ? <span className="inline-flex items-center gap-1 text-sm text-ok"><Check className="h-3.5 w-3.5" /> enabled</span>
            : <span className="text-sm text-warn">not set up</span>}
        </div>

        <AuthenticatorList
          factors={factors}
          error={factorsErr}
          onRetry={loadFactors}
          onRemoved={async () => { loadFactors(); await onChanged(); }}
        />

        {!enr && (
          // Button first, hint underneath — the two used to share a flex row, which
          // centred the button against a wrapping two-line sentence and read as a
          // misalignment. This matches how hints sit under controls elsewhere.
          // Separated from the list above: that list has its own password field
          // (for removing), and two unlabelled boxes stacked together is how you
          // type the right password into the wrong form.
          <form className="space-y-1.5 border-t border-border pt-3" onSubmit={start}>
            {user?.totpEnabled && (
              <div className="max-w-xs space-y-1">
                <label className="label" htmlFor="repair-password">Your password</label>
                <input
                  id="repair-password"
                  className="input"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(ev) => setPassword(ev.target.value)}
                  required
                />
              </div>
            )}
            <button className="btn-ghost px-3 py-1.5 text-sm" type="submit" disabled={busy === "start"}>
              {busy === "start" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
              {user?.totpEnabled ? "Add an authenticator" : "Set up 2FA"}
            </button>
            {user?.totpEnabled && (
              <p className="text-xs text-muted">
                Your password is required because this changes what it takes to sign in as you —
                a stolen session alone must not be able to do it. Everything you already have
                paired keeps working; this adds one.
              </p>
            )}

            <div className="pt-2">
              <button
                type="button"
                className="btn-ghost px-3 py-1.5 text-sm disabled:opacity-40"
                onClick={addPasskey}
                disabled={passkeyBusy || !passkeys?.available}
                title={passkeys?.available ? "Use this device's fingerprint, face or security key" : passkeys?.reason}
              >
                {passkeyBusy ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}
                Add a passkey
              </button>
              <p className="text-xs text-muted mt-1">
                {passkeys?.available
                  ? "A passkey lives in this device's secure hardware and is tied to this site's address, so it cannot be phished or read out over the phone. Use it instead of a code."
                  : passkeys?.reason || "Checking whether this connection supports passkeys…"}
              </p>
            </div>

            {user?.mfaEnabled && user?.authSource !== "ldap" && (
              <PasswordlessToggle enabled={!!user.passwordless} onChanged={onChanged} />
            )}
          </form>
        )}

        {enr && (
          <form onSubmit={confirm} className="space-y-3 border-t border-border pt-3">
            <p className="text-sm">
              Scan this with your authenticator app, then enter the code it shows.
            </p>
            <div className="flex flex-wrap items-start gap-4">
              <img src={enr.qrDataUri} alt="Authenticator QR code" className="h-44 w-44 rounded-lg bg-white p-2" />
              <div className="space-y-2 min-w-0">
                <div className="text-xs text-muted">Can&apos;t scan? Enter this key by hand:</div>
                <code className="block font-mono text-xs break-all bg-panel2/50 rounded px-2 py-1">{enr.secret}</code>
                <input className="input font-mono tracking-widest" value={code} inputMode="numeric"
                  placeholder="123456" maxLength={6} onChange={(e) => setCode(e.target.value)} />
                <div className="space-y-1">
                  <label className="label" htmlFor="device-name">Name this device (optional)</label>
                  {/* Only ever shown back to its owner — it exists so "remove the
                      old phone" is a decision you can make without guessing. */}
                  <input id="device-name" className="input" value={deviceName} maxLength={64}
                    placeholder="Phone" onChange={(e) => setDeviceName(e.target.value)} />
                </div>
              </div>
            </div>
            {user?.totpEnabled && (
              <p className="text-xs text-warn">
                Nothing has changed yet, and nothing you already have paired will change. Cancel and
                this device simply never gets added.
              </p>
            )}
            {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
            <div className="flex justify-end gap-2">
              <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={() => { setEnr(null); setMsg(null); }}>
                <X className="h-4 w-4" /> Cancel
              </button>
              <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy === "confirm" || code.trim().length < 6}>
                {busy === "confirm" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />} Confirm
              </button>
            </div>
          </form>
        )}
        {!enr && msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
      </div>
    </div>
  );
}

// SessionsCard lists what is signed in as this account, and lets the owner end
// any of it.
//
// The point is recognition: a session you do not recognise is the only signal
// available that someone else is using your account, and until now there was
// nowhere to look. IP and browser are shown for that reason and no other — this
// is the account's own view, never an administrator's.
function SessionsCard() {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const dialogs = useDialogs();

  const load = useCallback(() => {
    // Clear first: an error left over from a failed attempt would otherwise sit
    // above a list that has since loaded fine, describing nothing.
    setErr("");
    api.sessions().then(setSessions).catch((e) => setErr(e instanceof Error ? e.message : "could not load sessions"));
  }, []);
  useEffect(() => load(), [load]);

  const revoke = async (s: Session) => {
    const ok = await dialogs.confirm({
      title: s.current ? "Sign out here?" : "Sign out that session?",
      message: s.current
        ? "This is the session you are using. Signing it out will return you to the login screen."
        : <>The device using <code className="font-mono text-text">{s.ip || "an unknown address"}</code> will be signed out immediately.</>,
      danger: true,
      confirmLabel: "Sign out",
    });
    if (!ok) return;
    setBusy(s.id); setErr("");
    try {
      await api.revokeSession(s.id);
      // Revoking your own session means the next request is unauthorized, so send
      // the browser somewhere that expects that rather than letting the page
      // discover it as a random failure.
      if (s.current) {
        window.location.assign("/");
        return;
      }
      load();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "could not sign that session out");
    } finally { setBusy(""); }
  };

  const revokeOthers = async () => {
    const ok = await dialogs.confirm({
      title: "Sign out everywhere else?",
      message: "Every other browser and device signed in as you will be signed out. This session stays.",
      danger: true,
      confirmLabel: "Sign out the others",
    });
    if (!ok) return;
    setBusy("others"); setErr("");
    try {
      await api.revokeOtherSessions();
      load();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "could not sign the other sessions out");
    } finally { setBusy(""); }
  };

  const others = (sessions ?? []).filter((s) => !s.current).length;

  return (
    <div className="card p-5 space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2 font-medium"><Monitor className="h-4 w-4 text-accent" /> Signed in</div>
          <p className="text-xs text-muted mt-1 max-w-md">
            Every browser and device holding a live session for your account. Only you can see this list.
          </p>
        </div>
        <button className="btn-ghost px-2 py-1.5 text-sm shrink-0" onClick={load} disabled={busy !== ""} title="Refresh">
          <RefreshCw className="h-4 w-4" />
        </button>
      </div>

      {err && (
        <div className="flex items-center gap-3">
          <p className="text-sm text-danger">{err}</p>
          <button className="btn-ghost px-2 py-1 text-sm" onClick={load}><RefreshCw className="h-3.5 w-3.5" /> Try again</button>
        </div>
      )}
      {!sessions ? (
        !err && <div className="flex items-center gap-2 text-muted text-sm"><Spinner /> Loading…</div>
      ) : (
        // Capped rather than unbounded: a long-lived account accumulates sessions,
        // and pushing the "change your password" advice off the bottom of the card
        // would hide the one instruction that ends all of them.
        <ul className="space-y-2 max-h-[26rem] overflow-y-auto pr-1">
          {sessions.map((s) => (
            <SessionRow
              key={s.id}
              session={s}
              busy={busy === s.id}
              disabled={busy !== ""}
              onRevoke={() => revoke(s)}
            />
          ))}
        </ul>
      )}

      <div className="flex items-center justify-between gap-3 border-t border-border pt-3">
        <p className="text-xs text-muted max-w-md">
          If something here isn&apos;t you, sign it out and <strong className="text-text">change your password</strong> —
          that ends every session at once, including the one you missed.
        </p>
        {others > 0 && (
          <button className="btn-ghost px-3 py-1.5 text-sm shrink-0" onClick={revokeOthers} disabled={busy !== ""}>
            {busy === "others" ? <Loader2 className="h-4 w-4 animate-spin" /> : <LogOut className="h-4 w-4" />}
            Sign out everywhere else
          </button>
        )}
      </div>
    </div>
  );
}

// AuthenticatorList shows what is paired and lets the owner unpair one.
//
// The removal asks for the password inline rather than through the shared confirm
// dialog: that dialog's input is plain text, and a password typed into it would be
// on screen. It is also the honest shape — this is a step-up, not a yes/no.
function AuthenticatorList({ factors, error, onRetry, onRemoved }: {
  factors: AuthFactor[] | null;
  error: string;
  onRetry: () => void;
  onRemoved: () => Promise<void>;
}) {
  const [removing, setRemoving] = useState<number | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const cancel = () => { setRemoving(null); setPassword(""); setErr(""); };

  const remove = async (e: React.FormEvent, id: number) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      await api.removeFactor(id, password);
      cancel();
      await onRemoved();
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : "could not remove that authenticator");
    } finally { setBusy(false); }
  };

  if (error) {
    return (
      <div className="flex items-center gap-3">
        <p className="text-sm text-danger">{error}</p>
        <button className="btn-ghost px-2 py-1 text-sm" onClick={onRetry}><RefreshCw className="h-3.5 w-3.5" /> Try again</button>
      </div>
    );
  }
  if (!factors) return <div className="flex items-center gap-2 text-muted text-sm"><Spinner /> Loading…</div>;
  if (factors.length === 0) return null; // the "set up 2FA" button below says everything

  const last = factors.length === 1;

  return (
    <ul className="space-y-2">
      {factors.map((f) => (
        <li key={f.id} className="rounded-lg border border-border p-3">
          <div className="flex items-center gap-3">
            {f.kind === "webauthn"
              ? <KeyRound className="h-5 w-5 shrink-0 text-muted" />
              : <Smartphone className="h-5 w-5 shrink-0 text-muted" />}
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="truncate text-sm font-medium">{f.name}</span>
                <span className="shrink-0 rounded border border-border px-1 text-[10px] uppercase tracking-wide text-muted">
                  {f.kind === "webauthn" ? "passkey" : "app"}
                </span>
              </div>
              <div className="mt-0.5 text-xs text-muted">
                added {sinceLabel(f.createdAt)} ·{" "}
                {f.lastUsedAt && !f.lastUsedAt.startsWith("0001-01-01")
                  ? `last used ${sinceLabel(f.lastUsedAt)}`
                  : "never used"}
              </div>
            </div>
            <button
              className="btn-ghost shrink-0 px-2 py-1 text-danger disabled:opacity-40"
              onClick={() => { setRemoving(removing === f.id ? null : f.id); setPassword(""); setErr(""); }}
              disabled={last}
              title={last
                ? "This is your only second factor — add another one before removing it"
                : "Remove this authenticator"}
            >
              <Trash2 className="h-4 w-4" />
            </button>
          </div>

          {removing === f.id && (
            <form onSubmit={(e) => remove(e, f.id)} className="mt-3 space-y-2 border-t border-border pt-3">
              <label className="label" htmlFor={`remove-pw-${f.id}`}>Your password</label>
              <input
                id={`remove-pw-${f.id}`}
                className="input max-w-xs"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
              <p className="text-xs text-muted">
                Removing an authenticator changes what it takes to sign in as you, so it needs
                your password — the same reason adding one does.
              </p>
              {err && <p className="text-sm text-danger">{err}</p>}
              <div className="flex justify-end gap-2">
                <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={cancel}>
                  <X className="h-4 w-4" /> Cancel
                </button>
                <button className="btn-danger px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy || !password}>
                  {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />} Remove
                </button>
              </div>
            </form>
          )}
        </li>
      ))}
      {last && (
        <p className="text-xs text-muted">
          This is your only second factor, so it cannot be removed — an account with none could not
          sign in at all. Add another one first, then remove this.
        </p>
      )}
    </ul>
  );
}

// deviceIcon picks the glyph for a parsed client. A phone that looks like a phone
// is the fastest way to answer "is that me?" — the row is scanned, not read.
function deviceIcon(kind: ClientKind) {
  const cls = "h-5 w-5";
  switch (kind) {
    case "mobile": return <Smartphone className={cls} />;
    case "tablet": return <Tablet className={cls} />;
    case "tool": return <TerminalIcon className={cls} />;
    case "desktop": return <Monitor className={cls} />;
    default: return <HelpCircle className={cls} />;
  }
}

// SessionRow is one signed-in client. Three facts, in the order someone checks
// them: what it is, where it is, when it was last used.
function SessionRow({ session, busy, disabled, onRevoke }: {
  session: Session;
  busy: boolean;
  disabled: boolean;
  onRevoke: () => void;
}) {
  const client = describeClient(session.userAgent);
  return (
    <li
      className={clsx(
        "flex items-center gap-3 rounded-lg border p-3",
        session.current ? "border-ok/40 bg-ok/5" : "border-border",
      )}
    >
      <div className={clsx("shrink-0", session.current ? "text-ok" : "text-muted")}>{deviceIcon(client.kind)}</div>

      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          {/* The parse is a summary of a string the client chose; the full value
              stays available rather than being thrown away. */}
          <span className="truncate text-sm font-medium" title={client.raw || undefined}>{client.label}</span>
          {session.current && (
            <span className="shrink-0 rounded border border-ok/40 px-1 text-[10px] uppercase tracking-wide text-ok">this device</span>
          )}
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted">
          <span className="font-mono">{session.ip || "unknown address"}</span>
          <span aria-hidden>·</span>
          <span>last used {sinceLabel(session.lastSeenAt)}</span>
          <span aria-hidden>·</span>
          <span>signed in {sinceLabel(session.createdAt)}</span>
        </div>
      </div>

      <button
        className="btn-ghost shrink-0 px-2 py-1 text-danger"
        onClick={onRevoke}
        disabled={disabled}
        title={session.current ? "Sign out here" : "Sign this session out"}
      >
        {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <LogOut className="h-4 w-4" />}
      </button>
    </li>
  );
}

// AccessTab shows what this account can actually reach and where each permission
// came from — the overlay of its roles and its own section grants.
function AccessTab({ access, error, onRetry }: { access: MyAccess | null; error: string; onRetry: () => void }) {
  if (error) {
    return (
      <div className="card p-5 space-y-3 max-w-2xl">
        <p className="text-sm text-danger">Could not load your permissions: {error}</p>
        <p className="text-xs text-muted">
          This page reads your own account only, so a failure here means the request didn&apos;t reach the
          server — it does not mean you have no access.
        </p>
        <button className="btn-ghost px-3 py-1.5 text-sm self-start" onClick={onRetry}>
          <RefreshCw className="h-4 w-4" /> Try again
        </button>
      </div>
    );
  }
  if (!access) return <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;

  if (access.admin) {
    const all = access.allSections ?? [];
    return (
      <div className="card p-5 space-y-3 max-w-3xl">
        <div className="flex items-center gap-2 font-medium"><KeyRound className="h-4 w-4 text-accent" /> What you can reach</div>
        <p className="text-sm">
          You are an <span className="text-accent">admin</span>. That is not a role and not a grant —
          it bypasses the permission system, so there is no overlay to compute. Concretely, it means
          all {all.length} sections, read and write, on{" "}
          {access.hostCount === 1 ? "the one configured host" : `all ${access.hostCount ?? 0} configured hosts`},
          plus administration itself: users, roles, settings, LDAP and the audit log.
        </p>
        <table className="w-full text-sm">
          <thead className="text-muted text-xs uppercase tracking-wide">
            <tr className="border-b border-border">
              <th className="text-left font-medium py-2">Section</th>
              <th className="text-left font-medium py-2">You can</th>
              <th className="text-left font-medium py-2">Where</th>
              <th className="text-left font-medium py-2">Granted by</th>
            </tr>
          </thead>
          <tbody>
            {all.map((s) => (
              <tr key={s} className="border-b border-border/50">
                <td className="py-2 font-medium">{sectionLabel(s)}</td>
                <td className="py-2 text-ok">view and change</td>
                <td className="py-2 text-xs text-muted">every host</td>
                <td className="py-2 text-xs text-muted">your admin account</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="text-xs text-muted">
          Sections an admin has turned off installation-wide are hidden from the menu, but an admin can
          still reach their APIs — the feature flag is not a permission.
        </p>
      </div>
    );
  }

  const effective = access.effective ?? [];
  return (
    <div className="space-y-4 max-w-3xl">
      <div className="card p-5 space-y-3">
        <div className="flex items-center gap-2 font-medium"><IdCard className="h-4 w-4 text-accent" /> Your roles</div>
        {access.roles.length === 0 ? (
          <p className="text-sm text-muted">
            No roles assigned. Anything you can reach comes from grants set directly on your account.
          </p>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
            {access.roles.map((r) => (
              <div key={r.id} className="rounded-lg border border-border p-3">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{r.name}</span>
                  {r.builtin && <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">built-in</span>}
                </div>
                {r.description && <div className="text-xs text-muted mt-0.5">{r.description}</div>}
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="card p-5 space-y-3">
        <div className="flex items-center gap-2 font-medium"><KeyRound className="h-4 w-4 text-accent" /> What you can reach</div>
        {access.readOnly && (
          <p className="text-xs text-warn">
            Your account is read-only, so every grant below is capped to reading — even where a role
            allows writing.
          </p>
        )}
        {effective.length === 0 ? (
          <EmptyState title="No sections granted" hint="An admin assigns you a role or grants sections on your account." />
        ) : (
          <table className="w-full text-sm">
            <thead className="text-muted text-xs uppercase tracking-wide">
              <tr className="border-b border-border">
                <th className="text-left font-medium py-2">Section</th>
                <th className="text-left font-medium py-2">You can</th>
                <th className="text-left font-medium py-2">Where</th>
                <th className="text-left font-medium py-2">Granted by</th>
              </tr>
            </thead>
            <tbody>
              {effective.map((g) => (
                <tr key={g.section} className="border-b border-border/50">
                  <td className="py-2 font-medium">{sectionLabel(g.section)}</td>
                  <td className="py-2">
                    {g.write
                      ? <span className="text-ok">view and change</span>
                      : <span className="text-muted">view only</span>}
                  </td>
                  <td className="py-2 text-xs text-muted">
                    {g.allHosts === false
                      ? `local + ${(g.hosts ?? []).length} host(s)`
                      : "every host"}
                  </td>
                  <td className="py-2 text-xs text-muted">{g.from.join(", ") || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="text-xs text-muted">
          Sections an admin has turned off installation-wide never appear here, even if a role grants them.
          <b> Where</b> is the set of Docker hosts a grant reaches; the local daemon is always included.
        </p>
      </div>
    </div>
  );
}

// PrefsTab holds per-account UI preferences. They are stored server-side, so a
// choice made here follows the account to another browser rather than being lost
// with local storage.
function PrefsTab() {
  const [toasts, setToasts] = useState(() => getPref("alerts.toasts", true));

  const change = (v: boolean) => {
    setToasts(v);
    setPref("alerts.toasts", v);
  };

  return (
    <div className="card p-4 space-y-4 max-w-2xl">
      <div>
        <div className="font-medium">Notifications</div>
        <p className="text-xs text-muted mt-1">Only affects this account.</p>
      </div>
      <label className="flex items-start gap-3">
        <input
          type="checkbox"
          className="mt-1"
          checked={toasts}
          onChange={(e) => change(e.target.checked)}
        />
        <span>
          <span className="block text-sm">Pop up alerts while I have Docker Commander open</span>
          <span className="block text-xs text-muted mt-0.5">
            A short notification in the corner when an alert fires, with a countdown you can pause by hovering.
            Turning this off changes nothing about the alerts themselves — they are still recorded, still counted in
            the sidebar badge, and still delivered by webhook and e-mail.
          </span>
        </span>
      </label>
    </div>
  );
}

// PasswordlessToggle lets the owner decide whether a passkey may be the WHOLE
// login, rather than the second half of one.
//
// Off unless asked for, and asking costs the password — the same rule as pairing,
// for a stronger reason. A passkey accepted as a second factor was accepted while
// the password still stood in front of it; this removes that. For a passkey that
// syncs (iCloud Keychain, Google Password Manager) it also moves the account onto
// whatever platform account it syncs through, because the PIN or fingerprint can be
// satisfied on any device that credential reaches. That is a reasonable trade for
// many people and a bad one for others, which is exactly why it is a choice.
function PasswordlessToggle({ enabled, onChanged }: { enabled: boolean; onChanged: () => Promise<void> }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState(false);

  const apply = async (next: boolean) => {
    setBusy(true); setErr("");
    try {
      await api.setPasswordless(next, password);
      setPassword(""); setOpen(false);
      await onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "could not save");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="border-t border-border pt-3">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium">Sign in with a passkey alone</p>
          <p className="text-xs text-muted mt-1">
            {enabled
              ? "On. Your passkey can sign you in without your password, as long as it verifies you with a PIN, fingerprint or face. Your password still works."
              : "Off. Your passkey is a second factor: your password comes first. Turning this on lets the passkey be the whole login."}
          </p>
        </div>
        <button type="button" className="btn-ghost px-3 py-1.5 text-sm shrink-0" onClick={() => setOpen((v) => !v)}>
          {enabled ? "Turn off" : "Turn on"}
        </button>
      </div>

      {open && (
        <div className="mt-3 space-y-2">
          {!enabled && (
            <p className="text-xs text-muted">
              Worth knowing: if your passkey syncs between your devices, whoever controls that
              account can use it. Your password stays valid either way — it is how you get back
              in if the key is lost.
            </p>
          )}
          <input
            className="input"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Your password"
            autoComplete="current-password"
          />
          <div className="flex gap-2">
            <button
              type="button"
              className="btn-primary px-3 py-1.5 text-sm"
              onClick={() => apply(!enabled)}
              disabled={busy || !password}
            >
              {busy ? "Saving…" : enabled ? "Turn off" : "Turn on"}
            </button>
            <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={() => { setOpen(false); setPassword(""); setErr(""); }}>
              Cancel
            </button>
          </div>
          {err && <p className="text-xs text-danger">{err}</p>}
        </div>
      )}
    </div>
  );
}
