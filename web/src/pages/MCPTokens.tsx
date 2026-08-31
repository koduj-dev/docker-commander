import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, KeyRound, Copy, Check, Clock, ShieldCheck } from "lucide-react";
import { api } from "../lib/api";
import { useAuth } from "../auth/AuthContext";
import type { MCPToken, MCPStatus, MCPTokenPolicy } from "../lib/types";
import { lifetimeOptions, defaultLifetimeIndex } from "../lib/tokenPolicy";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";
import { SECTION_LABELS, sectionLabel } from "../lib/sections";

// Canonical section list (keys mirror the backend's store.Sections). Admins may
// scope a token to any of these; other users only to their own sections.
const ALL_SECTIONS = Object.keys(SECTION_LABELS);

export function MCPTokens() {
  const { user } = useAuth();
  const dialogs = useDialogs();
  const [tokens, setTokens] = useState<MCPToken[] | null>(null);
  const [status, setStatus] = useState<MCPStatus | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [created, setCreated] = useState<string | null>(null);

  const load = useCallback(() => {
    api.mcpTokens().then(setTokens).catch(() => setTokens([]));
    api.mcpStatus().then(setStatus).catch(() => setStatus(null));
  }, []);
  useEffect(() => load(), [load]);

  const del = async (t: MCPToken) => {
    if (!(await dialogs.confirm({
      title: "Revoke token",
      message: <>Revoke <code className="font-mono text-text">{t.name || `token #${t.id}`}</code>? Any tool using it loses access immediately.</>,
      danger: true,
      confirmLabel: "Revoke",
    }))) return;
    await api.deleteMcpToken(t.id);
    load();
  };

  if (!tokens) return (<><PageHeader title="MCP Access" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="MCP Access" actions={<button className="btn-primary" onClick={() => setShowForm((v) => !v)}><Plus className="h-4 w-4" /> Create token</button>} />
      <div className="p-6 space-y-4">
        {status && !status.enabled && (
          <div className="card p-4 text-sm text-muted border-warning/40">
            The MCP server is currently <strong className="text-text">disabled</strong> on this host. Tokens you create here
            will work only once an administrator enables it (<code>DC_MCP_ENABLED=1</code>, behind HTTPS).
          </div>
        )}

        {showForm && (
          <MCPTokenForm
            sections={user?.role === "admin" ? ALL_SECTIONS : (user?.sections ?? [])}
            ownerReadOnly={user?.readOnly ?? false}
            policy={status?.tokenPolicy ?? FALLBACK_POLICY}
            onCancel={() => setShowForm(false)}
            onDone={(secret) => { setShowForm(false); setCreated(secret); load(); }}
          />
        )}

        {tokens.length === 0 ? (
          <EmptyState title="No tokens yet" hint="Create a token to connect an AI tool to the MCP server." />
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
                    <div className="text-xs text-muted mt-1">
                      {t.sections && t.sections.length > 0 ? <>scope: {t.sections.join(", ")}</> : <>scope: all your sections</>}
                    </div>
                    <div className="text-xs text-muted mt-0.5 flex flex-wrap gap-x-3">
                      <span>created {fmtDate(t.createdAt)}</span>
                      {t.lastUsedAt && <span>· last used {fmtDate(t.lastUsedAt)}</span>}
                      {t.expiresAt && <span className="flex items-center gap-1"><Clock className="h-3 w-3" /> expires {fmtDate(t.expiresAt)}</span>}
                    </div>
                  </div>
                  <button className="btn-ghost px-2 py-1 text-danger" title="Revoke" onClick={() => del(t)}><Trash2 className="h-4 w-4" /></button>
                </div>
              </div>
            ))}
          </div>
        )}

        <p className="text-xs text-muted">
          A token authenticates an AI tool to the MCP server <strong>as you</strong>: it can never exceed your own
          permissions, and you can narrow it further to specific sections or read-only. The secret is shown once and
          stored only as a hash — revoke and recreate if you lose it.
        </p>
      </div>

      {created && <SecretModal secret={created} onClose={() => setCreated(null)} />}
    </>
  );
}

// Used only until /api/mcp/status answers. It matches the server's own default
// rather than being permissive: a form that briefly offers "never" and then
// takes it away is worse than one that starts conservative.
const FALLBACK_POLICY: MCPTokenPolicy = { defaultDays: 30, maxDays: 365, allowUnlimited: false };

function MCPTokenForm({ sections, ownerReadOnly, policy, onCancel, onDone }: {
  sections: string[];
  ownerReadOnly: boolean;
  policy: MCPTokenPolicy;
  onCancel: () => void;
  onDone: (secret: string) => void;
}) {
  const [name, setName] = useState("");
  const [readOnly, setReadOnly] = useState(ownerReadOnly);
  const [picked, setPicked] = useState<string[]>([]);
  const options = lifetimeOptions(policy);
  // The value is the option's index: "never" and "0 days" would otherwise both
  // be 0 in a <select>, and those mean opposite things.
  const [lifetimeIdx, setLifetimeIdx] = useState(() => defaultLifetimeIndex(options, policy));
  // The policy can arrive after the form is already open (the page renders as
  // soon as the token list loads, while /api/mcp/status is still in flight). The
  // options list changes underneath the index when it does, so an index chosen
  // against the placeholder policy would silently come to mean a different
  // lifetime. Re-seed rather than clamp: a choice made against the wrong menu is
  // not a choice worth preserving.
  //
  // Keyed on the policy's VALUES, not the object. The page reloads its token
  // list after a revoke, handing down a fresh but identical policy object; on
  // object identity that would wipe a selection the user had already made, for
  // no reason they could see.
  useEffect(() => {
    setLifetimeIdx(defaultLifetimeIndex(lifetimeOptions(policy), policy));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [policy.defaultDays, policy.maxDays, policy.allowUnlimited]);
  const lifetime = options[lifetimeIdx] ?? options[0];
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const toggle = (s: string) => setPicked((p) => (p.includes(s) ? p.filter((x) => x !== s) : [...p, s]));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(""); setBusy(true);
    try {
      const res = await api.createMcpToken({
        name, readOnly, sections: picked,
        expiresInDays: lifetime.never ? 0 : lifetime.days,
        neverExpires: !!lifetime.never,
      });
      onDone(res.token);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label className="label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Claude Desktop on my laptop" required />
        </div>
        <div>
          <label className="label">Expires</label>
          <select className="input" value={lifetimeIdx} onChange={(e) => setLifetimeIdx(Number(e.target.value))}>
            {options.map((o, i) => <option key={`${o.days}-${o.never ?? false}`} value={i}>{o.label}</option>)}
          </select>
          {!policy.allowUnlimited && (
            <p className="text-xs text-muted mt-1">
              Never-expiring tokens are turned off by your administrator.
            </p>
          )}
        </div>
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={readOnly} disabled={ownerReadOnly} onChange={(e) => setReadOnly(e.target.checked)} />
        <span className="flex items-center gap-1"><ShieldCheck className="h-4 w-4 text-accent" /> Read-only (no start/stop/deploy)</span>
        {ownerReadOnly && <span className="text-xs text-muted">— your account is read-only</span>}
      </label>

      <div>
        <label className="label">Sections {picked.length === 0 && <span className="text-muted font-normal">— none selected = all your sections</span>}</label>
        <div className="flex flex-wrap gap-2 mt-1">
          {sections.length === 0 && <span className="text-xs text-muted">No grantable sections.</span>}
          {sections.map((s) => (
            <button type="button" key={s} onClick={() => toggle(s)}
              className={`text-xs rounded-md px-2 py-1 border ${picked.includes(s) ? "bg-accent/15 text-accent border-accent/40" : "bg-panel2 text-muted border-border"}`}>
              {sectionLabel(s)}
            </button>
          ))}
        </div>
      </div>

      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onCancel}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Creating…" : "Create token"}</button>
      </div>
    </form>
  );
}

function SecretModal({ secret, onClose }: { secret: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  const copy = async (text: string) => {
    try { await navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 1500); } catch { /* ignore */ }
  };
  const cmd = `claude mcp add --transport http docker-commander ${window.location.origin}/mcp --header "Authorization: Bearer ${secret}"`;

  return (
    <div className="fixed inset-0 bg-black/60 grid place-items-center z-50 p-4" onClick={(e) => { e.stopPropagation(); onClose(); }}>
      <div className="card p-6 max-w-2xl w-full space-y-4" onClick={(e) => e.stopPropagation()}>
        <h2 className="text-base font-semibold flex items-center gap-2"><KeyRound className="h-5 w-5 text-accent" /> Token created</h2>
        <p className="text-sm text-muted">Copy it now — it won't be shown again. Only its hash is stored.</p>

        <div>
          <label className="label">Token</label>
          <div className="flex items-center gap-2">
            <code className="input font-mono text-xs break-all flex-1">{secret}</code>
            <button className="btn-ghost px-2 py-1" onClick={() => copy(secret)} title="Copy">{copied ? <Check className="h-4 w-4 text-ok" /> : <Copy className="h-4 w-4" />}</button>
          </div>
        </div>

        <div>
          <label className="label">Add to Claude Code</label>
          <code className="input font-mono text-xs break-all block whitespace-pre-wrap">{cmd}</code>
          <p className="text-xs text-muted mt-1">For Claude Desktop / Cursor use the OAuth connector with this server's URL instead of a bearer token.</p>
        </div>

        <div className="flex justify-end">
          <button className="btn-primary" onClick={onClose}>Done</button>
        </div>
      </div>
    </div>
  );
}

function fmtDate(s: string): string {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
}
