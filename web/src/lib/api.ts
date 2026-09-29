import type {
  ActionResponse,
  Agent,
  Change,
  Channel,
  CheckResult,
  CheckSpec,
  Dashboard,
  DiscoveredDevice,
  FoundCard,
  Incident,
  Maintenance,
  MonitorDetail,
  MonitorView,
  PathPoint,
  Rule,
  SearchResult,
  StatusPage,
  Service,
  ServiceView,
  AgentToken,
  Anomaly,
  APIToken,
  AuditEntry,
  IPAM,
  MapGraph,
  Progress,
  Report,
  RunOptions,
  RunSummary,
  Schedule,
  Segment,
  Settings,
  User,
} from "./types";

export class ApiError extends Error {
  status: number;
  body: Record<string, unknown>;
  constructor(status: number, message: string, body: Record<string, unknown>) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

let unauthorized: () => void = () => {};

/** Registers a callback for 401 responses (session expired). */
export function setUnauthorizedHandler(fn: () => void): void {
  unauthorized = fn;
}

export async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const init: RequestInit = { method, credentials: "same-origin", headers };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    headers["Content-Type"] = "application/json";
  }
  const r = await fetch(`/api/v1${path}`, init);
  if (r.status === 204) return undefined as T;
  const text = await r.text();
  const data: unknown = text ? JSON.parse(text) : null;
  if (!r.ok) {
    const obj = (data ?? {}) as Record<string, unknown>;
    if (r.status === 401 && !path.startsWith("/auth/login")) unauthorized();
    throw new ApiError(r.status, String(obj.error ?? r.statusText), obj);
  }
  return data as T;
}

export interface ConfigChange {
  kind: string;
  name: string;
  action: "create" | "update" | "delete" | "unchanged";
}

/** Applies a YAML configuration file (GitOps); the plan is returned for errors too. */
export async function applyConfig(
  yaml: string,
  opts: { dryRun: boolean; prune: boolean },
): Promise<{ plan: ConfigChange[]; error?: string }> {
  const q = new URLSearchParams();
  if (opts.dryRun) q.set("dry_run", "true");
  if (opts.prune) q.set("prune", "true");
  const r = await fetch(`/api/v1/config?${q.toString()}`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/yaml" },
    body: yaml,
  });
  const data = (await r.json()) as { plan?: ConfigChange[]; error?: string };
  if (r.status === 401) unauthorized();
  return { plan: data.plan ?? [], error: r.ok ? undefined : (data.error ?? r.statusText) };
}

const enc = encodeURIComponent;

export const api = {
  setupState: () => call<{ needs_setup: boolean; version: string }>("GET", "/setup"),
  setup: (username: string, password: string) => call<User>("POST", "/setup", { username, password }),
  login: (username: string, password: string, code?: string) =>
    call<User>("POST", "/auth/login", { username, password, code }),
  oidcInfo: () => call<{ enabled: boolean; name: string }>("GET", "/auth/oidc"),
  logout: () => call<undefined>("POST", "/auth/logout"),
  me: () => call<{ user: User; role: string; version: string }>("GET", "/auth/me"),
  changePassword: (old: string, next: string) => call<undefined>("POST", "/auth/password", { old, new: next }),
  totpStart: () => call<{ secret: string; url: string }>("POST", "/auth/totp"),
  totpConfirm: (secret: string, code: string) => call<undefined>("PUT", "/auth/totp", { secret, code }),
  totpDisable: () => call<undefined>("DELETE", "/auth/totp"),
  pushKey: () => call<{ key: string }>("GET", "/push/key"),
  pushSubscribe: (sub: PushSubscriptionJSON) => call<undefined>("POST", "/push/subscribe", sub),
  pushUnsubscribe: (endpoint: string) => call<undefined>("POST", "/push/unsubscribe", { endpoint }),
  pushTest: () => call<{ sent: number }>("POST", "/push/test"),

  users: () => call<User[]>("GET", "/users"),
  createUser: (username: string, password: string, role: string) =>
    call<User>("POST", "/users", { username, password, role }),
  updateUser: (id: number, patch: Record<string, unknown>) => call<User>("PATCH", `/users/${id}`, patch),
  deleteUser: (id: number) => call<undefined>("DELETE", `/users/${id}`),
  tokens: () => call<APIToken[]>("GET", "/tokens"),
  createToken: (name: string, role: string) => call<{ id: number; token: string }>("POST", "/tokens", { name, role }),
  deleteToken: (id: number) => call<undefined>("DELETE", `/tokens/${id}`),

  agents: () => call<Agent[]>("GET", "/agents"),
  agent: (id: string) => call<Agent>("GET", `/agents/${enc(id)}`),
  updateAgent: (id: string, patch: Record<string, unknown>) => call<Agent>("PATCH", `/agents/${enc(id)}`, patch),
  deleteAgent: (id: string) => call<undefined>("DELETE", `/agents/${enc(id)}`),
  agentTokens: () => call<AgentToken[]>("GET", "/agent-tokens"),
  createAgentToken: (name: string, reusable: boolean, expiresInS: number) =>
    call<{ id: number; token: string; install: Record<string, string> }>("POST", "/agent-tokens", {
      name,
      reusable,
      expires_in_s: expiresInS,
    }),
  deleteAgentToken: (id: number) => call<undefined>("DELETE", `/agent-tokens/${id}`),
  install: () =>
    call<{ gateway: string; ca_fingerprint: string; commands: Record<string, string> }>("GET", "/install"),

  segments: () => call<Segment[]>("GET", "/segments"),
  setSegment: (id: string, name: string, expected: number) =>
    call<undefined>("PUT", `/segments/${enc(id)}`, { name, expected_mbps: expected }),
  ipam: () => call<IPAM[]>("GET", "/ipam"),
  discoveredDevices: () => call<DiscoveredDevice[]>("GET", "/devices/discovered"),
  importKuma: (backup: unknown) => call<{ created: number; skipped: string[] }>("POST", "/import/uptime-kuma", backup),
  importPrometheus: (url: string) => call<{ items: number }>("POST", "/import/prometheus", { url }),
  importHomeAssistant: (url: string, token: string) => call<{ items: number }>("POST", "/import/home-assistant", { url, token }),
  scan: (agent: string, cidrs: string[]) => call<{ status: string }>("POST", "/discovery/scan", { agent, cidrs }),
  anomalies: () => call<Anomaly[]>("GET", "/anomalies"),
  map: () => call<MapGraph>("GET", "/map"),
  pathHistory: (src: string, dst: string, seg = "") =>
    call<PathPoint[]>("GET", `/paths/history?src=${enc(src)}&dst=${enc(dst)}${seg ? `&seg=${enc(seg)}` : ""}`),

  runs: (limit = 50) => call<RunSummary[]>("GET", `/runs?limit=${limit}`),
  run: (id: number) => call<Report>("GET", `/runs/${id}`),
  lastRun: (kind = "") => call<Report | null>("GET", `/runs/last${kind ? `?kind=${kind}` : ""}`),
  activeRun: () => call<Progress | null>("GET", "/runs/active"),
  startRun: (opts: RunOptions) => call<{ id: number }>("POST", "/runs", opts),
  cancelRun: () => call<{ cancelled: boolean }>("POST", "/runs/cancel"),
  estimate: (kind: string, udp = false, bidir = false) =>
    call<{ paths: number; seconds: number }>(
      "GET",
      `/runs/estimate?kind=${kind}&udp=${udp ? 1 : 0}&bidir=${bidir ? 1 : 0}`,
    ),
  schedules: () => call<Schedule[]>("GET", "/schedules"),
  saveSchedule: (s: Partial<Schedule>) =>
    s.id ? call<Schedule>("PUT", `/schedules/${s.id}`, s) : call<Schedule>("POST", "/schedules", s),
  deleteSchedule: (id: number) => call<undefined>("DELETE", `/schedules/${id}`),

  settings: () => call<Settings>("GET", "/settings"),
  saveSettings: (s: Settings) => call<Settings>("PUT", "/settings", s),
  audit: () => call<AuditEntry[]>("GET", "/audit"),

  dashboard: () => call<Dashboard>("GET", "/dashboard"),
  search: (q: string) => call<SearchResult[]>("GET", `/search?q=${enc(q)}`),
  found: () => call<FoundCard[]>("GET", "/found"),
  addFound: (key: string, opts: { name?: string; group?: string; monitor: boolean; tile: boolean }) =>
    call<Service>("POST", "/found/add", { key, ...opts }),
  setFoundState: (key: string, status: "new" | "ignored" | "hidden") => call<undefined>("POST", "/found/state", { key, status }),
  wake: (mac: string, ip?: string) => call<ActionResponse>("POST", "/actions/wol", { mac, ip }),
  restart: (agent: string, source: string, key: string) =>
    call<ActionResponse>("POST", "/actions/restart", { agent, source, key }),
  rescan: () => call<{ agents: number }>("POST", "/discovery/rescan"),
  rules: () => call<Rule[]>("GET", "/discovery/rules"),
  saveRules: (rules: Rule[]) => call<Rule[]>("PUT", "/discovery/rules", rules),
  changes: (before = 0, limit = 100) => call<Change[]>("GET", `/changes?before=${before}&limit=${limit}`),

  services: () => call<ServiceView[]>("GET", "/services"),
  saveService: (s: Partial<Service>) =>
    s.id ? call<Service>("PUT", `/services/${s.id}`, s) : call<Service>("POST", "/services", s),
  deleteService: (id: number, monitors = false) => call<undefined>("DELETE", `/services/${id}${monitors ? "?monitors=1" : ""}`),
  orderServices: (order: { id: number; group: string; sort: number }[]) => call<undefined>("PUT", "/services/order", order),

  monitors: () => call<MonitorView[]>("GET", "/monitors"),
  monitor: (id: number, since = 0) => call<MonitorDetail>("GET", `/monitors/${id}${since ? `?since=${since}` : ""}`),
  saveMonitor: (m: Record<string, unknown> & { id?: number }) =>
    m.id ? call<MonitorView>("PUT", `/monitors/${m.id}`, m) : call<MonitorView>("POST", "/monitors", m),
  deleteMonitor: (id: number) => call<undefined>("DELETE", `/monitors/${id}`),
  checkNow: (id: number) => call<{ result: CheckResult; points: CheckResult[] }>("POST", `/monitors/${id}/check`),
  testMonitor: (spec: CheckSpec, points: string[]) =>
    call<{ result: CheckResult; points: CheckResult[] }>("POST", "/monitors/test", { spec, points }),
  incidents: (open = false, monitor = 0) =>
    call<Incident[]>("GET", `/incidents?${open ? "open=1&" : ""}${monitor ? `monitor=${monitor}` : ""}`),
  incident: (id: number) => call<Incident>("GET", `/incidents/${id}`),
  ackIncident: (id: number) => call<Incident>("POST", `/incidents/${id}/ack`),
  noteIncident: (id: number, text: string) => call<Incident>("POST", `/incidents/${id}/notes`, { text }),
  maintenance: () => call<Maintenance[]>("GET", "/maintenance"),
  createMaintenance: (m: Partial<Maintenance> & { duration_min?: number }) => call<Maintenance>("POST", "/maintenance", m),
  deleteMaintenance: (id: number) => call<undefined>("DELETE", `/maintenance/${id}`),

  channels: () => call<Channel[]>("GET", "/channels"),
  saveChannel: (c: Partial<Channel>) =>
    c.id ? call<Channel>("PUT", `/channels/${c.id}`, c) : call<Channel>("POST", "/channels", c),
  deleteChannel: (id: number) => call<undefined>("DELETE", `/channels/${id}`),
  testChannel: (id: number) => call<{ status: string }>("POST", `/channels/${id}/test`),
  statusPages: () => call<StatusPage[]>("GET", "/status-pages"),
  saveStatusPage: (p: StatusPage) =>
    p.id ? call<StatusPage>("PUT", `/status-pages/${p.id}`, p) : call<StatusPage>("POST", "/status-pages", p),
  deleteStatusPage: (id: number) => call<undefined>("DELETE", `/status-pages/${id}`),
};

/** Subscribes to server-sent events; returns an unsubscribe function. */
export function subscribe(handlers: Record<string, (data: unknown) => void>): () => void {
  let es: EventSource | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let closed = false;
  const connect = () => {
    es = new EventSource("/api/v1/events");
    for (const [type, fn] of Object.entries(handlers)) {
      es.addEventListener(type, (e) => fn(JSON.parse((e as MessageEvent<string>).data) as unknown));
    }
    es.onerror = () => {
      es?.close();
      if (!closed) timer = setTimeout(connect, 3000);
    };
  };
  connect();
  return () => {
    closed = true;
    clearTimeout(timer);
    es?.close();
  };
}
