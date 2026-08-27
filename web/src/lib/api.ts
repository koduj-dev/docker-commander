// Thin typed wrapper over fetch. The session lives in an httpOnly cookie, so we
// just send credentials and never touch the token in JS.

import type {
  Session,
  AlertEvent,
  AlertRule,
  AppSettings,
  UpdateStatus,
  ComposeModel,
  AuditEntry,
  DiagnosticsReport,
  LdapConfig,
  ManagedUser,
  ContainerDetail,
  ContainerSummary,
  BulkActionResponse,
  CreateSpec,
  DiffEntry,
  FileEntry,
  DiskUsage,
  HistoryEntry,
  ScanResponse,
  IgnoredCVE,
  Host,
  ImageSummary,
  ImageSearchResult,
  NetworkSummary,
  HostPortProbe,
  ParseRule,
  PortProbe,
  MyAccess,
  Registry,
  Role,
  RoleSection,
  MCPToken,
  MCPStatus,
  MCPTokenPolicy,
  AdminMCPToken,
  AdminOAuthClient,
  Project,
  ProjectTemplateMeta,
  ProjectTemplateDetail,
  ServiceBlockMeta,
  ServiceBlockDetail,
  ComposeFragmentMeta,
  ComposeFragmentDetail,
  TemplateFile,
  TemplateRef,
  FileApi,
  ProjectFile,
  DeployPreview,
  ResourceOverview,
  SmtpConfig,
  Stack,
  SystemInfo,
  VolumeSummary,
  TopResult,
  Topology,
  User,
  Webhook,
  NetworkStats,
  AuthFactor,
} from "./types";
import { getHostId, hostParam } from "./host";
import type { CreationOptions, RequestOptions } from "./webauthn";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function req<T>(
  method: string,
  path: string,
  body?: unknown,
  extraHeaders?: Record<string, string>,
): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: {
      ...(body ? { "Content-Type": "application/json" } : {}),
      ...(extraHeaders ?? {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  const data = parseJSON(text);
  if (!res.ok) {
    throw new ApiError(res.status, data?.error ?? res.statusText);
  }
  return data as T;
}

// parseJSON tolerates a body that isn't JSON.
//
// Parsing before checking res.ok meant an error page from something other than
// this app — a reverse proxy's 502/504 HTML, a plain-text gateway message — threw
// SyntaxError instead of the ApiError every caller expects. The UI then showed
// "Unexpected token '<'" rather than the status, and code branching on
// `e instanceof ApiError` (the login screen, for one) took the wrong path.
// A body we cannot read is not an excuse to lose the status that came with it.
// Returns `any`, mirroring JSON.parse itself: call sites already narrow the shape
// they expect, and a stricter type here would only move the cast around.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function parseJSON(text: string): any {
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
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
    groupMappings: c.groupMappings ?? [],
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
  const data = parseJSON(text);
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
  /** Which second factors this account HAS: "totp", "passkey". */
  methods?: string[];
  /** Whether a passkey ceremony is possible on this connection. */
  passkeyReady?: boolean;
  /** Why it is not, when the account has a passkey but the connection cannot use it. */
  passkeyReason?: string;
  user?: User;
  expiresAt?: string;
}

// Filters for the alert feed. Declared as a named type rather than inline:
// referring to it as Parameters<typeof api.alerts> from inside the same object
// literal is circular, and TypeScript quietly degrades the whole `api` object to
// any — which shows up as dozens of implicit-any errors in unrelated files.
export interface AlertListParams {
  severity?: string;
  kind?: string;
  host?: number;
  container?: string;
  rule?: string;
  q?: string;
  unacked?: boolean;
  sort?: string;
  desc?: boolean;
  limit?: number;
  offset?: number;
}

export const api = {
  authStatus: () => req<{ needsSetup: boolean }>("GET", "/api/auth/status"),
  setup: (username: string, password: string, enable2fa: boolean) =>
    req<LoginResult>("POST", "/api/auth/setup", { username, password, enable2fa }),
  login: (username: string, password: string) =>
    req<LoginResult>("POST", "/api/auth/login", { username, password }),
  verify2fa: (mfaToken: string, code: string) =>
    req<LoginResult>("POST", "/api/auth/2fa", { mfaToken, code }),
  me: () => req<User>("GET", "/api/auth/me"),
  logout: () => req<{ ok: boolean }>("POST", "/api/auth/logout"),
  // Own alert address. Self-service: it only ever affects this account's alerts.
  setMyEmail: (email: string) => req<{ ok: boolean }>("PUT", "/api/auth/me/email", { email }),
  // The caller's own roles and resulting grants, for the profile page.
  myAccess: () => req<MyAccess>("GET", "/api/auth/me/access"),

  // User management (admin)
  users: () => req<ManagedUser[]>("GET", "/api/users"),
  createUser: (b: { username: string; password: string; role: string; readOnly: boolean; sections: string[]; roleIds?: number[] }) =>
    req<{ ok: boolean; id?: number; error?: string; warning?: string }>("POST", "/api/users", b),
  // roleIds is sent explicitly (possibly empty) so the server replaces the
  // assignments; omitting it leaves them untouched.
  updateUser: (id: number, b: { role: string; readOnly: boolean; sections: string[]; roleIds?: number[] }) =>
    req<{ ok: boolean; error?: string }>("PATCH", `/api/users/${id}`, b),

  // Named roles (admin). Built-ins are immutable — duplicate to customise.
  roles: () => req<Role[]>("GET", "/api/roles"),
  createRole: (b: { name: string; description: string; sections: RoleSection[]; hostIds: number[] }) =>
    req<{ ok: boolean; id?: number }>("POST", "/api/roles", b),
  updateRole: (id: number, b: { name: string; description: string; sections: RoleSection[]; hostIds: number[] }) =>
    req<{ ok: boolean }>("PUT", `/api/roles/${id}`, b),
  deleteRole: (id: number) => req<{ ok: boolean }>("DELETE", `/api/roles/${id}`),
  duplicateRole: (id: number) =>
    req<{ ok: boolean; id: number; name: string }>("POST", `/api/roles/${id}/duplicate`),
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
  // password is required only when re-pairing (an authenticator already
  // works); a first enrolment sends none.
  sessions: () => req<Session[]>("GET", "/api/auth/sessions"),
  revokeSession: (id: string) => req<{ ok: boolean }>("DELETE", `/api/auth/sessions/${encodeURIComponent(id)}`),
  revokeOtherSessions: () => req<{ revoked: number }>("POST", "/api/auth/sessions/revoke-others"),
  totpSetup: (password?: string) => req<Enrollment>("POST", "/api/auth/totp/setup", password === undefined ? undefined : { password }),
  totpEnable: (code: string, name?: string) => req<{ ok: boolean }>("POST", "/api/auth/totp/enable", { code, name: name ?? "" }),
  factors: () => req<AuthFactor[]>("GET", "/api/auth/factors"),
  // Passkeys. The credential JSON is the whole body of the "finish" calls, because
  // that is what the WebAuthn library on the server reads — so the name and the
  // challenge token travel in the query string rather than sharing the body.
  // Turning a passkey into a whole login rather than a second factor. Needs the
  // password: it changes what it takes to sign in as you.
  setPasswordless: (enabled: boolean, password: string) =>
    req<{ enabled: boolean }>("PUT", "/api/auth/passwordless", { enabled, password }),
  passkeySupport: () => req<{ available: boolean; reason: string }>("GET", "/api/auth/webauthn/support"),
  passkeyRegisterBegin: (password?: string) =>
    req<CreationOptions>("POST", "/api/auth/webauthn/register/begin", password === undefined ? undefined : { password }),
  passkeyRegisterFinish: (name: string, credential: unknown) =>
    req<{ ok: boolean }>("POST", `/api/auth/webauthn/register/finish?name=${encodeURIComponent(name)}`, credential),
  // Signing in with a passkey alone. No username and no password: the browser
  // finds a discoverable credential for this site and the assertion is the claim.
  passwordlessBegin: () =>
    req<{ ceremonyId: string; publicKey: RequestOptions["publicKey"] }>("POST", "/api/auth/passkey/begin"),
  passwordlessFinish: (ceremonyId: string, credential: unknown) =>
    req<LoginResult>("POST", "/api/auth/passkey/finish", credential, { "X-Passkey-Ceremony": ceremonyId }),
  passkeyLoginBegin: (mfaToken: string) =>
    req<RequestOptions>("POST", "/api/auth/2fa/webauthn/begin", { mfaToken }),
  // The body is the credential, which the server's WebAuthn library parses itself,
  // so the challenge token rides in a header — not the URL, which gets logged.
  passkeyLoginFinish: (mfaToken: string, credential: unknown) =>
    req<LoginResult>("POST", "/api/auth/2fa/webauthn/finish", credential, { "X-MFA-Token": mfaToken }),
  // The password travels in the body, not the path: removing a factor is a
  // step-up, and a URL is the one part of a request that gets logged everywhere.
  removeFactor: (id: number, password: string) =>
    req<{ ok: boolean }>("DELETE", `/api/auth/factors/${id}`, { password }),

  // Host management
  hosts: () => req<Host[]>("GET", "/api/hosts"),
  createHost: (h: Partial<Host> & { tlsCa?: string; tlsCert?: string; tlsKey?: string }) =>
    req<{ id: number }>("POST", "/api/hosts", h),
  deleteHost: (id: number) => req<{ ok: boolean }>("DELETE", `/api/hosts/${id}`),
  updateHostAlertEmail: (id: number, alertEmail: string) =>
    req<{ ok: boolean }>("PATCH", `/api/hosts/${id}`, { alertEmail }),
  setHostDisabled: (id: number, disabled: boolean) =>
    req<{ ok: boolean }>("PATCH", `/api/hosts/${id}`, { disabled }),
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
  // restart/stop/start only — see internal/api/docker_handlers.go's bulkContainerActions.
  bulkContainerAction: (ids: string[], action: "restart" | "stop" | "start") =>
    req<BulkActionResponse>("POST", `/api/containers/bulk-action${hostParam()}`, { ids, action }),
  // Returns the WebSocket URL for handleBulkPullImages — the caller opens it
  // directly (see PullPanel in Images.tsx for the same pattern with a single
  // ref) and sends {ids} as its first message once open. Ids travel in that
  // first message, not the URL: up to 200 full container ids would not
  // reliably fit in the request line/headers many reverse proxies cap.
  bulkPullImagesUrl: () => {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    return `${proto}://${location.host}/api/containers/bulk-pull${hostParam("?")}`;
  },
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
    const data = parseJSON(text);
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { ok: boolean; error?: string; bytes?: number };
  },
  deleteFile: (id: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<{ ok: boolean; error?: string }>("DELETE", `/api/containers/${id}/files?${params.toString()}`);
  },
  mkdirFile: (id: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<{ ok: boolean; error?: string }>("POST", `/api/containers/${id}/files/mkdir?${params.toString()}`);
  },
  extractFile: async (id: string, destDir: string, file: File) => {
    const params = new URLSearchParams({ path: destDir, name: file.name });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    const res = await fetch(`/api/containers/${id}/files/extract?${params.toString()}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/octet-stream" }, body: file,
    });
    const text = await res.text();
    const data = parseJSON(text);
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { ok: boolean; error?: string };
  },

  // Volume file browser (a throwaway helper container mounts the volume).
  listVolumeFiles: (name: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<{ ok: boolean; path: string; entries: FileEntry[] | null; error?: string }>("GET", `/api/volumes/${encodeURIComponent(name)}/files?${params.toString()}`);
  },
  volumeFileDownloadUrl: (name: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return `/api/volumes/${encodeURIComponent(name)}/files/download?${params.toString()}`;
  },
  uploadVolumeFile: async (name: string, destDir: string, file: File) => {
    const params = new URLSearchParams({ path: destDir, name: file.name });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    const res = await fetch(`/api/volumes/${encodeURIComponent(name)}/files/upload?${params.toString()}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/octet-stream" }, body: file,
    });
    const text = await res.text();
    const data = parseJSON(text);
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { ok: boolean; error?: string; bytes?: number };
  },
  deleteVolumeFile: (name: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<{ ok: boolean; error?: string }>("DELETE", `/api/volumes/${encodeURIComponent(name)}/files?${params.toString()}`);
  },
  mkdirVolumeFile: (name: string, p: string) => {
    const params = new URLSearchParams({ path: p });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<{ ok: boolean; error?: string }>("POST", `/api/volumes/${encodeURIComponent(name)}/files/mkdir?${params.toString()}`);
  },
  extractVolumeFile: async (name: string, destDir: string, file: File) => {
    const params = new URLSearchParams({ path: destDir, name: file.name });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    const res = await fetch(`/api/volumes/${encodeURIComponent(name)}/files/extract?${params.toString()}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/octet-stream" }, body: file,
    });
    const text = await res.text();
    const data = parseJSON(text);
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { ok: boolean; error?: string };
  },
  closeVolumeBrowser: (name: string) => {
    const params = new URLSearchParams();
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    const q = params.toString();
    return req<{ ok: boolean }>("DELETE", `/api/volumes/${encodeURIComponent(name)}/browse${q ? "?" + q : ""}`);
  },

  createContainer: (spec: CreateSpec) =>
    req<{ ok: boolean; id?: string; error?: string }>("POST", `/api/containers${hostParam()}`, spec),
  renameContainer: (id: string, name: string) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/containers/${id}/rename${hostParam()}`, { name }),
  updateContainer: (id: string, body: { memory: number; nanoCpus: number; restartPolicy: string }) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/containers/${id}/update${hostParam()}`, body),
  commitContainer: (id: string, body: { ref: string; comment: string }) =>
    req<{ ok: boolean; imageId?: string; error?: string }>("POST", `/api/containers/${id}/commit${hostParam()}`, body),
  probePorts: (id: string) => req<PortProbe[]>("POST", `/api/containers/${id}/probe${hostParam()}`),

  // Compose stacks
  stacks: () => req<Stack[]>("GET", `/api/stacks${hostParam()}`),
  stackAction: (project: string, action: string) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/stacks/${encodeURIComponent(project)}/${action}${hostParam()}`),
  stackCompose: (project: string) =>
    req<{ ok: boolean; path?: string; content?: string; error?: string; editable?: boolean; readOnlyReason?: string }>(
      "GET",
      `/api/stacks/${encodeURIComponent(project)}/compose${hostParam()}`,
    ),
  // Writes the compose file back to the host WITHOUT applying it — saving and
  // redeploying are separate so a half-finished edit doesn't restart anything.
  writeStackCompose: (project: string, content: string) =>
    req<{ ok: boolean; path?: string; error?: string }>("PUT", `/api/stacks/${encodeURIComponent(project)}/compose${hostParam()}`, {
      content,
    }),
  redeployStack: (project: string) =>
    req<{ ok: boolean; output?: string; error?: string }>("POST", `/api/stacks/${encodeURIComponent(project)}/redeploy${hostParam()}`),

  // Compose projects (managed folders; local host only — no hostParam)
  projects: () => req<{ projects: Project[]; composeAvailable: boolean }>("GET", "/api/projects"),
  createProject: (
    name: string,
    opts?: {
      hostId?: number;
      template?: TemplateRef;
      instances?: { block: TemplateRef; key: string; merge: TemplateRef[] }[];
      fragments?: TemplateRef[];
      variables?: Record<string, string>;
    },
  ) => req<{ id: number; slug: string }>("POST", "/api/projects", { name, ...opts }),
  importProject: async (name: string, file: File) => {
    const res = await fetch(`/api/projects/import?name=${encodeURIComponent(name)}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/zip" }, body: file,
    });
    const text = await res.text();
    const data = parseJSON(text);
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { id: number; slug: string; files: number };
  },
  renameProject: (id: number, name: string, hostId?: number, allowRemoteHostPaths?: boolean, tearDownOldHost?: boolean) =>
    req<{ ok: boolean; tornDown?: boolean; removedVolumes?: string[] | null; volumeError?: string }>(
      "PATCH", `/api/projects/${id}`,
      { name, hostId, allowRemoteHostPaths: !!allowRemoteHostPaths, tearDownOldHost: !!tearDownOldHost }),
  // Volumes seeded on a remote host for this project's bind mounts. Used by the
  // delete flow to say how many there are before offering to remove them.
  projectSeedVolumes: (id: number) =>
    req<{ volumes: string[]; error?: string }>("GET", `/api/projects/${id}/seed-volumes`),
  // `volumes` also removes the project's seeded volumes on its host — that's
  // data, so it is only ever passed after an explicit confirmation.
  deleteProject: (id: number, force = false, volumes = false) =>
    req<{ ok: boolean; error?: string; output?: string; removedVolumes?: string[]; volumeError?: string }>(
      "DELETE",
      `/api/projects/${id}?${new URLSearchParams({ ...(force ? { force: "1" } : {}), ...(volumes ? { volumes: "1" } : {}) })}`,
    ),
  projectFiles: (id: number) => req<ProjectFile[]>("GET", `/api/projects/${id}/files`),
  makeProjectDir: (id: number, name: string) => req<{ ok: boolean }>("POST", `/api/projects/${id}/files/dir`, { name }),
  writeProjectFile: (id: number, name: string, content: string) =>
    req<{ ok: boolean }>("PUT", `/api/projects/${id}/files`, { name, content }),
  deleteProjectFile: (id: number, path: string) =>
    req<{ ok: boolean }>("DELETE", `/api/projects/${id}/files?path=${encodeURIComponent(path)}`),
  // Raw (octet-stream) upload + download for binary/data files that can't go
  // through the JSON text editor. Projects are local-only — no host param.
  projectFileDownloadUrl: (id: number, name: string) =>
    `/api/projects/${id}/files/raw?path=${encodeURIComponent(name)}`,
  uploadProjectFile: async (id: number, name: string, file: File) => {
    const res = await fetch(`/api/projects/${id}/files/raw?path=${encodeURIComponent(name)}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/octet-stream" }, body: file,
    });
    const text = await res.text();
    const data = parseJSON(text);
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { ok: boolean; error?: string; bytes?: number };
  },
  projectProfiles: (id: number) =>
    req<{ profiles: string[]; error?: string }>("GET", `/api/projects/${id}/profiles`),
  // With an overlay {name, content} it validates the unsaved editor buffer
  // (server copies the project + overlays that file); without it, the on-disk files.
  validateProject: (id: number, overlay?: { name: string; content: string }) =>
    req<{ valid: boolean; error?: string; unavailable?: boolean; warnings?: string[] }>("POST", `/api/projects/${id}/validate`, overlay),
  // Fully-resolved compose config (anchors/interpolation/extends flattened).
  resolveProject: (id: number, overlay?: { name: string; content: string }) =>
    req<{ ok: boolean; config?: string; error?: string }>("POST", `/api/projects/${id}/resolve`, overlay),
  // Resolved compose model (JSON) for the overview / port-conflict check.
  projectSummary: (id: number, overlay?: { name: string; content: string }) =>
    req<{ ok: boolean; model?: ComposeModel; error?: string }>("POST", `/api/projects/${id}/summary`, overlay),
  // What deploying this project would change — services added/recreated/left
  // alone, and (for anything already running) image/digest/env/ports/volumes/
  // networks/restart/resources/healthcheck differences. Read-only: a GET.
  previewProject: (id: number) => req<DeployPreview>("GET", `/api/projects/${id}/preview`),
  // Marks one (service, kind) drift as reviewed/accepted so it stops counting
  // toward `active` on future previews (still shown, marked `ignored`), or
  // reverses that.
  ignoreDrift: (id: number, service: string, kind: string) =>
    req<{ ok: boolean }>("POST", `/api/projects/${id}/drift/ignore`, { service, kind }),
  unignoreDrift: (id: number, service: string, kind: string) =>
    req<{ ok: boolean }>("POST", `/api/projects/${id}/drift/unignore`, { service, kind }),
  // Lint a Dockerfile via `docker build --check` (no build steps run).
  checkDockerfile: (id: number, content: string) =>
    req<{ level: "ok" | "warning" | "error"; output?: string; unavailable?: boolean }>("POST", `/api/projects/${id}/dockerfile-check`, { content }),
  // `note` is set when deploying to a remote host copied bind-mounted paths into
  // seeded volumes — the UI shows it above the compose output.
  deployProject: (id: number, profiles: string[] = []) =>
    req<{ ok: boolean; output?: string; error?: string; note?: string }>("POST", `/api/projects/${id}/deploy`, { profiles }),
  downProject: (id: number) =>
    req<{ ok: boolean; output?: string; error?: string }>("POST", `/api/projects/${id}/down`),
  restartProject: (id: number) =>
    req<{ ok: boolean; output?: string; error?: string }>("POST", `/api/projects/${id}/restart`),
  projectDownloadUrl: (id: number) => `/api/projects/${id}/download`,

  // Project templates (presets) + builder service blocks — builtin + user merged.
  projectTemplates: () => req<ProjectTemplateMeta[]>("GET", "/api/project-templates"),
  serviceBlocks: () => req<ServiceBlockMeta[]>("GET", "/api/service-blocks"),
  // Live read-only preview: the compose.yml (+ sidecars) a selection would seed,
  // without creating a project. Powers the New project dialog preview.
  previewTemplate: (opts: {
    name?: string;
    template?: TemplateRef;
    instances?: { block: TemplateRef; key: string; merge: TemplateRef[] }[];
    fragments?: TemplateRef[];
    variables?: Record<string, string>;
  }) =>
    req<{ files: TemplateFile[]; valid?: boolean; error?: string; warnings?: string[] }>("POST", "/api/project-templates/preview", opts),
  saveProjectAsTemplate: (fromProjectId: number, name: string, description: string) =>
    req<{ id: number; slug: string }>("POST", "/api/project-templates", { fromProjectId, name, description }),
  // Single-preset detail (its files) for the management page's view/edit.
  projectTemplate: (id: string) => req<ProjectTemplateDetail>("GET", `/api/project-templates/${id}`),
  updateProjectTemplate: (id: string, name: string, description: string) =>
    req<{ ok: boolean }>("PUT", `/api/project-templates/${id}`, { name, description }),
  // Copy any preset (built-in or user) into a new editable user preset. Built-in
  // sources are rendered with their default variables first.
  duplicateProjectTemplate: (id: string, name: string) =>
    req<{ id: number; slug: string }>("POST", `/api/project-templates/${id}/duplicate`, { name }),
  deleteProjectTemplate: (id: string) => req<{ ok: boolean }>("DELETE", `/api/project-templates/${id}`),
  // Template file editing (user presets only — local, no host param).
  templateFiles: (id: string) => req<ProjectFile[]>("GET", `/api/project-templates/${id}/files`),
  makeTemplateDir: (id: string, name: string) => req<{ ok: boolean }>("POST", `/api/project-templates/${id}/files/dir`, { name }),
  writeTemplateFile: (id: string, name: string, content: string) =>
    req<{ ok: boolean }>("PUT", `/api/project-templates/${id}/files`, { name, content }),
  deleteTemplateFile: (id: string, path: string) =>
    req<{ ok: boolean }>("DELETE", `/api/project-templates/${id}/files?path=${encodeURIComponent(path)}`),
  templateFileDownloadUrl: (id: string, name: string) =>
    `/api/project-templates/${id}/files/raw?path=${encodeURIComponent(name)}`,
  uploadTemplateFile: async (id: string, name: string, file: File) => {
    const res = await fetch(`/api/project-templates/${id}/files/raw?path=${encodeURIComponent(name)}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/octet-stream" }, body: file,
    });
    const text = await res.text();
    const data = parseJSON(text);
    if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
    return data as { ok: boolean; error?: string; bytes?: number };
  },
  templateDownloadUrl: (id: string) => `/api/project-templates/${id}/download`,
  createServiceBlock: (b: { name: string; description: string; service: string; serviceYaml: string; volumes: string[] }) =>
    req<{ id: number; slug: string }>("POST", "/api/service-blocks", b),
  serviceBlock: (id: string) => req<ServiceBlockDetail>("GET", `/api/service-blocks/${id}`),
  updateServiceBlock: (id: string, b: { name: string; description: string; service: string; serviceYaml: string; volumes: string[] }) =>
    req<{ ok: boolean }>("PUT", `/api/service-blocks/${id}`, b),
  duplicateServiceBlock: (id: string, name: string) =>
    req<{ id: number; slug: string }>("POST", `/api/service-blocks/${id}/duplicate`, { name }),
  deleteServiceBlock: (id: string) => req<{ ok: boolean }>("DELETE", `/api/service-blocks/${id}`),
  // Builder shared definitions (top-level YAML anchors) — builtin + user merged.
  composeFragments: () => req<ComposeFragmentMeta[]>("GET", "/api/compose-fragments"),
  composeFragment: (id: string) => req<ComposeFragmentDetail>("GET", `/api/compose-fragments/${id}`),
  createComposeFragment: (b: { name: string; description: string; content: string }) =>
    req<{ id: number; slug: string }>("POST", "/api/compose-fragments", b),
  updateComposeFragment: (id: string, b: { name: string; description: string; content: string }) =>
    req<{ ok: boolean }>("PUT", `/api/compose-fragments/${id}`, b),
  duplicateComposeFragment: (id: string, name: string) =>
    req<{ id: number; slug: string }>("POST", `/api/compose-fragments/${id}/duplicate`, { name }),
  deleteComposeFragment: (id: string) => req<{ ok: boolean }>("DELETE", `/api/compose-fragments/${id}`),

  // Generic raw inspect for any object kind. id/ref travels as a query param.
  inspect: (kind: "container" | "image" | "network" | "volume", id: string) => {
    const params = new URLSearchParams({ id });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<unknown>("GET", `/api/inspect/${kind}?${params.toString()}`);
  },

  diskUsage: () => req<DiskUsage>("GET", `/api/system/df${hostParam()}`),

  images: () => req<ImageSummary[]>("GET", `/api/images${hostParam()}`),
  // Image-name autocomplete: Docker Hub repo search (via the host daemon) and Hub
  // tag listing. Both are best-effort — the server returns [] on any error.
  searchImages: (q: string) => {
    const params = new URLSearchParams({ q });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<ImageSearchResult[]>("GET", `/api/images/search?${params.toString()}`);
  },
  imageTags: (repo: string) => req<string[]>("GET", `/api/images/tags?repo=${encodeURIComponent(repo)}`),
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
  scanImage: (ref: string) => {
    const params = new URLSearchParams({ ref });
    const h = getHostId();
    if (h != null) params.set("host", String(h));
    return req<ScanResponse>("GET", `/api/images/scan?${params.toString()}`);
  },
  // Ignored CVEs are global (a CVE id means the same thing on every host), so
  // — unlike scanImage — these deliberately carry no ?host=.
  ignoredCVEs: () => req<IgnoredCVE[]>("GET", "/api/images/ignored-cves"),
  ignoreCVEs: (ids: string[], reason: string) =>
    req<{ ok: boolean }>("POST", "/api/images/ignored-cves", { ids, reason }),
  unignoreCVE: (id: string) =>
    req<{ ok: boolean }>("DELETE", `/api/images/ignored-cves/${encodeURIComponent(id)}`),

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

  // MCP access tokens (self-service — each user manages their own).
  mcpStatus: () => req<MCPStatus>("GET", "/api/mcp/status"),
  mcpTokens: () => req<MCPToken[]>("GET", "/api/mcp/tokens"),
  createMcpToken: (b: {
    name: string;
    readOnly: boolean;
    sections: string[];
    expiresInDays: number;
    // Separate from expiresInDays: 0 means "not chosen" (the server applies its
    // default), so wanting no expiry at all has to be said explicitly.
    neverExpires?: boolean;
  }) =>
    req<{ id: number; token: string }>("POST", "/api/mcp/tokens", b),
  deleteMcpToken: (id: number) => req<{ ok: boolean }>("DELETE", `/api/mcp/tokens/${id}`),

  // MCP admin overview (admin-only): every user's tokens + registered OAuth clients.
  mcpAdminTokens: () => req<AdminMCPToken[]>("GET", "/api/mcp-admin/tokens"),
  mcpAdminRevokeToken: (id: number) => req<{ ok: boolean }>("DELETE", `/api/mcp-admin/tokens/${id}`),
  mcpAdminTokenPolicy: () => req<MCPTokenPolicy>("GET", "/api/mcp-admin/token-policy"),
  mcpAdminSetTokenPolicy: (b: MCPTokenPolicy) => req<MCPTokenPolicy>("PUT", "/api/mcp-admin/token-policy", b),
  mcpAdminOAuthClients: () => req<AdminOAuthClient[]>("GET", "/api/mcp-admin/oauth-clients"),
  mcpAdminDeleteOAuthClient: (id: string) =>
    req<{ ok: boolean }>("DELETE", `/api/mcp-admin/oauth-clients/${encodeURIComponent(id)}`),

  networkStats: (id: string) =>
    req<NetworkStats>("GET", `/api/networks/${encodeURIComponent(id)}/stats${hostParam()}`),
  networks: () => req<NetworkSummary[]>("GET", `/api/networks${hostParam()}`),
  createNetwork: (b: { name: string; driver?: string; subnet?: string; gateway?: string; internal?: boolean; attachable?: boolean }) =>
    req<{ ok: boolean; id?: string; error?: string }>("POST", `/api/networks${hostParam()}`, b),
  deleteNetwork: (id: string) => req<{ ok: boolean; error?: string }>("DELETE", `/api/networks/${id}${hostParam()}`),
  pruneNetworks: () => req<{ deleted: string[] | null }>("POST", `/api/networks/prune${hostParam()}`),
  connectNetwork: (id: string, container: string) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/networks/${id}/connect${hostParam()}`, { container }),
  disconnectNetwork: (id: string, container: string, force = false) =>
    req<{ ok: boolean; error?: string }>("POST", `/api/networks/${id}/disconnect${hostParam()}`, { container, force }),

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
  version: () => req<{ version: string }>("GET", "/api/version"),
  updateStatus: () => req<UpdateStatus>("GET", "/api/update"), // admin-only
  applyUpdate: () => req<{ from: string; to: string; restartRequired: boolean }>("POST", "/api/update"), // admin-only
  restartServer: () => req<{ restarting: boolean }>("POST", "/api/update/restart"), // admin-only
  prefs: () => req<Record<string, unknown>>("GET", "/api/prefs"),
  savePrefs: (obj: Record<string, unknown>) => req<{ ok: boolean }>("PUT", "/api/prefs", obj),
  system: () => req<SystemInfo>("GET", `/api/system${hostParam()}`),
  statsOverview: () => req<ResourceOverview>("GET", `/api/stats/overview${hostParam()}`),
  hostPorts: () => req<HostPortProbe[]>("GET", `/api/stats/ports${hostParam()}`),
  // hostSystem fetches engine/host info for a specific host (not the active one).
  hostSystem: (id: number) => req<SystemInfo>("GET", `/api/system?host=${id}`),
  audit: (limit = 50, before?: number) =>
    req<AuditEntry[]>("GET", `/api/audit?limit=${limit}${before ? `&before=${before}` : ""}`),
  runDiagnostics: () => req<DiagnosticsReport>("POST", `/api/diagnostics/run${hostParam()}`),

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
    emails?: string[];
    cooldownSec: number;
  }) => req<{ id: number }>("POST", "/api/alert-rules", body),
  updateAlertRule: (
    id: number,
    body: { name: string; type: string; target: string; config: unknown; severity: string; webhookId: number | null; email: boolean; emails?: string[]; cooldownSec: number }
  ) => req<{ ok: boolean }>("PUT", `/api/alert-rules/${id}`, body),
  toggleAlertRule: (id: number, enabled: boolean) =>
    req<{ ok: boolean }>("PATCH", `/api/alert-rules/${id}`, { enabled }),
  deleteAlertRule: (id: number) => req<{ ok: boolean }>("DELETE", `/api/alert-rules/${id}`),
  exportAlertRulesUrl: () => "/api/alert-rules/export",
  importAlertRules: (bundle: unknown) =>
    req<{ imported: number; warnings: string[] }>("POST", "/api/alert-rules/import", bundle),

  alerts: (params?: AlertListParams) => {
    const p = new URLSearchParams();
    if (params?.severity) p.set("severity", params.severity);
    if (params?.kind) p.set("kind", params.kind);
    if (params?.host !== undefined) p.set("host", String(params.host));
    if (params?.container) p.set("container", params.container);
    if (params?.rule) p.set("rule", params.rule);
    if (params?.q) p.set("q", params.q);
    if (params?.unacked) p.set("unacked", "1");
    if (params?.sort) p.set("sort", params.sort);
    if (params?.desc) p.set("desc", "1");
    if (params?.limit !== undefined) p.set("limit", String(params.limit));
    if (params?.offset !== undefined) p.set("offset", String(params.offset));
    const qs = p.toString();
    return req<{ events: AlertEvent[]; unread: number; total: number; outstanding: number; limit: number; offset: number }>(
      "GET",
      `/api/alerts${qs ? `?${qs}` : ""}`,
    );
  },
  ackAllAlerts: (params?: AlertListParams) => {
    const p = new URLSearchParams();
    if (params?.severity) p.set("severity", params.severity);
    if (params?.kind) p.set("kind", params.kind);
    if (params?.host !== undefined) p.set("host", String(params.host));
    if (params?.container) p.set("container", params.container);
    if (params?.rule) p.set("rule", params.rule);
    if (params?.q) p.set("q", params.q);
    const qs = p.toString();
    return req<{ ok: boolean; acknowledged: number }>("POST", `/api/alerts/ack-all${qs ? `?${qs}` : ""}`);
  },
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

// File-browser adapters: the FileBrowser component works over a FileApi, so the
// same UI serves both containers and volumes. Adapters are cached per id/name so
// inline JSX use (`<FileBrowser fs={fileApiForContainer(id)} />`) returns the
// same object reference across re-renders — a fresh object would reset the
// browser's state and trigger needless reloads.
const containerFileApis = new Map<string, FileApi>();
const volumeFileApis = new Map<string, FileApi>();

export function fileApiForContainer(id: string): FileApi {
  let fs = containerFileApis.get(id);
  if (!fs) {
    fs = {
      list: (p) => api.listFiles(id, p),
      upload: (dir, file) => api.uploadFile(id, dir, file),
      uploadExtract: (dir, file) => api.extractFile(id, dir, file),
      mkdir: (p) => api.mkdirFile(id, p),
      del: (p) => api.deleteFile(id, p),
      downloadUrl: (p) => api.downloadFileUrl(id, p),
    };
    containerFileApis.set(id, fs);
  }
  return fs;
}

export function fileApiForVolume(name: string): FileApi {
  let fs = volumeFileApis.get(name);
  if (!fs) {
    fs = {
      list: (p) => api.listVolumeFiles(name, p),
      upload: (dir, file) => api.uploadVolumeFile(name, dir, file),
      uploadExtract: (dir, file) => api.extractVolumeFile(name, dir, file),
      mkdir: (p) => api.mkdirVolumeFile(name, p),
      del: (p) => api.deleteVolumeFile(name, p),
      downloadUrl: (p) => api.volumeFileDownloadUrl(name, p),
    };
    volumeFileApis.set(name, fs);
  }
  return fs;
}
