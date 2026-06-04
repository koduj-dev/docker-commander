import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api } from "../lib/api";
import { loadPrefs, clearPrefs } from "../lib/prefs";
import type { User } from "../lib/types";

interface AuthState {
  user: User | null;
  loading: boolean;
  needsSetup: boolean;
  refresh: () => Promise<void>;
  setUser: (u: User | null) => void;
  logout: () => Promise<void>;
}

const Ctx = createContext<AuthState>(null as unknown as AuthState);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [needsSetup, setNeedsSetup] = useState(false);
  const [loading, setLoading] = useState(true);

  const refresh = async () => {
    try {
      const status = await api.authStatus();
      setNeedsSetup(status.needsSetup);
      if (!status.needsSetup) {
        try {
          const me = await api.me();
          await loadPrefs(); // populate the prefs cache before the app renders
          setUser(me);
        } catch {
          setUser(null);
        }
      }
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
  }, []);

  const logout = async () => {
    await api.logout();
    clearPrefs();
    setUser(null);
  };

  return (
    <Ctx.Provider value={{ user, loading, needsSetup, refresh, setUser, logout }}>
      {children}
    </Ctx.Provider>
  );
}

export const useAuth = () => useContext(Ctx);
