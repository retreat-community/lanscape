import type {
  Agent,
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

const enc = encodeURIComponent;

export const api = {
  setupState: () => call<{ needs_setup: boolean; version: string }>("GET", "/setup"),
  setup: (username: string, password: string) => call<User>("POST", "/setup", { username, password }),
  login: (username: string, password: string, code?: string) =>
    call<User>("POST", "/auth/login", { username, password, code }),
  logout: () => call<undefined>("POST", "/auth/logout"),
  me: () => call<{ user: User; role: string; version: string }>("GET", "/auth/me"),
  changePassword: (old: string, next: string) => call<undefined>("POST", "/auth/password", { old, new: next }),
  totpStart: () => call<{ secret: string; url: string }>("POST", "/auth/totp"),
  totpConfirm: (secret: string, code: string) => call<undefined>("PUT", "/auth/totp", { secret, code }),
  totpDisable: () => call<undefined>("DELETE", "/auth/totp"),

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
  anomalies: () => call<Anomaly[]>("GET", "/anomalies"),
  map: () => call<MapGraph>("GET", "/map"),

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
