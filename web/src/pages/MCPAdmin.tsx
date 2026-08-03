import { useCallback, useEffect, useState } from "react";
import { Trash2, KeyRound, Clock, ShieldCheck, Plug, User, Timer } from "lucide-react";
import { api } from "../lib/api";
import type { AdminMCPToken, AdminOAuthClient, MCPTokenPolicy } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import clsx from "clsx";
import { Tabs } from "../components/Tabs";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";

// MCP Admin — a fleet-wide view of every user's MCP credentials. Unlike the
// self-service "MCP Access" page (each user sees only their own tokens), this
// lists ALL active API tokens with their owners and every registered OAuth
// client, and lets an admin revoke/delete any of them. Admin-only: the route is
// gated by the "__admin" section on the backend.
export function MCPAdmin() {
  const dialogs = useDialogs();
  const [tokens, setTokens] = useState<AdminMCPToken[] | null>(null);
  const [clients, setClients] = useState<AdminOAuthClient[] | null>(null);
  const [tab, setTab] = useState<"tokens" | "clients" | "policy">("tokens");

  const load = useCallback(() => {
    api.mcpAdminTokens().then(setTokens).catch(() => setTokens([]));
    api.mcpAdminOAuthClients().then(setClients).catch(() => setClients([]));
  }, []);
  useEffect(() => load(), [load]);

  const revokeToken = async (t: AdminMCPToken) => {
    if (!(await dialogs.confirm({
      title: "Revoke token",
      message: <>Revoke <code className="font-mono text-text">{t.name || `token #${t.id}`}</code> owned by <strong>{t.username}</strong>? Any tool using it loses access immediately.</>,
      danger: true,
      confirmLabel: "Revoke",
    }))) return;
    await api.mcpAdminRevokeToken(t.id);
    load();
  };

  const deleteClient = async (c: AdminOAuthClient) => {
    if (!(await dialogs.confirm({
      title: "Remove OAuth client",
      message: <>Remove client <code className="font-mono text-text">{c.name || c.id}</code>? Its codes and refresh tokens are purged <strong>and any access token it already holds stops working immediately</strong>, so a tool connected through it loses access at once and must re-authorize.</>,
      danger: true,
      confirmLabel: "Remove",
    }))) return;
    await api.mcpAdminDeleteOAuthClient(c.id);
    load();
  };

  if (!tokens || !clients) return (<><PageHeader title="MCP Admin" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="MCP Admin" />
      <div className="p-6 space-y-4">
        <Tabs
          active={tab}
          onChange={setTab}
          tabs={[
            { key: "tokens", label: "API tokens", icon: <KeyRound className="h-4 w-4" />, count: tokens.length },
            { key: "clients", label: "OAuth clients", icon: <Plug className="h-4 w-4" />, count: clients.length },
            { key: "policy", label: "Token policy", icon: <Timer className="h-4 w-4" /> },
          ]}
        />
        <section className={clsx("space-y-3", tab !== "tokens" && "hidden")}>
          <p className="text-xs text-muted">Every active API token across all users.</p>
          {tokens.length === 0 ? (
            <EmptyState title="No active tokens" hint="Tokens users create on the MCP Access page appear here." />
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              {tokens.map((t) => (
                <div key={t.id} className="card p-4">
                  <div className="flex items-start justify-between">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 font-medium">
                        <KeyRound className="h-4 w-4 text-accent" /> {t.name || `token #${t.id}`}
                        {t.readOnly && <span className="text-[10px] uppercase tracking-wide bg-panel2 text-muted rounded px-1.5 py-0.5">read-only</span>}
                      </div>
                      <div className="text-xs text-muted mt-1 flex items-center gap-1">
                        <User className="h-3 w-3" /> {t.username}
                      </div>
                      <div className="text-xs text-muted mt-0.5">
                        {t.sections && t.sections.length > 0 ? <>scope: {t.sections.join(", ")}</> : <>scope: all the owner's sections</>}
                      </div>
                      <div className="text-xs text-muted mt-0.5 flex flex-wrap gap-x-3">
                        <span>created {fmtDate(t.createdAt)}</span>
                        {t.lastUsedAt && <span>· last used {fmtDate(t.lastUsedAt)}</span>}
                        {t.expiresAt && <span className="flex items-center gap-1"><Clock className="h-3 w-3" /> expires {fmtDate(t.expiresAt)}</span>}
                      </div>
                    </div>
                    <button className="btn-ghost px-2 py-1 text-danger" title="Revoke" onClick={() => revokeToken(t)}><Trash2 className="h-4 w-4" /></button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        <section className={clsx("space-y-3", tab !== "clients" && "hidden")}>
          <p className="text-xs text-muted">Connectors registered through the OAuth flow.</p>
          {clients.length === 0 ? (
            <EmptyState title="No OAuth clients" hint="Clients self-register when a Claude Desktop / Cursor connector first authorizes against this server." />
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              {clients.map((c) => (
                <div key={c.id} className="card p-4">
                  <div className="flex items-start justify-between">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 font-medium"><Plug className="h-4 w-4 text-accent" /> {c.name || "Unnamed client"}</div>
                      <div className="text-xs text-muted mt-1 font-mono break-all">{c.id}</div>
                      {c.redirectUris && c.redirectUris.length > 0 && (
                        <div className="text-xs text-muted mt-0.5 break-all">redirect: {c.redirectUris.join(", ")}</div>
                      )}
                      <div className="text-xs text-muted mt-0.5">registered {fmtDate(c.createdAt)}</div>
                    </div>
                    <button className="btn-ghost px-2 py-1 text-danger" title="Remove" onClick={() => deleteClient(c)}><Trash2 className="h-4 w-4" /></button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        <section className={clsx("space-y-3", tab !== "policy" && "hidden")}>
          <TokenPolicyEditor />
        </section>

        <p className="text-xs text-muted flex items-start gap-1.5">
          <ShieldCheck className="h-4 w-4 text-accent shrink-0 mt-0.5" />
          Tokens authenticate AI tools as their owner and never exceed that user's live permissions. Revoking a token or
          removing a client takes effect immediately. Secrets are never shown here — only metadata.
        </p>
      </div>
    </>
  );
}

// The lifetime rules applied when anyone mints an MCP token.
//
// An expiry date is the only control here that works when nobody is paying
// attention: revocation needs someone to remember, and the tokens most worth
// revoking are the ones everybody has forgotten. Hence a default that expires,
// a ceiling so "no never-expiring tokens" cannot be sidestepped by asking for a
// hundred years, and an explicit switch for installations that genuinely need
// a permanent credential.
function TokenPolicyEditor() {
  const [policy, setPolicy] = useState<MCPTokenPolicy | null>(null);
  const [saved, setSaved] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.mcpAdminTokenPolicy().then(setPolicy).catch((e) => setErr(e instanceof Error ? e.message : "failed"));
  }, []);

  if (err && !policy) return <p className="text-xs text-danger">{err}</p>;
  if (!policy) return <div className="flex items-center gap-2 text-muted text-sm"><Spinner /> Loading…</div>;

  const set = (patch: Partial<MCPTokenPolicy>) => { setPolicy({ ...policy, ...patch }); setSaved(false); };

  const save = async () => {
    setBusy(true); setErr("");
    try {
      // Take the server's normalised answer rather than keeping the typed one:
      // it repairs contradictions (a ceiling below the default, or none at all
      // while never-expiring tokens are off), and the admin should see what is
      // actually in force.
      setPolicy(await api.mcpAdminSetTokenPolicy(policy));
      setSaved(true);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card p-5 space-y-4 max-w-2xl">
      <p className="text-xs text-muted">
        Applies when a token is <strong>created</strong>. Tokens that already exist keep the expiry they were given —
        tightening this will not cut off a running integration, so review the list above if you want existing
        never-expiring tokens gone.
      </p>

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div>
          <label className="label">Default lifetime (days)</label>
          <input type="number" min={1} className="input" value={policy.defaultDays}
            onChange={(e) => set({ defaultDays: Number(e.target.value) })} />
          <p className="text-xs text-muted mt-1">Pre-selected in the creation form, and used when a client asks for no particular lifetime.</p>
        </div>
        <div>
          <label className="label">Maximum lifetime (days)</label>
          <input type="number" min={0} className="input" value={policy.maxDays}
            onChange={(e) => set({ maxDays: Number(e.target.value) })} />
          <p className="text-xs text-muted mt-1">
            The longest anyone may choose. {policy.allowUnlimited ? "0 = no ceiling." : "Required while never-expiring tokens are off — otherwise the rule can be sidestepped with a very large number."}
          </p>
        </div>
      </div>

      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={policy.allowUnlimited}
          onChange={(e) => set({ allowUnlimited: e.target.checked })} />
        <span>
          Allow never-expiring tokens
          <span className="block text-xs text-muted">
            Off by default. A token with no expiry outlives the laptop it was pasted into and the person it was made for.
          </span>
        </span>
      </label>

      {err && <p className="text-xs text-danger">{err}</p>}
      <div className="flex items-center gap-3">
        <button className="btn-primary" disabled={busy} onClick={save}>{busy ? "Saving…" : "Save policy"}</button>
        {saved && <span className="text-xs text-accent">Saved.</span>}
      </div>
    </div>
  );
}

function fmtDate(s: string): string {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
}
