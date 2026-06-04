// Per-user UI preferences, stored server-side (so they follow the account
// across browsers) and cached in-memory for synchronous reads. The cache is
// loaded once at auth bootstrap before the app renders, so reads don't flash.
import { api } from "./api";

let cache: Record<string, unknown> = {};

// loadPrefs fetches the user's prefs into the cache; call it once after login.
export async function loadPrefs(): Promise<void> {
  try {
    cache = (await api.prefs()) ?? {};
  } catch {
    cache = {};
  }
}

export function clearPrefs(): void {
  cache = {};
}

export function getPref<T>(key: string, fallback: T): T {
  return key in cache ? (cache[key] as T) : fallback;
}

let saveTimer: ReturnType<typeof setTimeout> | undefined;
export function setPref(key: string, value: unknown): void {
  cache[key] = value;
  // Debounce the server write so rapid changes (typing, paging) coalesce.
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    void api.savePrefs(cache).catch(() => {});
  }, 600);
}
