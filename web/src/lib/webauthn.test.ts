import { describe, it, expect, vi, afterEach } from "vitest";
import { createPasskey, describePasskeyError, passkeysSupported } from "./webauthn";

// The encoding is where this goes wrong silently: a mis-decoded challenge does not
// throw, it produces a signature over the wrong bytes and a rejection that reads
// like "your key is broken". These tests pin the round trip and the error copy.

// bytes the browser was handed, for whatever createPasskey last decoded.
function stubAuthenticator(): { seen: () => PublicKeyCredentialCreationOptions } {
  let captured: PublicKeyCredentialCreationOptions;
  const create = vi.fn(async (opts: CredentialCreationOptions) => {
    captured = opts.publicKey as PublicKeyCredentialCreationOptions;
    // Echo the challenge straight back as the credential id, so what comes out of
    // the encoder is exactly what went into the decoder.
    const echoed = new Uint8Array(captured.challenge as ArrayBuffer);
    return {
      id: "ignored",
      type: "public-key",
      rawId: echoed.buffer.slice(echoed.byteOffset, echoed.byteOffset + echoed.byteLength),
      response: { attestationObject: new Uint8Array([1]).buffer, clientDataJSON: new Uint8Array([2]).buffer },
    };
  });
  Object.defineProperty(navigator, "credentials", { value: { create }, configurable: true });
  return { seen: () => captured };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("base64url", () => {
  // Vectors chosen for the two things that break: the URL-safe alphabet (- and _
  // where base64 has + and /) and the stripped padding. A decoder that forgets
  // either produces the wrong bytes without complaining.
  const vectors: Array<[string, number[]]> = [
    ["AAEC", [0x00, 0x01, 0x02]],
    ["____", [0xff, 0xff, 0xff]],
    ["_-8", [0xff, 0xef]], // three chars: one byte of padding was stripped
    ["-_-_", [0xfb, 0xff, 0xbf]],
  ];

  it.each(vectors)("decodes %s to the right bytes", async (encoded, bytes) => {
    const stub = stubAuthenticator();
    await createPasskey({
      publicKey: { challenge: encoded, user: { id: "AAEC", name: "a", displayName: "a" } },
    });
    expect(Array.from(new Uint8Array(stub.seen().challenge as ArrayBuffer))).toEqual(bytes);
  });

  it.each(vectors)("round-trips %s back to the same string", async (encoded) => {
    stubAuthenticator();
    const out = (await createPasskey({
      publicKey: { challenge: encoded, user: { id: "AAEC", name: "a", displayName: "a" } },
    })) as { rawId: string };
    expect(out.rawId).toBe(encoded);
  });

  it("decodes the user handle too, not just the challenge", async () => {
    const stub = stubAuthenticator();
    await createPasskey({
      publicKey: { challenge: "AAEC", user: { id: "____", name: "a", displayName: "a" } },
    });
    expect(Array.from(new Uint8Array(stub.seen().user.id as ArrayBuffer))).toEqual([0xff, 0xff, 0xff]);
  });
});

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
