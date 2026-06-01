import type { ReactNode } from "react";
import { Container } from "lucide-react";

// Centered card layout shared by setup / login / 2FA screens.
export function AuthShell({ title, subtitle, children }: { title: string; subtitle?: string; children: ReactNode }) {
  return (
    <div className="min-h-full grid place-items-center p-4">
      <div className="w-full max-w-sm">
        <div className="flex items-center gap-3 mb-6 justify-center">
          <div className="h-10 w-10 rounded-xl bg-accent grid place-items-center">
            <Container className="h-6 w-6 text-white" />
          </div>
          <div className="text-lg font-semibold">Docker Commander</div>
        </div>
        <div className="card p-6">
          <h1 className="text-base font-semibold">{title}</h1>
          {subtitle && <p className="text-sm text-muted mt-1 mb-4">{subtitle}</p>}
          <div className={subtitle ? "" : "mt-4"}>{children}</div>
        </div>
      </div>
    </div>
  );
}
