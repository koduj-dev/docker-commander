import { NavLink, useNavigate } from "react-router-dom";
import { useEffect, useState, type ReactNode } from "react";
import { Activity, Bell, Boxes, Container, Database, KeyRound, Layers, LayoutDashboard, Network, ScrollText, Server, Settings, Share2, Terminal, Users, LogOut } from "lucide-react";
import clsx from "clsx";
import { useAuth } from "../auth/AuthContext";
import { api } from "../lib/api";
import type { Host } from "../lib/types";
import { getHostId, setHostId } from "../lib/host";

// Navigation grouped into sections so the sidebar stays scannable as the
// feature set grows.
type NavItem = { to: string; label: string; icon: typeof Boxes; end?: boolean; section?: string; adminOnly?: boolean };
const navGroups: { title: string; items: NavItem[] }[] = [
  {
    title: "",
    items: [{ to: "/", label: "Dashboard", icon: LayoutDashboard, end: true, section: "dashboard" }],
  },
  {
    title: "Compute",
    items: [
      { to: "/containers", label: "Containers", icon: Boxes, section: "containers" },
      { to: "/images", label: "Images", icon: Layers, section: "images" },
      { to: "/volumes", label: "Volumes", icon: Database, section: "volumes" },
    ],
  },
  {
    title: "Network",
    items: [
      { to: "/networks", label: "Networks", icon: Network, section: "networks" },
      { to: "/topology", label: "Topology", icon: Share2, section: "topology" },
    ],
  },
  {
    title: "Observability",
    items: [
      { to: "/logs", label: "Logs", icon: Terminal, section: "logs" },
      { to: "/events", label: "Events", icon: Activity, section: "events" },
      { to: "/alerts", label: "Alerts", icon: Bell, section: "alerts" },
    ],
  },
  {
    title: "System",
    items: [
      { to: "/hosts", label: "Hosts", icon: Server, section: "hosts" },
      { to: "/registries", label: "Registries", icon: KeyRound, section: "registries" },
      { to: "/audit", label: "Audit log", icon: ScrollText, section: "audit" },
      { to: "/users", label: "Users", icon: Users, adminOnly: true },
      { to: "/settings", label: "Settings", icon: Settings, adminOnly: true },
    ],
  },
];

// loadHosts memoises the host list so the switcher and the per-page badge don't
// each refetch it. A host change reloads the page, which clears this cache.
let hostsPromise: Promise<Host[]> | null = null;
function loadHosts(): Promise<Host[]> {
  if (!hostsPromise) hostsPromise = api.hosts().catch(() => [] as Host[]);
  return hostsPromise;
}

// activeHostId resolves the currently selected host: the explicit choice, else
// the local host, else the first one.
function activeHostId(hosts: Host[]): number | undefined {
  return getHostId() ?? hosts.find((h) => h.kind === "local")?.id ?? hosts[0]?.id;
}

// HostSwitcher selects the active Docker host. Changing it reloads the app so
// every view and WebSocket re-binds to the new host cleanly. Hidden when there
// is only one host (nothing to switch).
function HostSwitcher() {
  const [hosts, setHosts] = useState<Host[]>([]);
  useEffect(() => {
    loadHosts().then(setHosts);
  }, []);
  if (hosts.length <= 1) return null;
  const current = activeHostId(hosts);
  return (
    <div className="px-3 py-3 border-b border-border">
      <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wide text-muted mb-1.5 px-0.5">
        <Server className="h-3.5 w-3.5 text-accent" /> Viewing host
      </div>
      <select
        className="input py-2 font-medium border-accent/40 bg-accent/5 focus:ring-accent"
        value={current}
        onChange={(e) => {
          setHostId(Number(e.target.value));
          window.location.reload();
        }}
      >
        {hosts.map((h) => (
          <option key={h.id} value={h.id}>{h.name} ({h.kind})</option>
        ))}
      </select>
    </div>
  );
}

// ActiveHostBadge shows which host the current page is bound to. Rendered in
// every page header (only when more than one host exists) so the active host is
// always visible, not just in the sidebar switcher.
function ActiveHostBadge() {
  const [hosts, setHosts] = useState<Host[]>([]);
  useEffect(() => {
    loadHosts().then(setHosts);
  }, []);
  if (hosts.length <= 1) return null;
  const h = hosts.find((x) => x.id === activeHostId(hosts));
  if (!h) return null;
  return (
    <span
      className="inline-flex items-center gap-1.5 text-xs rounded-md border border-border bg-panel2 px-2 py-1 text-muted"
      title="Active host — switch it in the sidebar"
    >
      <Server className="h-3.5 w-3.5 text-accent" />
      <span className="font-medium text-text">{h.name}</span>
    </span>
  );
}

// VersionTag shows the running Docker Commander build version in the sidebar
// footer, so it's clear which version is deployed.
function VersionTag() {
  const [version, setVersion] = useState("");
  useEffect(() => {
    api.version().then((v) => setVersion(v.version)).catch(() => {});
  }, []);
  if (!version) return null;
  return <div className="px-2 pt-1 text-[10px] text-muted">Docker Commander {version}</div>;
}

export function Shell({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const [unread, setUnread] = useState(0);

  // Poll the unread alert count to badge the Alerts nav item.
  useEffect(() => {
    const load = () => api.alerts().then((r) => setUnread(r.unread)).catch(() => {});
    load();
    const t = setInterval(load, 8000);
    return () => clearInterval(t);
  }, []);

  return (
    <div className="h-full grid grid-cols-[240px_1fr]">
      <aside className="bg-panel border-r border-border flex flex-col">
        <div className="flex items-center gap-2.5 px-5 h-16 border-b border-border">
          <div className="h-8 w-8 rounded-lg bg-accent grid place-items-center">
            <Container className="h-5 w-5 text-white" />
          </div>
          <div className="font-semibold text-sm">Docker Commander</div>
        </div>
        <HostSwitcher />
        <nav className="flex-1 p-3 space-y-3 overflow-y-auto">
          {navGroups.map((group, gi) => {
            // Filter items by the user's accessible sections + admin-only flag.
            const isAdmin = user?.role === "admin";
            const allowed = new Set(user?.sections ?? []);
            const items = group.items.filter((n) => (n.adminOnly ? isAdmin : !n.section || allowed.has(n.section)));
            if (items.length === 0) return null;
            return (
            <div key={gi} className="space-y-1">
              {group.title && (
                <div className="px-3 pt-2 pb-0.5 text-[10px] uppercase tracking-wider text-muted/60 font-semibold">{group.title}</div>
              )}
              {items.map((n) => (
                <NavLink
                  key={n.to}
                  to={n.to}
                  end={n.end}
                  className={({ isActive }) =>
                    clsx(
                      "flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors",
                      isActive ? "bg-accent/15 text-accent" : "text-muted hover:bg-panel2 hover:text-text"
                    )
                  }
                >
                  <n.icon className="h-4 w-4" />
                  <span className="flex-1">{n.label}</span>
                  {n.to === "/alerts" && unread > 0 && (
                    <span className="text-[10px] font-semibold bg-danger text-white rounded-full px-1.5 py-0.5 min-w-[18px] text-center">
                      {unread > 99 ? "99+" : unread}
                    </span>
                  )}
                </NavLink>
              ))}
            </div>
            );
          })}
        </nav>
        <div className="p-3 border-t border-border">
          <div className="flex items-center justify-between px-2 py-1.5">
            <div className="min-w-0">
              <div className="text-sm font-medium truncate">{user?.username}</div>
              <div className="text-xs text-muted">{user?.role}</div>
            </div>
            <button
              className="btn-ghost px-2 py-2"
              title="Sign out"
              onClick={async () => {
                await logout();
                navigate("/");
              }}
            >
              <LogOut className="h-4 w-4" />
            </button>
          </div>
          <VersionTag />
        </div>
      </aside>
      <main className="overflow-auto">{children}</main>
    </div>
  );
}

export function PageHeader({ title, actions }: { title: string; actions?: ReactNode }) {
  return (
    <div className="flex items-center justify-between h-16 px-6 border-b border-border sticky top-0 bg-bg/80 backdrop-blur z-10">
      <h1 className="text-lg font-semibold">{title}</h1>
      <div className="flex items-center gap-2"><ActiveHostBadge />{actions}</div>
    </div>
  );
}
