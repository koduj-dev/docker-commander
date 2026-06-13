import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth/AuthContext";
import { Spinner } from "./components/ui";
import { Shell } from "./layout/Shell";
import { Setup } from "./pages/Setup";
import { Login } from "./pages/Login";
import { Enroll2FA } from "./pages/Enroll2FA";
import { Dashboard } from "./pages/Dashboard";
import { Containers } from "./pages/Containers";
import { ContainerDetail } from "./pages/ContainerDetail";
import { Stacks } from "./pages/Stacks";
import { Projects } from "./pages/Projects";
import { Templates } from "./pages/Templates";
import { Images } from "./pages/Images";
import { Volumes } from "./pages/Volumes";
import { Networks } from "./pages/Networks";
import { Topology } from "./pages/Topology";
import { Logs } from "./pages/Logs";
import { Events } from "./pages/Events";
import { Alerts } from "./pages/Alerts";
import { Hosts } from "./pages/Hosts";
import { Registries } from "./pages/Registries";
import { MCPTokens } from "./pages/MCPTokens";
import { Users } from "./pages/Users";
import { Settings } from "./pages/Settings";
import { Audit } from "./pages/Audit";

export default function App() {
  const { user, loading, needsSetup } = useAuth();

  if (loading) {
    return (
      <div className="h-full grid place-items-center">
        <Spinner className="h-8 w-8" />
      </div>
    );
  }

  // Unauthenticated flows.
  if (needsSetup) return <Setup />;
  if (!user) return <Login />;

  // 2FA enrollment gate — enforced unless this connection is exempt (the admin
  // allowed password-only login from localhost).
  if (user.mfaEnforced && !user.totpEnabled) return <Enroll2FA />;

  return (
    <Shell>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/containers" element={<Containers />} />
        <Route path="/containers/:id" element={<ContainerDetail />} />
        <Route path="/stacks" element={<Stacks />} />
        <Route path="/projects" element={<Projects />} />
        <Route path="/templates" element={<Templates />} />
        <Route path="/images" element={<Images />} />
        <Route path="/volumes" element={<Volumes />} />
        <Route path="/networks" element={<Networks />} />
        <Route path="/topology" element={<Topology />} />
        <Route path="/logs" element={<Logs />} />
        <Route path="/events" element={<Events />} />
        <Route path="/alerts" element={<Alerts />} />
        <Route path="/hosts" element={<Hosts />} />
        <Route path="/registries" element={<Registries />} />
        <Route path="/mcp-tokens" element={<MCPTokens />} />
        <Route path="/users" element={<Users />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  );
}
