import { useCallback, useEffect, useState } from "react";
import { Loader2, ShieldCheck } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { PolicyMode, PolicyRuleId } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { Spinner } from "../components/ui";

// Deploy-time policy checks: refuse or warn on a deploy that would create a
// privileged container, break out of the host's network/PID namespace, mount
// the Docker socket, run an unpinned image, or skip resource limits /
// healthchecks. Each rule is independently off/warn/block — see
// internal/docker/policy.go for the evaluation and NEXT.md's "Policy checks
// before deploy" for the design.

const RULE_INFO: Record<PolicyRuleId, { label: string; description: string }> = {
  privileged: { label: "Privileged containers", description: "A privileged container has full access to the host's devices — effectively root on the host." },
  host_network: { label: "Host network mode", description: "Shares the host's network namespace instead of an isolated one." },
  host_pid: { label: "Host PID namespace", description: "Can see and signal every process on the host, not just its own." },
  docker_socket_mount: { label: "Docker socket mount", description: "A container with the Docker socket can control every other container on the host." },
  latest_tag: { label: "Unpinned image (:latest)", description: "No tag or an explicit \"latest\" tag — the exact image running can change silently on the next pull." },
  missing_resource_limits: { label: "Missing resource limits", description: "No CPU or memory limit — a runaway service can starve everything else on the host." },
  missing_healthcheck: { label: "Missing healthcheck", description: "Docker has no way to tell a hung process from a running one." },
};

const MODES: PolicyMode[] = ["off", "warn", "block"];
const MODE_LABEL: Record<PolicyMode, string> = { off: "Off", warn: "Warn", block: "Block" };

export function PolicyRules() {
  const [rules, setRules] = useState<PolicyRuleId[]>([]);
  const [modes, setModes] = useState<Record<string, PolicyMode>>({});
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const load = useCallback(() => {
    api.policyRules().then((r) => {
      setRules(r.rules);
      setModes(r.modes);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);
  useEffect(() => load(), [load]);

  const save = async () => {
    setBusy(true); setMsg(null);
    try {
      await api.setPolicyRules(modes);
      setMsg({ ok: true, text: "Saved." });
    } catch (e) {
      setMsg({ ok: false, text: e instanceof Error ? `Save failed: ${e.message}` : "Save failed" });
    } finally { setBusy(false); }
  };

  if (!loaded) return (<><PageHeader title="Policy rules" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="Policy rules" />
      <div className="p-6 max-w-3xl space-y-4">
        <p className="text-sm text-muted">
          Checked against every project deploy. <b>Off</b> never looks. <b>Warn</b> asks the
          operator to confirm before the deploy runs. <b>Block</b> refuses the deploy outright —
          the only way past it is changing the rule&apos;s mode here.
        </p>
        <div className="card divide-y divide-border">
          {rules.map((rule) => {
            const info = RULE_INFO[rule];
            return (
              <div key={rule} className="p-4 flex items-center justify-between gap-4">
                <div className="min-w-0">
                  <div className="font-medium flex items-center gap-2"><ShieldCheck className="h-4 w-4 text-accent shrink-0" /> {info?.label ?? rule}</div>
                  {info && <p className="text-xs text-muted mt-0.5">{info.description}</p>}
                </div>
                <div className="flex gap-1 shrink-0">
                  {MODES.map((mode) => (
                    <button
                      key={mode}
                      onClick={() => setModes((m) => ({ ...m, [rule]: mode }))}
                      className={clsx(
                        "text-xs px-2.5 py-1 rounded-md border",
                        modes[rule] === mode ? "bg-accent/20 border-accent/40 text-text" : "border-border text-muted",
                      )}
                    >
                      {MODE_LABEL[mode]}
                    </button>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
        <div className="flex items-center justify-end gap-3">
          {msg && <span className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</span>}
          <button className="btn-primary" onClick={save} disabled={busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Save policy rules
          </button>
        </div>
      </div>
    </>
  );
}
