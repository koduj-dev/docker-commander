// Shared types mirroring the Go API responses.

export interface User {
  id: number;
  username: string;
  role: string;
  totpEnabled: boolean;
}

export interface Host {
  id: number;
  name: string;
  kind: string; // local | tcp | ssh
  address: string;
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

export interface PullProgress {
  status?: string;
  id?: string;
  current?: number;
  total?: number;
  error?: string;
  done?: boolean;
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
  architecture: string;
  cpus: number;
  memTotal: number;
  containersRunning: number;
  containersStopped: number;
  images: number;
}

export interface StatsSample {
  containerId: string;
  timestamp: number;
  cpuPercent: number;
  memUsage: number;
  memLimit: number;
  memPercent: number;
  netRx: number;
  netTx: number;
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
  cooldownSec: number;
  createdAt: string;
}

export interface AlertEvent {
  id: number;
  ruleId: number;
  ruleName: string;
  type: string;
  severity: Severity;
  containerId: string;
  containerName: string;
  message: string;
  value: number | null;
  acknowledged: boolean;
  createdAt: string;
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
