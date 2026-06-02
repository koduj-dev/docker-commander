// Thin typed wrapper over fetch. The session lives in an httpOnly cookie, so we
// just send credentials and never touch the token in JS.

import type {
  AlertEvent,
  AlertRule,
  AppSettings,
  AuditEntry,
  LdapConfig,
  ManagedUser,
  ContainerDetail,
  ContainerSummary,
  CreateSpec,
  DiffEntry,
  FileEntry,
  DiskUsage,
  HistoryEntry,
  Host,
  ImageSummary,
  NetworkSummary,
  ParseRule,
  Registry,
  SmtpConfig,
  SystemInfo,
  VolumeSummary,
  TopResult,
  Topology,
  User,
  Webhook,
} from "./types";
import { getHostId, hostParam } from "./host";

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

// smtpPayload strips read-only/derived fields (hasPassword) the API rejects.
function smtpPayload(c: SmtpConfig & { password?: string }) {
  return {
    host: c.host, port: c.port, username: c.username,
    password: c.password ?? "", from: c.from, to: c.to, tls: c.tls,
  };
}

// ldapPayload strips the read-only hasBindPassword field the API rejects.
function ldapPayload(c: LdapConfig & { bindPassword?: string }) {
  return {
    enabled: c.enabled, url: c.url, startTls: c.startTls, bindDn: c.bindDn,
    bindPassword: c.bindPassword ?? "", userBaseDn: c.userBaseDn,
    userFilter: c.userFilter, adminGroupDn: c.adminGroupDn,
  };
}

// uploadTar POSTs a tar file as a raw body (not JSON) for load/import.
async function uploadTar(path: string, file: File): Promise<{ ok: boolean; error?: string; output?: string }> {
  const res = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/x-tar" },
    body: file,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
  return data;
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

  // User management (admin)
  users: () => req<ManagedUser[]>("GET", "/api/users"),
  createUser: (b: { username: string; password: string; role: string; readOnly: boolean; sections: string[] }) =>
    req<{ ok: boolean; id?: number; error?: string }>("POST", "/api/users", b),
  updateUser: (id: number, b: { role: string; readOnly: boolean; sections: string[] }) =>
    req<{ ok: boolean; error?: string }>("PATCH", `/api/users/${id}`, b),
  resetUserPassword: (id: number, password: string) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/users/${id}/password`, { password }),
  deleteUser: (id: number) => req<{ ok: boolean; error?: string }>("DELETE", `/api/users/${id}`),

  // App settings (admin): feature flags + localhost 2FA
  settings: () => req<AppSettings>("GET", "/api/settings"),
  setSettings: (b: { disabledSections: string[]; localhostNo2fa: boolean }) =>
    req<{ ok: boolean }>("PUT", "/api/settings", b),

  // LDAP / external auth (admin). Send only server-known fields.
  ldap: () => req<LdapConfig>("GET", "/api/ldap"),
  setLdap: (c: LdapConfig & { bindPassword?: string }) => req<{ ok: boolean }>("PUT", "/api/ldap", ldapPayload(c)),
  testLdap: (c: LdapConfig & { bindPassword?: string }) =>
    req<{ ok: boolean; error?: string; entries?: number }>("POST", "/api/ldap/test", ldapPayload(c)),
  totpSetup: () => req<Enrollment>("POST", "/api/auth/totp/setup"),
  totpEnable: (code: string) => req<{ ok: boolean }>("POST", "/api/auth/totp/enable", { code }),

  // Host management
  hosts: () => req<Host[]>("GET", "/api/hosts"),
  createHost: (h: Partial<Host> & { tlsCa?: string; tlsCert?: string; tlsKey?: string }) =>
    req<{ id: number }>("POST", "/api/hosts", h),
  deleteHost: (id: number) => req<{ ok: boolean }>("DELETE", `/api/hosts/${id}`),
  updateHostAlertEmail: (id: number, alertEmail: string) =>
    req<{ ok: boolean }>("PATCH", `/api/hosts/${id}`, { alertEmail }),
  testHost: (id: number) =>
    req<{
      ok: boolean;
      error?: string;
      serverVersion?: string;
      containersRunning?: number;
      untrusted?: boolean; // ssh host key not yet trusted
      mismatch?: boolean; // ssh host key changed (possible MITM)
      fingerprint?: string;
      keyType?: string;
    }>("GET", `/api/hosts/${id}/test`),
  trustHost: (id: number, fingerprint?: string) =>
    req<{ ok: boolean; error?: string; mismatch?: boolean; fingerprint?: string }>(
      "POST",
      `/api/hosts/${id}/trust`,
      { fingerprint }
    ),

  containers: () => req<ContainerSummary[]>("GET", `/api/containers${hostParam()}`),
  container: (id: string) => req<ContainerDetail>("GET", `/api/containers/${id}${hostParam()}`),
  containerAction: (id: string, action: string) =>
    req<{ ok: boolean }>("POST", `/api/containers/${id}/${action}${hostParam()}`),
  containerDiff: (id: string) => req<DiffEntry[]>("GET", `/api/containers/${id}/diff${hostParam()}`),
  containerTop: (id: string) => req<TopResult>("GET", `/api/containers/${id}/top${hostParam()}`),

  // In-container file browser (docker cp).
  listFiles: (id: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<{ ok: boolean; path: string; entries: FileEntry[] | null; error?: string }>("GET", `/api/containers/${id}/files?${params.toString()}`);
  },
  downloadFileUrl: (id: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return `/api/containers/${id}/files/download?${params.toString()}`;
  },
  uploadFile: async (id: string, destDir: string, file: File) => {
    const params = new URLSearchParams({ path: destDir, name: file.name });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    const res = await fetch(`/api/containers/${id}/files/upload?${params.toString()}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/octet-stream" }, body: file,
    });
    const text = await res.text();
    const data = text ? JSON.parse(text) : null;
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { ok: boolean; error?: string; bytes?: number };
  },
  deleteFile: (id: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<{ ok: boolean; error?: string }>("DELETE", `/api/containers/${id}/files?${params.toString()}`);
  },

  createContainer: (spec: CreateSpec) =>
    req<{ ok: boolean; id?: string; error?: string }>("POST", `/api/containers${hostParam()}`, spec),
  renameContainer: (id: string, name: string) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/containers/${id}/rename${hostParam()}`, { name }),
  updateContainer: (id: string, body: { memory: number; nanoCpus: number; restartPolicy: string }) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/containers/${id}/update${hostParam()}`, body),
  commitContainer: (id: string, body: { ref: string; comment: string }) =>
    req<{ ok: boolean; imageId?: string; error?: string }>("POST", `/api/containers/${id}/commit${hostParam()}`, body),

  // Generic raw inspect for any object kind. id/ref travels as a query param.
  inspect: (kind: "container" | "image" | "network" | "volume", id: string) => {
    const params = new URLSearchParams({ id });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<unknown>("GET", `/api/inspect/${kind}?${params.toString()}`);
  },

  diskUsage: () => req<DiskUsage>("GET", `/api/system/df${hostParam()}`),

  images: () => req<ImageSummary[]>("GET", `/api/images${hostParam()}`),
  removeImage: (ref: string, force = false) => {
    const params = new URLSearchParams({ ref });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    if (force) params.set("force", "1");
    return req<{ ok: boolean; error?: string; changed?: string[] }>(
      "DELETE",
      `/api/images?${params.toString()}`
    );
  },
  pruneImages: () => req<{ deleted: string[] | null; spaceReclaimed: number }>("POST", `/api/images/prune${hostParam()}`),
  imageHistory: (ref: string) => {
    const params = new URLSearchParams({ ref });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<HistoryEntry[]>("GET", `/api/images/history?${params.toString()}`);
  },
  tagImage: (source: string, target: string) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/images/tag${hostParam()}`, { source, target }),

  // Image/container transfer. Save/export are downloads (same-origin GET, cookie
  // auth) so we expose URLs the UI hands to an <a download>.
  saveImageUrl: (ref: string) => {
    const p = new URLSearchParams({ ref });
    const h = getHostId();
    if (h != null) p.set("host", String(h));
    return `/api/images/save?${p.toString()}`;
  },
  exportContainerUrl: (id: string) => `/api/containers/${id}/export${hostParam()}`,
  loadImage: (file: File) => uploadTar(`/api/images/load${hostParam()}`, file),
  importImage: (ref: string, file: File) => {
    const p = new URLSearchParams({ ref });
    const h = getHostId();
    if (h != null) p.set("host", String(h));
    return uploadTar(`/api/images/import?${p.toString()}`, file);
  },

  // Registry credentials
  registries: () => req<Registry[]>("GET", "/api/registries"),
  createRegistry: (b: { name: string; address: string; username: string; secret: string }) =>
    req<{ id: number }>("POST", "/api/registries", b),
  deleteRegistry: (id: number) => req<{ ok: boolean }>("DELETE", `/api/registries/${id}`),
  testRegistry: (id: number) => req<{ ok: boolean; error?: string }>("POST", `/api/registries/${id}/test${hostParam()}`),

  networks: () => req<NetworkSummary[]>("GET", `/api/networks${hostParam()}`),
  deleteNetwork: (id: string) => req<{ ok: boolean; error?: string }>("DELETE", `/api/networks/${id}${hostParam()}`),

  volumes: () => req<VolumeSummary[]>("GET", `/api/volumes${hostParam()}`),
  createVolume: (b: { name: string; driver?: string }) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/volumes${hostParam()}`, b),
  deleteVolume: (name: string, force = false) => {
    const params = new URLSearchParams();
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    if (force) params.set("force", "1");
    const qs = params.toString();
    return req<{ ok: boolean; error?: string }>("DELETE", `/api/volumes/${encodeURIComponent(name)}${qs ? `?${qs}` : ""}`);
  },
  pruneVolumes: () => req<{ deleted: string[] | null; spaceReclaimed: number }>("POST", `/api/volumes/prune${hostParam()}`),
  topology: () => req<Topology>("GET", `/api/topology${hostParam()}`),
  system: () => req<SystemInfo>("GET", `/api/system${hostParam()}`),
  audit: () => req<AuditEntry[]>("GET", "/api/audit"),

  // Alerting
  webhooks: () => req<Webhook[]>("GET", "/api/webhooks"),
  createWebhook: (w: Partial<Webhook>) => req<{ id: number }>("POST", "/api/webhooks", w),
  deleteWebhook: (id: number) => req<{ ok: boolean }>("DELETE", `/api/webhooks/${id}`),

  alertRules: () => req<AlertRule[]>("GET", "/api/alert-rules"),
  createAlertRule: (body: {
    name: string;
    enabled: boolean;
    type: string;
    target: string;
    config: unknown;
    severity: string;
    webhookId: number | null;
    email: boolean;
    cooldownSec: number;
  }) => req<{ id: number }>("POST", "/api/alert-rules", body),
  updateAlertRule: (
    id: number,
    body: { name: string; type: string; target: string; config: unknown; severity: string; webhookId: number | null; email: boolean; cooldownSec: number }
  ) => req<{ ok: boolean }>("PUT", `/api/alert-rules/${id}`, body),
  toggleAlertRule: (id: number, enabled: boolean) =>
    req<{ ok: boolean }>("PATCH", `/api/alert-rules/${id}`, { enabled }),
  deleteAlertRule: (id: number) => req<{ ok: boolean }>("DELETE", `/api/alert-rules/${id}`),

  alerts: () => req<{ events: AlertEvent[]; unread: number }>("GET", "/api/alerts"),
  ackAlert: (id: number) => req<{ ok: boolean }>("POST", `/api/alerts/${id}/ack`),

  // Saved log parsing rules (applied client-side in the Logs view)
  parseRules: () => req<ParseRule[]>("GET", "/api/parse-rules"),
  createParseRule: (name: string, pattern: string) => req<{ id: number }>("POST", "/api/parse-rules", { name, pattern }),
  deleteParseRule: (id: number) => req<{ ok: boolean }>("DELETE", `/api/parse-rules/${id}`),

  // Email (SMTP) alert channel. Send only the server-known fields (the API
  // rejects unknown ones like the read-only `hasPassword`).
  smtp: () => req<SmtpConfig>("GET", "/api/smtp"),
  setSmtp: (c: SmtpConfig & { password?: string }) => req<{ ok: boolean }>("PUT", "/api/smtp", smtpPayload(c)),
  testSmtp: (c?: SmtpConfig & { password?: string }) =>
    req<{ ok: boolean; error?: string }>("POST", "/api/smtp/test", c ? smtpPayload(c) : {}),

  metricsHistory: (container: string, metric: string, range: string) =>
    req<{ metric: string; points: { t: number; v: number }[] }>(
      "GET",
      `/api/metrics/history?container=${encodeURIComponent(container)}&metric=${metric}&range=${range}${hostParam("&")}`
    ),
};
