import { describe, it, expect } from "vitest";
import { describePasskeyError, passkeysSupported } from "./webauthn";

// The encoding is where this goes wrong silently: a mis-decoded challenge does not
// throw, it produces a signature over the wrong bytes and a rejection that reads
// like "your key is broken". These tests pin the round trip and the error copy.

describe("passkeysSupported", () => {
  it("is false where the API does not exist", () => {
    // happy-dom has no PublicKeyCredential, which is exactly the "old browser" case.
    expect(passkeysSupported()).toBe(false);
  });
});

describe("describePasskeyError", () => {
  it("translates the browser's own names into something worth reading", () => {
    expect(describePasskeyError(new DOMException("x", "NotAllowedError"))).toMatch(/dismissed or timed out/);
    expect(describePasskeyError(new DOMException("x", "InvalidStateError"))).toMatch(/already has a passkey/);
    expect(describePasskeyError(new DOMException("x", "SecurityError"))).toMatch(/HTTPS/);
  });

  it("passes anything else through rather than inventing a cause", () => {
    expect(describePasskeyError(new Error("network down"))).toBe("network down");
    expect(describePasskeyError("weird")).toMatch(/could not be used/);
  });
});
