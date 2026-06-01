// Thin typed wrapper over fetch. The session lives in an httpOnly cookie, so we
// just send credentials and never touch the token in JS.

import type {
  AuditEntry,
  ContainerDetail,
  ContainerSummary,
  NetworkSummary,
  SystemInfo,
  User,
} from "./types";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) {
    throw new ApiError(res.status, data?.error ?? res.statusText);
  }
  return data as T;
}

export interface Enrollment {
  secret: string;
  otpauthUrl: string;
  qrDataUri: string;
}

export interface LoginResult {
  mfaRequired?: boolean;
  mfaToken?: string;
  user?: User;
  expiresAt?: string;
}

export const api = {
  authStatus: () => req<{ needsSetup: boolean }>("GET", "/api/auth/status"),
  setup: (username: string, password: string) =>
    req<LoginResult>("POST", "/api/auth/setup", { username, password }),
  login: (username: string, password: string) =>
    req<LoginResult>("POST", "/api/auth/login", { username, password }),
  verify2fa: (mfaToken: string, code: string) =>
    req<LoginResult>("POST", "/api/auth/2fa", { mfaToken, code }),
  me: () => req<User>("GET", "/api/auth/me"),
  logout: () => req<{ ok: boolean }>("POST", "/api/auth/logout"),
  totpSetup: () => req<Enrollment>("POST", "/api/auth/totp/setup"),
  totpEnable: (code: string) => req<{ ok: boolean }>("POST", "/api/auth/totp/enable", { code }),

  containers: () => req<ContainerSummary[]>("GET", "/api/containers"),
  container: (id: string) => req<ContainerDetail>("GET", `/api/containers/${id}`),
  containerAction: (id: string, action: string) =>
    req<{ ok: boolean }>("POST", `/api/containers/${id}/${action}`),

  networks: () => req<NetworkSummary[]>("GET", "/api/networks"),
  system: () => req<SystemInfo>("GET", "/api/system"),
  audit: () => req<AuditEntry[]>("GET", "/api/audit"),
};
