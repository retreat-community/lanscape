import type { Verdict } from "./types";

/** Formats bits per second as Mbit/s or Gbit/s. */
export function rate(bps: number | undefined | null): string {
  if (!bps) return "—";
  const m = bps / 1e6;
  if (m >= 1000) return `${(m / 1000).toFixed(m >= 10000 ? 0 : 2)} Gbit/s`;
  if (m >= 100) return `${Math.round(m)} Mbit/s`;
  if (m >= 10) return `${m.toFixed(1)} Mbit/s`;
  return `${m.toFixed(2)} Mbit/s`;
}

/** Short number of Mbit/s for matrix cells. */
export function mbps(bps: number): string {
  const m = bps / 1e6;
  return m >= 100 ? `${Math.round(m)}` : m >= 10 ? m.toFixed(1) : m.toFixed(2);
}

export function ms(us: number | undefined | null): string {
  if (!us) return "—";
  const v = us / 1000;
  return `${v < 10 ? v.toFixed(2) : v.toFixed(1)} ms`;
}

export function bytes(n: number): string {
  const u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${u[i]}`;
}

export function duration(s: number): string {
  if (s < 60) return `${Math.round(s)}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

export function when(t: number, lang: string): string {
  if (!t) return "—";
  return new Date(t).toLocaleString(lang === "ru" ? "ru-RU" : "en-GB");
}

const rank: Record<Verdict, number> = { green: 1, none: 2, purple: 3, yellow: 4, red: 5 };

/** Returns the worse of two verdicts. */
export function worse(a: Verdict | undefined, b: Verdict | undefined): Verdict | undefined {
  if (!a) return b;
  if (!b) return a;
  return rank[b] > rank[a] ? b : a;
}

export function mask(secret: string): string {
  if (secret.length <= 8) return "••••";
  return `${secret.slice(0, 4)}••••${secret.slice(-4)}`;
}
