import { describe, expect, it } from "vitest";
import { buildMatrix, nameMap, summarize } from "./matrix";
import type { PathResult, Segment } from "./types";

const seg: Segment = {
  id: "10.31.0.0/24 vlan 301",
  cidr: "10.31.0.0/24",
  vlan: 301,
  expected_mbps: 100,
  manual: true,
  members: [
    { node: "a", iface: "eth1", ip: "10.31.0.1", speed: 1000, mtu: 1500, kind: "vlan" },
    { node: "b", iface: "eth1", ip: "10.31.0.2", speed: 1000, mtu: 1500, kind: "vlan" },
  ],
};

const path = (src: string, dst: string, bps: number, echo = "ok"): PathResult => ({
  seg: 0,
  seg_id: seg.id,
  src,
  src_if: "eth1",
  src_ip: "",
  dst,
  dst_if: "eth1",
  dst_ip: "",
  ping: { status: "ok", sent: 5, recv: 5, rtt_min_us: 1, rtt_avg_us: 1, rtt_p95_us: 1, rtt_max_us: 1, jitter_us: 0 },
  echo: { status: echo, sent: 1, recv: 1, rtt_min_us: 1, rtt_avg_us: 1, rtt_p95_us: 1, rtt_max_us: 1, jitter_us: 0 },
  mtu: null,
  tcp1: null,
  tcpn: null,
  expected_mbps: 100,
  best_bps: bps,
  verdict: bps ? "green" : "red",
  mismatch: false,
  mtu_ok: true,
  loss_high: false,
  rtt_high: false,
});

describe("matrix", () => {
  it("indexes paths by endpoint", () => {
    const m = buildMatrix(seg, 0, [path("a", "b", 95e6), path("b", "a", 0, "refused")]);
    expect(m.rows.map((r) => r.key)).toEqual(["a/eth1", "b/eth1"]);
    expect(m.cell(m.rows[0], m.rows[1])?.best_bps).toBe(95e6);
    expect(m.cell(m.rows[0], m.rows[0])).toBeUndefined();
    const s = summarize(m.cell(m.rows[1], m.rows[0])!);
    expect(s.main).toBe("TCP ✗");
    expect(s.icmp).toBe(true);
  });
  it("maps node names", () => {
    expect(nameMap([{ id: "x1", name: "router" }])("x1")).toBe("router");
    expect(nameMap(null)("x1")).toBe("x1");
  });
});
