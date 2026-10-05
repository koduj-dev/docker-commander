import type { User } from "../lib/types";

// mustEnrol2FA says whether a signed-in account has to pair a second factor
// before it may use the app: 2FA is enforced on this connection and the account
// has none.
//
// "None" means no factor of ANY kind. It used to check for an authenticator app
// only, so an account whose one factor was a passkey was sent to the
// authenticator enrolment screen at every sign-in, and could not reach the app
// without pairing an app it didn't want.
export function mustEnrol2FA(user: Pick<User, "mfaEnforced" | "mfaEnabled" | "totpEnabled">): boolean {
  // mfaEnabled is the server's "any second factor"; totpEnabled implies it.
  const hasFactor = !!user.mfaEnabled || user.totpEnabled;
  return user.mfaEnforced && !hasFactor;
}
