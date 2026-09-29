import { describe, expect, it } from "vitest";
import { elements, filterToQuery, queryToFilter } from "./mapview";
import type { MapGraph } from "./types";

const g: MapGraph = {
  nodes: [
    { id: "dev:a", label: "a", type: "device", status: "green" },
    { id: "dev:b", label: "b", type: "device", status: "red" },
    { id: "port:a/eth0", label: "eth0", type: "port", parent: "dev:a" },
    { id: "port:b/eth0", label: "eth0", type: "port", parent: "dev:b" },
    { id: "seg:lan", label: "lan", type: "segment" },
  ],
  edges: [
    { id: "l1", source: "port:a/eth0", target: "seg:lan", type: "link" },
    { id: "l2", source: "port:b/eth0", target: "seg:lan", type: "link" },
    { id: "p1", source: "dev:a", target: "dev:b", type: "path", verdict: "red", label: "10 Mbit/s" },
  ],
  hypotheses: [],
};

describe("map elements", () => {
  it("keeps everything by default", () => {
    const els = elements(g, queryToFilter(""), {});
    expect(els.length).toBe(8);
  });
  it("hides paths and filters problems", () => {
    expect(elements(g, { ...queryToFilter(""), paths: false }, {}).some((e) => e.data.id === "p1")).toBe(false);
    const onlyBad = elements(g, { ...queryToFilter(""), onlyProblems: true }, {});
    expect(onlyBad.some((e) => e.data.id === "dev:b")).toBe(true);
    expect(onlyBad.some((e) => e.data.id === "p1")).toBe(true);
  });
  it("round-trips the filter through the URL", () => {
    const f = { paths: false, services: true, onlyProblems: true, segment: "10.31.0.0/24 vlan 301" };
    expect(queryToFilter(filterToQuery(f))).toEqual(f);
  });
});

describe("nesting", () => {
  it("drops the parent of a node whose container is filtered out", () => {
    const g = {
      nodes: [
        { id: "dev:host", label: "host", type: "device", status: "red" },
        { id: "dev:vm", label: "vm", type: "device", parent: "dev:host", status: "red" },
        { id: "dev:ok", label: "ok", type: "device", parent: "dev:host", status: "green" },
      ],
      edges: [],
      hypotheses: [],
    };
    const els = elements(g, { paths: true, services: true, onlyProblems: false, segment: "" }, {});
    expect(els.find((e) => e.data.id === "dev:vm")?.data.parent).toBe("dev:host");
    const onlyVM = elements({ ...g, nodes: g.nodes.slice(1) }, { paths: true, services: true, onlyProblems: false, segment: "" }, {});
    expect(onlyVM.find((e) => e.data.id === "dev:vm")?.data.parent).toBeUndefined();
  });
});
