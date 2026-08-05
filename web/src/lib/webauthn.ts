// The browser half of WebAuthn.
//
// The API speaks ArrayBuffers and the wire speaks base64url, so most of this file
// is that translation. It is worth doing carefully: a mis-decoded challenge does
// not fail loudly, it produces a signature over the wrong bytes and a rejection
// that looks like "your key is wrong".

/** Whether this browser can do WebAuthn at all. */
export function passkeysSupported(): boolean {
  return typeof window !== "undefined"
    && typeof window.PublicKeyCredential !== "undefined"
    && typeof navigator?.credentials?.create === "function";
}

function fromBase64url(value: string): Uint8Array {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

function toBase64url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** The server's credential-creation options, as they arrive over JSON. */
export interface CreationOptions {
  publicKey: {
    challenge: string;
    user: { id: string; name: string; displayName: string };
    excludeCredentials?: Array<{ id: string; type: string; transports?: string[] }>;
    [key: string]: unknown;
  };
}

/** The server's assertion options, as they arrive over JSON. */
export interface RequestOptions {
  publicKey: {
    challenge: string;
    allowCredentials?: Array<{ id: string; type: string; transports?: string[] }>;
    [key: string]: unknown;
  };
}

/**
 * createPasskey runs navigator.credentials.create and returns the JSON the server
 * expects back.
 */
export async function createPasskey(options: CreationOptions): Promise<unknown> {
  const publicKey = {
    ...options.publicKey,
    challenge: fromBase64url(options.publicKey.challenge),
    user: {
      ...options.publicKey.user,
      id: fromBase64url(options.publicKey.user.id),
    },
    excludeCredentials: (options.publicKey.excludeCredentials ?? []).map((c) => ({
      ...c,
      id: fromBase64url(c.id),
    })),
    // The server sends the rest (rp, pubKeyCredParams, timeout, …) verbatim and the
    // browser validates it, so the cast is over the fields decoded above rather
    // than a claim that this file knows the whole shape.
  } as unknown as PublicKeyCredentialCreationOptions;

  const credential = await navigator.credentials.create({ publicKey });
  if (!credential) throw new Error("no passkey was created");
  const c = credential as PublicKeyCredential;
  const response = c.response as AuthenticatorAttestationResponse;
  return {
    id: c.id,
    rawId: toBase64url(c.rawId),
    type: c.type,
    response: {
      attestationObject: toBase64url(response.attestationObject),
      clientDataJSON: toBase64url(response.clientDataJSON),
    },
  };
}

/**
 * usePasskey runs navigator.credentials.get and returns the JSON the server
 * expects back.
 */
export async function usePasskey(options: RequestOptions): Promise<unknown> {
  const publicKey = {
    ...options.publicKey,
    challenge: fromBase64url(options.publicKey.challenge),
    allowCredentials: (options.publicKey.allowCredentials ?? []).map((c) => ({
      ...c,
      id: fromBase64url(c.id),
    })),
  } as unknown as PublicKeyCredentialRequestOptions;

  const credential = await navigator.credentials.get({ publicKey });
  if (!credential) throw new Error("no passkey was used");
  const c = credential as PublicKeyCredential;
  const response = c.response as AuthenticatorAssertionResponse;
  return {
    id: c.id,
    rawId: toBase64url(c.rawId),
    type: c.type,
    response: {
      authenticatorData: toBase64url(response.authenticatorData),
      clientDataJSON: toBase64url(response.clientDataJSON),
      signature: toBase64url(response.signature),
      userHandle: response.userHandle ? toBase64url(response.userHandle) : "",
    },
  };
}

/**
 * describePasskeyError turns a WebAuthn exception into something worth reading.
 *
 * The browser's own messages are written for developers ("The operation either
 * timed out or was not allowed"), and the most common case — the user closing the
 * prompt — is not an error worth shouting about.
 */
export function describePasskeyError(e: unknown): string {
  if (e instanceof DOMException) {
    switch (e.name) {
      case "NotAllowedError":
        return "The passkey prompt was dismissed or timed out.";
      case "InvalidStateError":
        return "This device already has a passkey for this account.";
      case "SecurityError":
        return "The browser refused: passkeys need HTTPS (or localhost).";
      case "NotSupportedError":
        return "This browser or device cannot create that kind of passkey.";
    }
  }
  return e instanceof Error ? e.message : "the passkey could not be used";
}
