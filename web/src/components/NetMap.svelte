<script lang="ts">
  import cytoscape, { type Core } from "cytoscape";
  import elk from "cytoscape-elk";
  import { onDestroy, onMount } from "svelte";
  import { elements, type MapFilter } from "../lib/mapview";
  import type { MapGraph } from "../lib/types";

  cytoscape.use(elk);

  let {
    graph,
    filter,
    onselect,
  }: { graph: MapGraph; filter: MapFilter; onselect: (id: string, type: string) => void } = $props();

  let el: HTMLDivElement;
  let cy: Core | null = null;
  const posKey = "lanscape-map-positions";

  function loadPositions(): Record<string, { x: number; y: number }> {
    try {
      return JSON.parse(localStorage.getItem(posKey) ?? "{}") as Record<string, { x: number; y: number }>;
    } catch {
      return {};
    }
  }

  function savePositions(): void {
    if (!cy) return;
    const pos = loadPositions();
    cy.nodes(":childless").forEach((n) => {
      pos[n.id()] = n.position();
    });
    try {
      localStorage.setItem(posKey, JSON.stringify(pos));
    } catch {
      // not persisted in private mode
    }
  }

  const colors = () => {
    const s = getComputedStyle(document.documentElement);
    const v = (n: string) => s.getPropertyValue(n).trim();
    return {
      fg: v("--fg"),
      muted: v("--muted"),
      card: v("--card"),
      line: v("--line"),
      accent: v("--accent"),
      green: v("--green"),
      yellow: v("--yellow"),
      red: v("--red"),
      purple: v("--purple"),
      none: v("--none"),
    };
  };

  function style(): cytoscape.StylesheetJson {
    const c = colors();
    const vc: Record<string, string> = { green: c.green, yellow: c.yellow, red: c.red, purple: c.purple, none: c.none };
    return [
      { selector: "node", style: { label: "data(label)", color: c.fg, "font-size": 11, "text-wrap": "wrap" } },
      {
        selector: "node.device",
        style: {
          shape: "round-rectangle",
          "background-color": c.card,
          "border-width": 2,
          "border-color": c.line,
          "text-valign": "top",
          "text-halign": "center",
          "font-weight": "bold",
          padding: "10px",
        },
      },
      ...Object.entries(vc).map(([k, col]) => ({ selector: `node.device.s-${k}`, style: { "border-color": col } })),
      { selector: "node.device.s-offline", style: { "border-style": "dashed", opacity: 0.6 } },
      {
        selector: "node.port",
        style: { shape: "round-rectangle", width: 90, height: 18, "background-color": c.line, "font-size": 9, "text-valign": "center" },
      },
      {
        selector: "node.segment",
        style: {
          shape: "round-rectangle",
          width: 260,
          height: 14,
          "background-color": c.accent,
          "text-valign": "top",
          "font-weight": "bold",
        },
      },
      { selector: "node.service", style: { shape: "ellipse", width: 16, height: 16, "font-size": 9, "background-color": c.none } },
      ...Object.entries(vc).map(([k, col]) => ({ selector: `node.service.s-${k}`, style: { "background-color": col } })),
      { selector: "edge", style: { width: 2, "curve-style": "bezier", "line-color": c.none } },
      ...Object.entries(vc).map(([k, col]) => ({ selector: `edge.v-${k}`, style: { "line-color": col } })),
      { selector: "edge.link.v-none", style: { "line-color": c.line } },
      {
        selector: "edge.path",
        style: { "line-style": "dashed", label: "data(label)", "font-size": 9, color: c.muted, "text-background-opacity": 0.8, "text-background-color": c.card },
      },
      { selector: ":selected", style: { "overlay-opacity": 0.15, "overlay-color": c.accent } },
    ] as cytoscape.StylesheetJson;
  }

  function render(relayout = false): void {
    if (!cy) return;
    const pos = relayout ? {} : loadPositions();
    cy.elements().remove();
    cy.add(elements(graph, filter, pos));
    cy.style(style());
    const missing = cy.nodes(":childless").filter((n) => !pos[n.id()]).length > 0;
    if (missing || relayout) {
      cy.layout({
        name: "elk",
        nodeDimensionsIncludeLabels: true,
        elk: { algorithm: "layered", "elk.direction": "UP", "elk.spacing.nodeNode": 30, "elk.layered.spacing.nodeNodeBetweenLayers": 60 },
      } as cytoscape.LayoutOptions).run();
    } else {
      cy.fit(undefined, 30);
    }
  }

  export function relayout(): void {
    try {
      localStorage.removeItem(posKey);
    } catch {
      // ignore
    }
    render(true);
  }

  export function png(): string {
    return cy ? cy.png({ full: true, scale: 2, bg: colors().card }) : "";
  }

  /** Minimal SVG export built from the current positions. */
  export function svg(): string {
    if (!cy) return "";
    const c = colors();
    const bb = cy.elements().boundingBox();
    const parts: string[] = [
      `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${bb.x1 - 20} ${bb.y1 - 20} ${bb.w + 40} ${bb.h + 40}" font-family="sans-serif">`,
    ];
    const esc = (s: string) => s.replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[ch]!);
    cy.edges().forEach((e) => {
      const s = e.sourceEndpoint();
      const t = e.targetEndpoint();
      parts.push(
        `<line x1="${s.x}" y1="${s.y}" x2="${t.x}" y2="${t.y}" stroke="${e.style("line-color")}" stroke-width="2"${e.hasClass("path") ? ' stroke-dasharray="5 4"' : ""}/>`,
      );
    });
    cy.nodes().forEach((n) => {
      const b = n.boundingBox({ includeLabels: false });
      parts.push(
        `<rect x="${b.x1}" y="${b.y1}" width="${b.w}" height="${b.h}" rx="6" fill="${n.style("background-color")}" stroke="${n.style("border-color")}"/>`,
        `<text x="${(b.x1 + b.x2) / 2}" y="${n.isParent() ? b.y1 - 4 : (b.y1 + b.y2) / 2 + 4}" font-size="10" text-anchor="middle" fill="${c.fg}">${esc(String(n.data("label")))}</text>`,
      );
    });
    parts.push("</svg>");
    return parts.join("\n");
  }

  onMount(() => {
    cy = cytoscape({ container: el, wheelSensitivity: 0.3, minZoom: 0.1, maxZoom: 3 });
    cy.on("tap", "node", (e) => onselect(e.target.id(), String(e.target.data("type"))));
    cy.on("tap", "edge.path", (e) => onselect(e.target.id(), "path"));
    cy.on("dragfree", "node", savePositions);
    render();
  });

  onDestroy(() => cy?.destroy());

  $effect(() => {
    // re-render when the graph or the filter changes
    void graph;
    void filter.paths;
    void filter.services;
    void filter.onlyProblems;
    void filter.segment;
    render();
  });
</script>

<div class="map" bind:this={el} data-testid="map"></div>

<style>
  .map {
    width: 100%;
    height: 100%;
    min-height: 480px;
  }
</style>
