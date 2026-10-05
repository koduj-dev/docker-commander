import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { MyAccess, User } from "../lib/types";
import { useAuth } from "./AuthContext";

// canWrite says whether the signed-in account has write access to a section
// anywhere. The server is the gate, with the host as well; this only decides
// whether to offer a control that would otherwise end in a refusal.
//
// Used for raw downloads (export, file and folder downloads, image save): they
// need write, and a download link the server refuses opens a bare 403 page
// instead of a file. Unknown access counts as no: the control appears once it is
// known, rather than appearing and failing.
export function canWrite(user: Pick<User, "role" | "readOnly"> | null, access: MyAccess | null, section: string): boolean {
  if (!user) return false;
  if (user.role === "admin") return true;
  if (user.readOnly || !access || access.readOnly) return false;
  return (access.effective ?? []).some((g) => g.section === section && g.write);
}

// One fetch per account: every download button on a page asks.
let cache: { userId: number; access: Promise<MyAccess> } | null = null;

// resetAccessCache forgets the account's access, at logout (clearUserState).
export function resetAccessCache(): void {
  cache = null;
}

export function useCanWrite(section: string): boolean {
  const { user } = useAuth();
  const [access, setAccess] = useState<MyAccess | null>(null);
  const userId = user?.id;
  const admin = user?.role === "admin";

  useEffect(() => {
    if (userId == null || admin) return;
    if (!cache || cache.userId !== userId) {
      const access = api.myAccess();
      cache = { userId, access };
      // A failed fetch is not cached, or one network blip would hide the
      // buttons until the next sign-in.
      access.catch(() => {
        if (cache?.access === access) cache = null;
      });
    }
    let live = true;
    cache.access.then((a) => live && setAccess(a)).catch(() => {});
    return () => {
      live = false;
    };
  }, [userId, admin]);

  return canWrite(user, access, section);
}
