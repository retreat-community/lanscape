import type { ElementDefinition } from "cytoscape";
import type { MapGraph } from "./types";

export interface MapFilter {
  paths: boolean;
  services: boolean;
  onlyProblems: boolean;
  segment: string;
}

const bad = new Set(["red", "yellow", "purple", "offline"]);

/** Converts the server graph into Cytoscape elements according to the filter. */
export function elements(g: MapGraph, f: MapFilter, positions: Record<string, { x: number; y: number }>): ElementDefinition[] {
  const keepSeg = (id: string) => !f.segment || id === `seg:${f.segment}`;
  const segNodes = new Set(g.nodes.filter((n) => n.type === "segment" && keepSeg(n.id)).map((n) => n.id));
  const linkEdges = g.edges.filter((e) => e.type === "link" && segNodes.has(e.target));
  const portsInSeg = new Set(linkEdges.map((e) => e.source));
  const problemDevices = new Set<string>();
  for (const e of g.edges) {
    if (e.type === "path" && e.verdict && bad.has(e.verdict)) {
      problemDevices.add(e.source);
      problemDevices.add(e.target);
    }
  }
  for (const n of g.nodes) if (n.type === "device" && n.status && bad.has(n.status)) problemDevices.add(n.id);
  const devicesInSeg = new Set(
    g.nodes.filter((n) => n.type === "port" && portsInSeg.has(n.id) && n.parent).map((n) => n.parent as string),
  );
  const keepNode = (id: string, type: string, parent?: string): boolean => {
    switch (type) {
      case "segment":
        return segNodes.has(id);
      case "device":
        return (!f.segment || devicesInSeg.has(id)) && (!f.onlyProblems || problemDevices.has(id));
      case "port":
        return portsInSeg.has(id) && !!parent && keepNode(parent, "device");
      case "service":
        return f.services && !!parent && keepNode(parent, "device");
      default:
        return !f.onlyProblems;
    }
  };
  const kept = new Set<string>();
  const out: ElementDefinition[] = [];
  for (const n of g.nodes) {
    if (!keepNode(n.id, n.type, n.parent)) continue;
    kept.add(n.id);
    out.push({
      group: "nodes",
      data: { id: n.id, label: n.label, type: n.type, status: n.status ?? "", icon: n.icon ?? "", parent: n.parent },
      classes: `${n.type} s-${n.status ?? "none"}`,
      position: positions[n.id],
    });
  }
  for (const e of g.edges) {
    if (!kept.has(e.source) || !kept.has(e.target)) continue;
    if (e.type === "path" && !f.paths) continue;
    if (e.type === "path" && f.onlyProblems && !(e.verdict && bad.has(e.verdict))) continue;
    out.push({
      group: "edges",
      data: { id: e.id, source: e.source, target: e.target, label: e.label ?? "", verdict: e.verdict || "none" },
      classes: `${e.type} v-${e.verdict || "none"}`,
    });
  }
  return out;
}

/** Serialises the map filter into the URL hash query ("link to the map state"). */
export function filterToQuery(f: MapFilter): string {
  const p = new URLSearchParams();
  if (!f.paths) p.set("paths", "0");
  if (!f.services) p.set("services", "0");
  if (f.onlyProblems) p.set("problems", "1");
  if (f.segment) p.set("seg", f.segment);
  return p.toString();
}

export function queryToFilter(q: string): MapFilter {
  const p = new URLSearchParams(q);
  return {
    paths: p.get("paths") !== "0",
    services: p.get("services") !== "0",
    onlyProblems: p.get("problems") === "1",
    segment: p.get("seg") ?? "",
  };
}
