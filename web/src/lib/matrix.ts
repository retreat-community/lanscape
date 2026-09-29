import type { PathResult, Segment, Verdict } from "./types";

export interface MatrixEndpoint {
  key: string;
  node: string;
  iface: string;
  ip: string;
  speed: number;
}

export interface Matrix {
  rows: MatrixEndpoint[];
  cell: (src: MatrixEndpoint, dst: MatrixEndpoint) => PathResult | undefined;
}

/** Builds the node × node matrix of one segment. */
export function buildMatrix(seg: Segment, segIndex: number, paths: PathResult[]): Matrix {
  const rows = seg.members.map((m) => ({ key: `${m.node}/${m.iface}`, node: m.node, iface: m.iface, ip: m.ip, speed: m.speed }));
  const idx = new Map<string, PathResult>();
  for (const p of paths) {
    if (p.seg === segIndex) idx.set(`${p.src}/${p.src_if}>${p.dst}/${p.dst_if}`, p);
  }
  return { rows, cell: (a, b) => idx.get(`${a.key}>${b.key}`) };
}

export type CellSummary = {
  main: string;
  bps: number;
  verdict: Verdict;
  icmp: boolean;
  tcp: boolean;
  mtu: boolean;
  mismatch: boolean;
};

export function summarize(p: PathResult): CellSummary {
  const icmp = !!p.ping && p.ping.recv > 0;
  const tcp = !!p.echo && p.echo.status === "ok";
  return {
    main: p.best_bps ? "" : tcp ? "✗" : "TCP ✗",
    bps: p.best_bps,
    verdict: p.verdict,
    icmp,
    tcp,
    mtu: p.mtu_ok,
    mismatch: p.mismatch,
  };
}

/** Node name lookup for reports (ids of full agents are opaque hex strings). */
export function nameMap(nodes: { id: string; name: string }[] | null | undefined): (id: string) => string {
  const m = new Map((nodes ?? []).map((n) => [n.id, n.name]));
  return (id) => m.get(id) ?? id;
}
