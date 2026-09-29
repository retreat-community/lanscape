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
  segment?: string;
}

export interface PathPoint {
  run_id: number;
  finished: number;
  src_if: string;
  dst_if: string;
  best_bps: number;
  rtt_us?: number;
  verdict: string;
  status?: string;
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

// --- services, discovery, monitors ---

export interface Address {
  type: "internal" | "external" | "hostport" | "workload" | "container" | "process";
  value: string;
  agent?: string;
}

export interface AppMatch {
  id: string;
  name: string;
  category: string;
  icon?: string;
  score: number;
  monitor: { type: string; path?: string };
}

export interface CheckSpec {
  type: "http" | "tcp" | "udp" | "icmp" | "dns" | "tls" | "domain" | "heartbeat" | "composite" | "container" | "k8s" | "vm";
  target: string;
  timeout_ms?: number;
  method?: string;
  expect_status?: number[];
  keyword?: string;
  keyword_regex?: boolean;
  invert_keyword?: boolean;
  json_path?: string;
  json_value?: string;
  headers?: Record<string, string>;
  basic_user?: string;
  basic_password?: string;
  bearer?: string;
  no_redirects?: boolean;
  ignore_tls_errors?: boolean;
  send?: string;
  expect?: string;
  server?: string;
  record?: string;
  warn_days?: number;
  grace_s?: number;
  expr?: string;
}

export type FoundStatus = "new" | "added" | "ignored" | "hidden";

export interface FoundCard {
  key: string;
  name: string;
  app?: AppMatch;
  kind: string;
  namespace?: string;
  agents: string[] | null;
  state?: string;
  health?: string;
  internal_url?: string;
  external_url?: string;
  host_port?: string;
  tls?: boolean;
  cert_not_after?: number;
  addresses: Address[];
  refs: { agent: string; source: string; key: string; kind: string; gone?: boolean }[];
  gone?: boolean;
  first_seen: number;
  monitor?: CheckSpec;
  status: FoundStatus;
  service_id?: number;
  rule?: string;
}

export interface Rule {
  name: string;
  kind?: "" | "ingress" | "k8s" | "container" | "socket";
  app?: string;
  category?: string;
  tls_only?: boolean;
  action: "add" | "ignore";
  monitor?: boolean;
  tile?: boolean;
  group?: string;
}

export interface Service {
  id: number;
  name: string;
  app_id: string;
  icon: string;
  category: string;
  group: string;
  internal_url: string;
  external_url: string;
  addresses: Address[];
  card_key?: string;
  tile: boolean;
  sort: number;
  notes: string;
  created_at: number;
}

export type MonitorStatus = "pending" | "up" | "degraded" | "down" | "paused" | "maintenance";

export interface ServiceView extends Service {
  status: MonitorStatus | "";
  latency_ms: number;
  monitors: number[];
  url: string;
  uptime_day: number | null;
  incident_id?: number;
}

export interface Monitor {
  id: number;
  service_id: number;
  name: string;
  spec: CheckSpec;
  interval_s: number;
  retries: number;
  points: string[];
  min_failing: number;
  sla: number;
  enabled: boolean;
  status: MonitorStatus;
  last_check: number;
  last_latency: number;
  last_message: string;
  cert_not_after?: number;
  created_at: number;
  push_token?: string;
  last_push?: number;
  parents: number[];
}

export interface Uptime {
  day: number | null;
  week: number | null;
  month: number | null;
  year: number | null;
}

export interface MonitorView extends Monitor {
  uptime: Uptime;
  incident_id?: number;
}

export interface CheckResult {
  status: "up" | "down" | "degraded" | "unknown";
  latency_ms: number;
  message?: string;
  code?: number;
  cert_not_after?: number;
  at: number;
  point?: string;
}

export interface CheckRecord {
  ts: number;
  status: string;
  latency_ms: number;
  point?: string;
  message?: string;
}

export interface DayStat {
  day: number;
  up: number;
  down: number;
  degraded: number;
  maint: number;
}

export interface Incident {
  id: number;
  monitor_id: number;
  monitor: string;
  opened: number;
  closed?: number;
  cause: string;
  acked_by?: string;
  acked_at?: number;
  maintenance: boolean;
  suppressed?: boolean;
  parent_id?: number;
  notes?: { id: number; ts: number; author: string; text: string }[];
}

export interface MonitorDetail {
  monitor: Monitor;
  uptime: Uptime;
  days: DayStat[];
  checks: CheckRecord[];
  incidents: Incident[];
}

export interface Maintenance {
  id: number;
  name: string;
  starts: number;
  ends: number;
  monitors: number[];
  created_by: string;
  created_at: number;
}

export type ChannelType = "telegram" | "webhook" | "email" | "ntfy" | "gotify" | "discord" | "slack" | "matrix";

export interface Channel {
  id: number;
  name: string;
  type: ChannelType;
  config: Record<string, unknown>;
  enabled: boolean;
  created_at: number;
}

export interface Change {
  id: number;
  ts: number;
  kind: string;
  subject: string;
  detail?: string;
  agent_id?: string;
}

export interface Dashboard {
  groups: { name: string; tiles: ServiceView[] }[];
  summary: Record<string, number>;
  incidents: Incident[];
  network: { run_id: number; finished: number; status: string; paths: number; verdicts: Record<string, number>; problems: number } | null;
  agents: {
    id: string;
    name: string;
    online: boolean;
    cpus: number;
    load1: number;
    mem_total: number;
    mem_available: number;
    temp_c?: number;
    disk_pct: number;
    disk_mount?: string;
    updates: number;
  }[];
  certificates: { name: string; not_after: number; source: string; ref?: string }[];
  changes: Change[];
  found_new: number;
  maintenance: Maintenance[];
}

export interface SearchResult {
  type: "service" | "monitor" | "agent" | "device" | "found";
  id: string;
  title: string;
  subtitle?: string;
  url?: string;
  href: string;
  status?: string;
}

export interface StatusPage {
  id: number;
  slug: string;
  title: string;
  public: boolean;
  token?: string;
  domain?: string;
  config: {
    description?: string;
    groups: { name: string; monitors: number[] }[];
    accent?: string;
    logo_url?: string;
    theme?: string;
    footer?: string;
  };
  created_at: number;
}
