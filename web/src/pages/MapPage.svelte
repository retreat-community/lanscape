<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import NetMap from "../components/NetMap.svelte";
  import { api, subscribe } from "../lib/api";
  import { filterToQuery, queryToFilter, type MapFilter } from "../lib/mapview";
  import { can, t, toast } from "../lib/state.svelte";
  import { rate, when } from "../lib/format";
  import type { MapEdge, MapGraph, MapNode, PathPoint } from "../lib/types";

  let graph = $state<MapGraph | null>(null);
  const q = location.hash.split("?")[1] ?? "";
  let filter = $state<MapFilter>(queryToFilter(q));
  let wall = $state(new URLSearchParams(q).get("wall") === "1");
  let selected = $state<MapNode | null>(null);
  let selEdge = $state<MapEdge | null>(null);
  let hist = $state<PathPoint[]>([]);

  async function select(id: string, type: string): Promise<void> {
    if (type !== "path") {
      selEdge = null;
      selected = graph?.nodes.find((n) => n.id === id) ?? null;
      return;
    }
    selected = null;
    selEdge = graph?.edges.find((e) => e.id === id) ?? null;
    hist = [];
    if (!selEdge) return;
    try {
      hist = await api.pathHistory(selEdge.source.slice(4), selEdge.target.slice(4), selEdge.segment ?? "");
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  const nodeName = (id: string) => graph?.nodes.find((n) => n.id === id)?.label ?? id;
  const histMax = $derived(Math.max(1, ...hist.map((p) => p.best_bps)));
  let mapRef = $state<ReturnType<typeof NetMap> | null>(null);
  let box: HTMLDivElement;

  const segments = $derived(graph ? graph.nodes.filter((n) => n.type === "segment") : []);

  async function load(): Promise<void> {
    try {
      graph = await api.map();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  $effect(() => {
    const qs = filterToQuery(filter);
    const url = `#/map${qs || wall ? "?" : ""}${qs}${wall ? `${qs ? "&" : ""}wall=1` : ""}`;
    history.replaceState(null, "", url);
  });

  function download(name: string, href: string): void {
    const a = document.createElement("a");
    a.href = href;
    a.download = name;
    a.click();
  }

  function exportSvg(): void {
    const s = mapRef?.svg() ?? "";
    download("lanscape-map.svg", URL.createObjectURL(new Blob([s], { type: "image/svg+xml" })));
  }

  async function toggleWall(): Promise<void> {
    wall = !wall;
    if (wall && box.requestFullscreen) await box.requestFullscreen().catch(() => undefined);
    else if (!wall && document.fullscreenElement) await document.exitFullscreen();
  }

  async function remeasure(n: MapNode): Promise<void> {
    const id = String(n.data?.agent ?? "");
    try {
      await api.startRun({ kind: "full", nodes: [id] });
      toast(t("net.running", { done: 0, total: "…", eta: "…" }));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  let unsub: (() => void) | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;
  onMount(() => {
    void load();
    unsub = subscribe({ "run.done": () => void load(), agent: () => void load() });
    timer = setInterval(() => wall && void load(), 30000);
  });
  onDestroy(() => {
    unsub?.();
    clearInterval(timer);
  });
</script>

<div class="page" class:wall bind:this={box}>
  <div class="card toolbar row">
    <b>{t("map.layers")}:</b>
    <label><input type="checkbox" checked disabled /> {t("map.layer.physical")}</label>
    <label><input type="checkbox" bind:checked={filter.paths} /> {t("map.layer.paths")}</label>
    <label><input type="checkbox" bind:checked={filter.services} /> {t("map.layer.services")}</label>
    <label><input type="checkbox" bind:checked={filter.onlyProblems} /> {t("map.only_problems")}</label>
    <label>
      {t("map.segment")}
      <select bind:value={filter.segment}>
        <option value="">{t("map.all")}</option>
        {#each segments as s (s.id)}
          <option value={s.id.slice(4)}>{s.id.slice(4)}</option>
        {/each}
      </select>
    </label>
    <span class="spacer"></span>
    <button onclick={() => mapRef?.relayout()}>{t("map.relayout")}</button>
    <button onclick={() => download("lanscape-map.png", mapRef?.png() ?? "")}>{t("map.export_png")}</button>
    <button onclick={exportSvg}>{t("map.export_svg")}</button>
    <button onclick={toggleWall}>{t("map.wall")}</button>
  </div>
  <div class="body">
    <div class="card canvas">
      {#if graph}
        <NetMap bind:this={mapRef} {graph} {filter} onselect={(id, type) => void select(id, type)} />
      {:else}
        <p class="muted">{t("common.loading")}</p>
      {/if}
    </div>
    {#if selected || selEdge || graph?.hypotheses.length}
      <aside class="card side">
        {#if selected}
          <h2>{selected.label}</h2>
          <div class="muted small">{selected.type}{selected.status ? ` · ${selected.status}` : ""}</div>
          <table class="small">
            <tbody>
              {#each Object.entries(selected.data ?? {}) as [k, v] (k)}
                <tr><th>{k}</th><td>{String(v)}</td></tr>
              {/each}
            </tbody>
          </table>
          {#if selected.type === "device" && can("operator")}
            <button onclick={() => selected && remeasure(selected)}>{t("map.remeasure")}</button>
          {/if}
          {#if typeof selected.data?.url === "string"}
            <a href={String(selected.data.url)} target="_blank" rel="noopener">{selected.data.url}</a>
          {/if}
        {/if}
        {#if selEdge}
          <h2>{nodeName(selEdge.source)} ↔ {nodeName(selEdge.target)}</h2>
          <div class="muted small">{selEdge.segment} · {selEdge.label ?? ""}</div>
          <h3>{t("map.history")}</h3>
          {#if hist.length}
            <svg class="spark" viewBox="0 0 {hist.length * 12} 60" preserveAspectRatio="none" role="img" aria-label={t("map.history")}>
              {#each hist as p, i (p.run_id + p.src_if + p.dst_if)}
                <rect
                  x={i * 12 + 1}
                  width="10"
                  y={60 - Math.max(2, (p.best_bps / histMax) * 58)}
                  height={Math.max(2, (p.best_bps / histMax) * 58)}
                  class="bar-{p.verdict || 'none'}"
                >
                  <title>#{p.run_id} {when(p.finished, "en")}: {rate(p.best_bps)} {p.status ?? ""}</title>
                </rect>
              {/each}
            </svg>
            <table class="small">
              <tbody>
                {#each hist.slice(-8).reverse() as p (p.run_id + p.src_if + p.dst_if)}
                  <tr>
                    <td><a href="#/runs/{p.run_id}">#{p.run_id}</a></td>
                    <td><span class="dot v-{p.verdict || 'none'}"></span>{rate(p.best_bps)}</td>
                    <td class="muted">{p.src_if}→{p.dst_if}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {:else}
            <p class="muted small">{t("common.loading")}</p>
          {/if}
        {/if}
        {#if graph?.hypotheses.length}
          <h3>{t("map.hypotheses")}</h3>
          <ul class="small">
            {#each graph.hypotheses as h (h.id)}
              <li>{h.detail}</li>
            {/each}
          </ul>
        {/if}
      </aside>
    {/if}
  </div>
</div>

<style>
  .page {
    display: grid;
    gap: 12px;
  }
  .page.wall {
    position: fixed;
    inset: 0;
    z-index: 10;
    background: var(--bg);
    padding: 8px;
  }
  .toolbar {
    gap: 12px;
  }
  .body {
    display: flex;
    gap: 12px;
    min-height: 70vh;
  }
  .wall .body {
    min-height: calc(100vh - 80px);
  }
  .canvas {
    flex: 1;
    padding: 0;
    min-height: 480px;
  }
  .side {
    width: 300px;
    flex: none;
  }
  .spark {
    width: 100%;
    height: 60px;
  }
  .spark rect {
    fill: var(--none);
  }
  .spark .bar-green {
    fill: var(--green);
  }
  .spark .bar-yellow {
    fill: var(--yellow);
  }
  .spark .bar-red {
    fill: var(--red);
  }
  .spark .bar-purple {
    fill: var(--purple);
  }
  @media (max-width: 800px) {
    .body {
      flex-direction: column;
    }
    .side {
      width: auto;
    }
  }
</style>
