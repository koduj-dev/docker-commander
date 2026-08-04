import { clearPrefs } from "./prefs";
import { resetAlertStream } from "./alertStream";

// Everything the browser remembers about the person who was signed in.
//
// It lives in one function because the failure mode is forgetting one of them:
// alert state is module-level and a logout is an SPA navigation, not a reload, so
// whatever isn't cleared here is inherited by the next person to sign in on this
// browser — their unread badge, and their pending alerts replayed as toasts,
// naming containers on hosts the new user may not even be scoped to.
//
// Add to this function rather than to the logout handler, so the next thing that
// caches per-user data is cleared by construction.
export function clearUserState(): void {
  clearPrefs();
  resetAlertStream();
}
