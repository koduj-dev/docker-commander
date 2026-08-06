// Shared types mirroring the Go API responses.

export interface User {
  id: number;
  username: string;
  role: string;
  /** Where this account's own alert e-mails go. Optional; synced from LDAP when available. */
  email?: string;
  /** "local" (password stored here) or "ldap" (verified against the directory). */
  authSource?: string;
  createdAt?: string;
  lastLoginAt?: string;
  totpEnabled: boolean;
  /** Any second factor at all — an authenticator app OR a passkey. */
  mfaEnabled?: boolean;
  /** Whether a passkey alone may sign this account in. Off unless asked for. */
  passwordless?: boolean;
  readOnly: boolean;
  sections: string[];
  mfaEnforced: boolean;
}

export interface ManagedUser {
  id: number;
  username: string;
  role: string;
  readOnly: boolean;
  sections: string[] | null;
  /** Ids of the named roles assigned to this account. */
  roleIds?: number[] | null;
  /**
   * Sections the account can actually reach — its own list plus every role's,
   * minus app-wide disabled ones. Computed by the server.
   */
  effectiveSections?: string[] | null;
  totpEnabled: boolean;
  lastLoginAt: string;
}

/** One section grant inside a role; `write: false` means read-only there. */
export interface RoleSection {
  section: string;
  write: boolean;
}

/**
 * A named bundle of section grants. Built-in roles are read-only — the UI offers
 * Duplicate to make an editable copy, like project templates.
 */
export interface Role {
  id: number;
  name: string;
  description: string;
  builtin: boolean;
  sections: RoleSection[] | null;
  /** Docker hosts the role is limited to. Empty/absent means EVERY host. */
  hostIds?: number[] | null;
  /** How many accounts currently hold this role. */
  users?: number;
}

export interface AppSettings {
  allSections: string[];
  disabledSections: string[] | null;
  localhostNo2fa: boolean;
}

export interface UpdateStatus {
  current: string;
  latest?: string;
  updateAvailable: boolean;
  url?: string;
  publishedAt?: string;
  disabled?: boolean;
  error?: string;
  /** True when the in-app one-tap "Update & restart" is offered (admin + allowed + restartable). */
  selfUpdate?: boolean;
}

export interface LdapGroupMapping {
  groupDn: string;
  sections: string[];
  /** Named roles granted to members of the group. Absent in configs written before roles existed. */
  roleIds?: number[];
}

export interface LdapConfig {
  enabled: boolean;
  url: string;
  startTls: boolean;
  bindDn: string;
  userBaseDn: string;
  userFilter: string;
  adminGroupDn: string;
  groupMappings?: LdapGroupMapping[];
  /** Role granted in place of a mapped role that no longer exists. 0 = none. */
  fallbackRoleId?: number;
  hasBindPassword?: boolean;
}

export interface Host {
  id: number;
  name: string;
  kind: string; // local | tcp | ssh
  address: string;
  alertEmail?: string;
  disabled?: boolean;
  reachable?: boolean; // monitor's live view; false = daemon unreachable
  unreachableSince?: string; // RFC3339, present only while unreachable
}

export interface PortSpec {
  hostPort: string;
  containerPort: string;
  proto: string;
}

export interface CreateSpec {
  image: string;
  name: string;
  cmd: string[];
  env: string[];
  binds: string[];
  ports: PortSpec[];
  restartPolicy: string;
  memory: number;
  nanoCpus: number;
  start: boolean;
}

export interface PortMapping {
  ip?: string;
  privatePort: number;
  publicPort?: number;
  type: string;
}

export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  created: number;
  ports: PortMapping[] | null;
  networks: string[] | null;
  labels: Record<string, string> | null;
}

export interface MountInfo {
  type: string;
  source: string;
  destination: string;
  rw: boolean;
}

export interface NetworkAttach {
  name: string;
  networkId: string;
  ipAddress: string;
  gateway: string;
  macAddress: string;
}

export interface ContainerDetail {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  health?: string;
  created: string;
  startedAt?: string;
  restartCount: number;
  command: string[];
  env: string[] | null;
  labels: Record<string, string> | null;
  mounts: MountInfo[] | null;
  ports: PortMapping[] | null;
  networks: NetworkAttach[] | null;
  restartPolicy?: string;
}

export interface Registry {
  id: number;
  name: string;
  address: string;
  username: string;
}

export interface ImageSummary {
  id: string;
  repoTags: string[] | null;
  repoDigests: string[] | null;
  size: number;
  created: number; // unix seconds
  dangling: boolean;
  inUse: boolean;
}

// Image vulnerability scan (Trivy).
export interface Vulnerability {
  id: string;
  severity: string; // CRITICAL | HIGH | MEDIUM | LOW | UNKNOWN
  package: string;
  version: string;
  fixedVersion?: string;
  title?: string;
  url?: string;
}

export interface ScanResult {
  ref: string;
  summary: Record<string, number>;
  vulns: Vulnerability[];
}

export interface ScanResponse {
  available: boolean; // false = Trivy not installed
  ok?: boolean;
  error?: string;
  result?: ScanResult;
}

// One Docker Hub search hit, used for image-name autocomplete.
export interface ImageSearchResult {
  name: string;
  description: string;
  stars: number;
  official: boolean;
}

export interface PullProgress {
  status?: string;
  id?: string;
  current?: number;
  total?: number;
  error?: string;
  done?: boolean;
}

export interface FileEntry {
  name: string;
  isDir: boolean;
  isLink: boolean;
  size: number;
  mode: string;
  target?: string;
}

// FileApi abstracts the file operations so the FileBrowser works for both
// containers and volumes (each builds an adapter over its own endpoints).
export interface FileApi {
  list: (path: string) => Promise<{ ok: boolean; entries?: FileEntry[] | null; path?: string; error?: string }>;
  upload: (dir: string, file: File) => Promise<{ ok: boolean; error?: string }>;
  uploadExtract: (dir: string, file: File) => Promise<{ ok: boolean; error?: string }>;
  mkdir: (path: string) => Promise<{ ok: boolean; error?: string }>;
  del: (path: string) => Promise<{ ok: boolean; error?: string }>;
  downloadUrl: (path: string) => string;
}

export interface DiffEntry {
  kind: "modified" | "added" | "deleted" | "unknown";
  path: string;
}

export interface TopResult {
  titles: string[];
  processes: string[][];
}

export interface HistoryEntry {
  id: string;
  created: number;
  createdBy: string;
  size: number;
  comment: string;
  tags: string[] | null;
}

export interface UsageCategory {
  count: number;
  size: number;
}

export interface DiskUsage {
  layersSize: number;
  images: UsageCategory;
  containers: UsageCategory;
  volumes: UsageCategory;
  buildCache: UsageCategory;
}

export interface EventMsg {
  time: number;
  type: string;
  action: string;
  id: string;
  name: string;
  attr?: Record<string, string>;
}

export interface VolumeSummary {
  name: string;
  driver: string;
  mountpoint: string;
  scope: string;
  createdAt: string;
  labels: Record<string, string> | null;
  inUseBy: string[] | null;
}

export interface NetworkSummary {
  id: string;
  name: string;
  driver: string;
  scope: string;
  internal: boolean;
  subnets: string[] | null;
  containers: string[] | null;
}

export interface TopoNetwork {
  id: string;
  name: string;
  driver: string;
  scope: string;
  internal: boolean;
  subnets: string[] | null;
}

export interface TopoContainer {
  id: string;
  name: string;
  image: string;
  state: string;
  stack?: string;
  ports?: PortMapping[];
}

export interface TopoLink {
  containerId: string;
  networkId: string;
  ipAddress: string;
}

export interface Topology {
  networks: TopoNetwork[] | null;
  containers: TopoContainer[] | null;
  links: TopoLink[] | null;
}

export interface SystemInfo {
  hostName: string;
  serverVersion: string;
  operatingSystem: string;
  osType: string;
  osVersion: string;
  kernelVersion: string;
  architecture: string;
  cpus: number;
  memTotal: number;
  storageDriver: string;
  loggingDriver: string;
  cgroupDriver: string;
  cgroupVersion: string;
  dockerRootDir: string;
  liveRestore: boolean;
  containers: number;
  containersRunning: number;
  containersPaused: number;
  containersStopped: number;
  images: number;
}

export interface StackContainer {
  id: string;
  name: string;
  service: string;
  state: string;
  status: string;
  image: string;
  ports?: PortMapping[];
}

export interface Stack {
  project: string;
  configFile?: string;
  workingDir?: string;
  containers: StackContainer[];
  running: number;
}

export interface Project {
  id: number;
  name: string;
  slug: string;
  composeFile: string;
  hostId: number;
  hostName?: string; // resolved label; absent/"" = local daemon
  /**
   * Opt-in: let a remote deploy mount bind sources from OUTSIDE the project
   * folder, i.e. paths on the remote host itself. Off by default; enabling it
   * needs write access to the "hosts" section.
   */
  allowRemoteHostPaths?: boolean;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface ComposeService {
  image?: string;
  build?: { context?: string; dockerfile?: string } | string;
  ports?: { target?: number; published?: string; protocol?: string; mode?: string }[];
  volumes?: ({ type?: string; source?: string; target?: string } | string)[];
  depends_on?: Record<string, unknown> | string[];
  restart?: string;
  profiles?: string[];
}

export interface ComposeModel {
  name?: string;
  services?: Record<string, ComposeService>;
  networks?: Record<string, unknown>;
  volumes?: Record<string, unknown>;
  configs?: Record<string, unknown>;
  secrets?: Record<string, unknown>;
}

export interface ProjectFile {
  name: string;
  size: number;
  content: string;
  isDir?: boolean;
  tooLarge?: boolean;
  binary?: boolean;
}

// Project templates (presets) + builder service blocks. "builtin" ones are
// embedded server-side; "user" ones are saved by the user.
export type TemplateSource = "builtin" | "user" | "remote";

export interface TemplateVariable {
  key: string;
  label: string;
  default?: string;
  secret?: boolean;
  generate?: string;
}

export interface ProjectTemplateMeta {
  id: string;
  name: string;
  description: string;
  source: TemplateSource;
  variables?: TemplateVariable[];
  deletable: boolean;
}

export interface ServiceBlockMeta {
  id: string;
  name: string;
  description: string;
  source: TemplateSource;
  service: string;
  variables?: TemplateVariable[];
  deletable: boolean;
}

// A reference the create-project call uses to identify a preset, block or fragment.
export interface TemplateRef {
  id: string;
  source: TemplateSource;
}

// A builder "shared definition" — a top-level compose fragment (YAML anchor).
export interface ComposeFragmentMeta {
  id: string;
  name: string;
  description: string;
  source: TemplateSource;
  deletable: boolean;
}

export interface ComposeFragmentDetail extends ComposeFragmentMeta {
  content: string;
}

// One file in a rendered template/builder preview ({{.Var}} already substituted).
export interface TemplateFile {
  path: string;
  content: string;
}

// Full block payload (YAML + volumes) for the management page's view/edit.
export interface ServiceBlockDetail extends ServiceBlockMeta {
  serviceYaml: string;
  volumes: string[];
}

// Full preset payload (its files) for the management page's view; user presets
// are edited file-by-file via the template file endpoints.
export interface ProjectTemplateDetail extends ProjectTemplateMeta {
  files: TemplateFile[];
}

export interface PortProbe {
  privatePort: number;
  publicPort: number;
  type: string;
  guessByPort: string;
  open: boolean;
  detected: string;
  info?: string;
  tls: boolean;
  error?: string;
}

export interface HostPortProbe extends PortProbe {
  containerId: string;
  containerName: string;
}

export interface ResourceUsage {
  id: string;
  name: string;
  cpuPercent: number; // share of total host CPU (0..100)
  memBytes: number;
  memPercent: number; // share of total host memory (0..100)
  netRxRate: number; // bytes/s, derived from consecutive polls
  netTxRate: number;
}

export interface ResourceOverview {
  cpus: number;
  memTotal: number;
  containers: ResourceUsage[];
}

export interface StatsSample {
  containerId: string;
  timestamp: number;
  cpuPercent: number;
  memUsage: number;
  memLimit: number;
  memPercent: number;
  // Cumulative since the container started, summed across interfaces. Rates are
  // derived from the delta between samples — see netRates().
  netRx: number;
  netTx: number;
  netRxPackets: number;
  netTxPackets: number;
  netRxDropped: number;
  netTxDropped: number;
  netRxErrors: number;
  netTxErrors: number;
  interfaces?: NetInterfaceStats[];
  blkRead: number;
  blkWrite: number;
  pids: number;
}

export interface LogLine {
  stream: "stdout" | "stderr";
  message: string;
  timestamp?: string;
}

export interface Webhook {
  id: number;
  name: string;
  url: string;
  method: string;
  headers: Record<string, string>;
  bodyTemplate: string;
  createdAt: string;
}

export type AlertType = "state" | "resource" | "log" | "restart";
export type Severity = "info" | "warning" | "critical";

export interface AlertRule {
  id: number;
  name: string;
  enabled: boolean;
  type: AlertType;
  target: string;
  config: string; // raw JSON
  severity: Severity;
  webhookId: number | null;
  email: boolean;
  /** This rule's own recipients. Empty falls back to the instance-wide SMTP "To". */
  emails?: string[] | null;
  cooldownSec: number;
  createdAt: string;
}

export interface ParseRule {
  id: number;
  name: string;
  pattern: string;
  createdAt: string;
}

export interface SmtpConfig {
  host: string;
  port: number;
  username: string;
  from: string;
  to: string;
  tls: boolean;
  hasPassword?: boolean;
}

export interface AlertEvent {
  id: number;
  ruleId: number;
  ruleName: string;
  type: string;
  severity: Severity;
  hostId: number;
  hostName: string;
  containerId: string;
  containerName: string;
  message: string;
  value: number | null;
  acknowledged: boolean;
  // Where in a condition's life this event sits. Threshold rules move between
  // these; state/log/restart rules only ever emit "firing", because a container
  // that died has no later moment at which it stops having died.
  kind: "firing" | "escalated" | "eased" | "repeat" | "resolved";
  durationSec: number;
  acknowledgedBy?: string;
  acknowledgedAt?: string;
  deliveries?: AlertDelivery[];
  createdAt: string;
}

// One attempt to get an alert out of the building. Target is the webhook's name
// and host (never its full URL — those carry tokens) or the mail recipients.
// One container interface's cumulative counters. Docker names these eth0, eth1…
// and does not say which Docker network each belongs to.
export interface NetInterfaceStats {
  name: string;
  rxBytes: number;
  txBytes: number;
  rxPackets: number;
  txPackets: number;
  rxDropped: number;
  txDropped: number;
  rxErrors: number;
  txErrors: number;
}

// Endpoint traffic for a Docker network. Docker reports no per-network counters,
// so these are the attached containers' own totals — and only those attached to
// exactly one network can be attributed to it.
export interface NetworkEndpointStats {
  containerId: string;
  containerName: string;
  rxBytes: number;
  txBytes: number;
  attributable: boolean;
}

export interface NetworkStats {
  networkId: string;
  rxBytes: number;
  txBytes: number;
  endpoints: number;
  unattributed: number;
  containers: NetworkEndpointStats[];
}

export interface AlertDelivery {
  id: number;
  eventId: number;
  channel: "webhook" | "email";
  target: string;
  ok: boolean;
  status?: number;
  detail?: string;
  attemptedAt: string;
}

export interface AuditEntry {
  id: number;
  username: string;
  action: string;
  target: string;
  detail: string;
  ip: string;
  createdAt: string;
}

// MCP access token (self-service personal token for the remote MCP server).
// The secret is returned only once at creation and never again.
export interface MCPToken {
  id: number;
  name: string;
  sections: string[] | null; // null/empty = inherit all of the owner's sections
  readOnly: boolean;
  createdAt: string;
  lastUsedAt?: string;
  expiresAt?: string;
}

export interface MCPStatus {
  enabled: boolean; // the MCP server is turned on (DC_MCP_ENABLED)
  oauth: boolean; // OAuth flow available (public URL configured)
  tokenPolicy: MCPTokenPolicy; // lifetime rules the creation form must respect
}

// How long an MCP bearer token may live. Set by an admin; enforced server-side
// when a token is minted, and reflected in the creation form so a user is never
// offered a lifetime that will be refused.
export interface MCPTokenPolicy {
  defaultDays: number; // applied when the request does not name a lifetime
  maxDays: number; // longest lifetime a user may choose; 0 = no ceiling
  allowUnlimited: boolean; // whether never-expiring tokens may be created at all
}

// Admin overview rows: every user's tokens (with the owner's username) and the
// registered OAuth clients. Admin-only; secrets are never included.
export interface AdminMCPToken extends MCPToken {
  userId: number;
  username: string;
}

export interface AdminOAuthClient {
  id: string;
  name: string;
  redirectUris: string[] | null;
  createdAt: string;
}

/** One section a user can reach, and which role(s) or grant it came from. */
export interface EffectiveGrant {
  section: string;
  write: boolean;
  from: string[];
  /** False when the grant only reaches `hosts` (plus the always-in-scope local daemon). */
  allHosts?: boolean;
  hosts?: number[] | null;
}

/** The signed-in account's own permissions, for the profile page. */
export interface MyAccess {
  admin: boolean;
  readOnly: boolean;
  roles: Role[];
  sections: string[] | null;
  /** Absent for an admin, who bypasses the grant system entirely. */
  effective?: EffectiveGrant[] | null;
  /** Admin only: every section that exists, so "everything" can be shown rather than asserted. */
  allSections?: string[] | null;
  /** Admin only: how many Docker hosts are configured. */
  hostCount?: number;
}

/** One signed-in browser or client, as shown in Profile → Security. */
/** One paired second factor. The secret is never part of this. */
export interface AuthFactor {
  id: number;
  /** "totp" today; passkeys join the same list later. */
  kind: string;
  name: string;
  createdAt: string;
  lastUsedAt: string;
}

export interface Session {
  id: string;
  ip: string;
  userAgent: string;
  createdAt: string;
  lastSeenAt: string;
  /** The session making the request — never offer to sign this one out silently. */
  current: boolean;
}
