// Shared types mirroring the Go API responses.

export interface User {
  id: number;
  username: string;
  role: string;
  totpEnabled: boolean;
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

export interface NetworkSummary {
  id: string;
  name: string;
  driver: string;
  scope: string;
  internal: boolean;
  subnets: string[] | null;
  containers: string[] | null;
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

export interface AuditEntry {
  id: number;
  username: string;
  action: string;
  target: string;
  detail: string;
  ip: string;
  createdAt: string;
}
