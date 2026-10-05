import { describe, it, expect } from "vitest";
import { mustEnrol2FA } from "./enrolment";

describe("mustEnrol2FA", () => {
  it("sends an account with no second factor to enrolment while 2FA is enforced", () => {
    expect(mustEnrol2FA({ mfaEnforced: true, mfaEnabled: false, totpEnabled: false })).toBe(true);
  });

  it("lets an account whose only factor is a passkey in", () => {
    expect(mustEnrol2FA({ mfaEnforced: true, mfaEnabled: true, totpEnabled: false })).toBe(false);
  });

  it("lets an account with an authenticator app in", () => {
    expect(mustEnrol2FA({ mfaEnforced: true, mfaEnabled: true, totpEnabled: true })).toBe(false);
  });

  it("asks nothing on an exempt connection", () => {
    expect(mustEnrol2FA({ mfaEnforced: false, mfaEnabled: false, totpEnabled: false })).toBe(false);
  });
});
