<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import NetMap from "../components/NetMap.svelte";
  import { api, subscribe } from "../lib/api";
  import { filterToQuery, queryToFilter, type MapFilter } from "../lib/mapview";
  import { can, t, toast } from "../lib/state.svelte";
  import type { MapGraph, MapNode } from "../lib/types";

  let graph = $state<MapGraph | null>(null);
  const q = location.hash.split("?")[1] ?? "";
  let filter = $state<MapFilter>(queryToFilter(q));
  let wall = $state(new URLSearchParams(q).get("wall") === "1");
  let selected = $state<MapNode | null>(null);
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
        <NetMap bind:this={mapRef} {graph} {filter} onselect={(id) => (selected = graph?.nodes.find((n) => n.id === id) ?? null)} />
      {:else}
        <p class="muted">{t("common.loading")}</p>
      {/if}
    </div>
    {#if selected || graph?.hypotheses.length}
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
  @media (max-width: 800px) {
    .body {
      flex-direction: column;
    }
    .side {
      width: auto;
    }
  }
</style>
