import { useCallback, useEffect, useState } from "react";
import { Trash2, KeyRound, Clock, ShieldCheck, Plug, User } from "lucide-react";
import { api } from "../lib/api";
import type { AdminMCPToken, AdminOAuthClient } from "../lib/types";
import { PageHeader } from "../layout/Shell";
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
      message: <>Remove client <code className="font-mono text-text">{c.name || c.id}</code>? Its codes and refresh tokens are purged, so any tool connected through it must re-authorize.</>,
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
      <div className="p-6 space-y-8">
        <section className="space-y-3">
          <h2 className="text-sm font-semibold flex items-center gap-2"><KeyRound className="h-4 w-4 text-accent" /> API tokens <span className="text-muted font-normal">— all users</span></h2>
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

        <section className="space-y-3">
          <h2 className="text-sm font-semibold flex items-center gap-2"><Plug className="h-4 w-4 text-accent" /> OAuth clients <span className="text-muted font-normal">— registered connectors</span></h2>
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

        <p className="text-xs text-muted flex items-start gap-1.5">
          <ShieldCheck className="h-4 w-4 text-accent shrink-0 mt-0.5" />
          Tokens authenticate AI tools as their owner and never exceed that user's live permissions. Revoking a token or
          removing a client takes effect immediately. Secrets are never shown here — only metadata.
        </p>
      </div>
    </>
  );
}

function fmtDate(s: string): string {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
}
