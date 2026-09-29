// Types mirroring the JSON of the Lanscape API (internal/server, internal/topo).

export type Role = "viewer" | "operator" | "admin";
export type Verdict = "green" | "yellow" | "red" | "purple" | "none";

export interface User {
  id: number;
  username: string;
  role: Role;
  totp_enabled: boolean;
  disabled: boolean;
  created_at: number;
}

export interface Addr {
  ip: string;
  prefix: number;
}

export interface Iface {
  name: string;
  index: number;
  mac: string;
  mtu: number;
  up: boolean;
  carrier: boolean;
  kind: string;
  parent?: string;
  master?: string;
  members?: string[];
  bond_mode?: string;
  vlan?: number;
  speed: number;
  addrs: Addr[] | null;
}

export interface Disk {
  mount: string;
  device: string;
  fs: string;
  total: number;
  used: number;
}

export interface Inventory {
  ifaces: Iface[] | null;
  routes?: { table: number; dst: string; gateway?: string; dev: string; src?: string; metric: number }[] | null;
  rules?: string[] | null;
  neighbors?: { ip: string; mac: string; dev: string; state: string }[] | null;
  env: { kind: string; virt?: string; k8s_node?: string; hypervisor?: string };
  resources: {
    os: string;
    os_version: string;
    kernel: string;
    arch: string;
    cpus: number;
    cpu_model?: string;
    mem_total: number;
    mem_available: number;
    uptime_s: number;
    load1: number;
    disks?: Disk[] | null;
    updates: number;
    temp_c?: number;
  };
}

export interface Agent {
  id: string;
  name: string;
  kind: "full" | "lite";
  online: boolean;
  version: string;
  os: string;
  arch: string;
  hostname: string;
  host_id: string;
  data_port: number;
  last_seen: number;
  inventory: Inventory;
  labels?: Record<string, string> | null;
}

export interface Member {
  node: string;
  iface: string;
  ip: string;
  speed: number;
  mtu: number;
  kind: string;
  parent?: string;
}

export interface Segment {
  id: string;
  cidr: string;
  vlan: number;
  expected_mbps: number;
  manual: boolean;
  members: Member[];
  name?: string;
}

export interface Probe {
  status: string;
  size?: number;
  sent: number;
  recv: number;
  rtt_min_us: number;
  rtt_avg_us: number;
  rtt_p95_us: number;
  rtt_max_us: number;
  jitter_us: number;
}

export interface Throughput {
  status: string;
  streams: number;
  bps: number;
  bps_reverse?: number;
  cpu_local: number;
  cpu_peer: number;
  path_ok: boolean;
  verified: boolean;
  sent?: number;
  lost?: number;
  loss_pct?: number;
  jitter_us?: number;
  message?: string;
}

export interface PathResult {
  seg: number;
  seg_id: string;
  src: string;
  src_if: string;
  src_ip: string;
  dst: string;
  dst_if: string;
  dst_ip: string;
  ping: Probe | null;
  echo: Probe | null;
  mtu: Probe | null;
  jumbo?: Probe | null;
  tcp1: Throughput | null;
  tcpn: Throughput | null;
  udp?: Throughput | null;
  bidir?: Throughput | null;
  route_dev?: string;
  expected_mbps: number;
  best_bps: number;
  verdict: Verdict;
  mismatch: boolean;
  mtu_ok: boolean;
  loss_high: boolean;
  rtt_high: boolean;
}

export interface Problem {
  kind: string;
  seg: number;
  seg_id: string;
  src: string;
  src_if: string;
  dst: string;
  dst_if: string;
  detail: string;
}

export interface Anomaly {
  kind: string;
  key: string;
  nodes: string[] | null;
  detail: string;
}

export interface NodeInfo {
  id: string;
  name: string;
  kind: string;
  env?: string;
  arch?: string;
  parent?: string;
}

export interface RunOptions {
  kind: "full" | "reachability" | "aggregate";
  duration_ms?: number;
  streams?: number;
  udp?: boolean;
  udp_rate_kbps?: number;
  bidir?: boolean;
  segments?: string[];
  nodes?: string[];
}

export interface AggregateSeg {
  seg: number;
  seg_id: string;
  total_bps: number;
  pairs: number;
  node_out_bps: Record<string, number>;
  node_in_bps: Record<string, number>;
}

export interface Report {
  id: number;
  kind: string;
  status: string;
  started: number;
  finished: number;
  options: RunOptions;
  nodes: NodeInfo[] | null;
  segments: Segment[] | null;
  paths: PathResult[] | null;
  problems: Problem[] | null;
  anomalies: Anomaly[] | null;
  aggregate?: AggregateSeg[] | null;
}

export interface RunSummary {
  id: number;
  kind: string;
  status: string;
  started: number;
  finished: number;
}

export interface Progress {
  id: number;
  kind: string;
  done: number;
  total: number;
  eta_s: number;
  step?: string;
}

export interface MapNode {
  id: string;
  label: string;
  type: string;
  parent?: string;
  icon?: string;
  status?: string;
  data?: Record<string, unknown>;
}

export interface MapEdge {
  id: string;
  source: string;
  target: string;
  type: string;
  verdict?: string;
  label?: string;
}

export interface Hypothesis {
  id: string;
  segment: string;
  mbps: number;
  group_a: string[];
  group_b: string[];
  detail: string;
  accepted: boolean;
}

export interface MapGraph {
  nodes: MapNode[];
  edges: MapEdge[];
  hypotheses: Hypothesis[];
  run_id?: number;
}

export interface IPAMEntry {
  ip: string;
  node?: string;
  iface?: string;
  mac?: string;
  name?: string;
  source: string;
  in_pool: boolean;
}

export interface IPAM {
  segment: string;
  cidr: string;
  size: number;
  used: number;
  free: number;
  pool_size: number;
  entries: IPAMEntry[];
}

export interface Settings {
  duration_ms: number;
  streams: number;
  ping_count: number;
  rtt_warn_ms: number;
  mini_token?: string;
  webhooks: string[] | null;
  retention_days: number;
  public_url?: string;
}

export interface Schedule {
  id: number;
  name: string;
  kind: string;
  spec: string;
  enabled: boolean;
  params?: unknown;
  last_run: number;
}

export interface APIToken {
  id: number;
  user_id: number;
  name: string;
  role: Role;
  created_at: number;
  last_used: number;
  expires_at: number;
}

export interface AgentToken {
  id: number;
  name: string;
  labels: Record<string, string> | null;
  reusable: boolean;
  uses: number;
  expires_at: number;
  created_at: number;
}

export interface AuditEntry {
  id: number;
  ts: number;
  actor: string;
  action: string;
  target: string;
  result: string;
  detail: string;
}
