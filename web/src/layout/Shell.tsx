import { NavLink, useNavigate } from "react-router-dom";
import type { ReactNode } from "react";
import { Boxes, Container, LayoutDashboard, Network, ScrollText, LogOut } from "lucide-react";
import clsx from "clsx";
import { useAuth } from "../auth/AuthContext";

const nav = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard, end: true },
  { to: "/containers", label: "Containers", icon: Boxes },
  { to: "/networks", label: "Networks", icon: Network },
  { to: "/audit", label: "Audit log", icon: ScrollText },
];

export function Shell({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth();
  const navigate = useNavigate();

  return (
    <div className="h-full grid grid-cols-[240px_1fr]">
      <aside className="bg-panel border-r border-border flex flex-col">
        <div className="flex items-center gap-2.5 px-5 h-16 border-b border-border">
          <div className="h-8 w-8 rounded-lg bg-accent grid place-items-center">
            <Container className="h-5 w-5 text-white" />
          </div>
          <div className="font-semibold text-sm">Docker Commander</div>
        </div>
        <nav className="flex-1 p-3 space-y-1">
          {nav.map((n) => (
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
              {n.label}
            </NavLink>
          ))}
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
      <div className="flex items-center gap-2">{actions}</div>
    </div>
  );
}
