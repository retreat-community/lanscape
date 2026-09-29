<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { api, subscribe } from "../lib/api";
  import { latency, pct, statusClass, when } from "../lib/format";
  import { can, navigate, t, toast, ui } from "../lib/state.svelte";
  import type { DayStat, MonitorDetail } from "../lib/types";
  import type uPlot from "uplot";
  import IncidentItem from "./IncidentItem.svelte";
  import MonitorForm from "./MonitorForm.svelte";

  let { id }: { id: number } = $props();
  let d = $state<MonitorDetail | null>(null);
  let editing = $state(false);
  let chartEl = $state<HTMLDivElement | null>(null);
  let plot: uPlot | null = null;

  async function load(): Promise<void> {
    try {
      d = await api.monitor(id);
      await draw();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  async function draw(): Promise<void> {
    if (!d || !chartEl) return;
    const xs = d.checks.map((c) => c.ts / 1000);
    const ys = d.checks.map((c) => (c.status === "down" ? null : c.latency_ms));
    const data = [xs, ys] as uPlot.AlignedData;
    if (plot) {
      plot.setData(data);
      return;
    }
    const { default: UPlot } = await import("uplot");
    await import("uplot/dist/uPlot.min.css");
    const css = getComputedStyle(document.documentElement);
    plot = new UPlot(
      {
        width: chartEl.clientWidth || 600,
        height: 180,
        legend: { show: false },
        scales: { y: { range: (_u, _min, max) => [0, Math.max(10, max * 1.2)] } },
        axes: [
          { stroke: css.getPropertyValue("--muted"), grid: { stroke: css.getPropertyValue("--line") } },
          { stroke: css.getPropertyValue("--muted"), grid: { stroke: css.getPropertyValue("--line") }, values: (_u, v) => v.map((x) => `${x} ms`) },
        ],
        series: [{}, { stroke: css.getPropertyValue("--accent"), width: 1.5, spanGaps: false }],
      },
      data,
      chartEl,
    );
  }

  let pending: ReturnType<typeof setTimeout> | undefined;
  onMount(() => {
    void load();
    const off = subscribe({
      monitor: (e) => {
        if ((e as { id: number }).id === id && !pending) pending = setTimeout(() => ((pending = undefined), void load()), 1000);
      },
      incident: () => void load(),
    });
    return off;
  });
  onDestroy(() => {
    plot?.destroy();
    clearTimeout(pending);
  });

  function dayClass(s: DayStat | undefined): string {
    if (!s) return "v-none";
    const total = s.up + s.degraded + s.down;
    if (total === 0) return s.maint ? "v-purple" : "v-none";
    const up = (s.up + s.degraded) / total;
    return up >= 0.999 ? "v-green" : up >= 0.95 ? "v-yellow" : "v-red";
  }

  // 90 day slots ending today (local time)
  const days = $derived.by(() => {
    const byDay = new Map((d?.days ?? []).map((x) => [x.day, x]));
    const out: { key: number; stat?: DayStat; label: string }[] = [];
    for (let i = 89; i >= 0; i--) {
      const dt = new Date(Date.now() - i * 86400000);
      const key = dt.getFullYear() * 10000 + (dt.getMonth() + 1) * 100 + dt.getDate();
      out.push({ key, stat: byDay.get(key), label: dt.toLocaleDateString(ui.lang === "ru" ? "ru-RU" : "en-GB") });
    }
    return out;
  });

  async function act(fn: () => Promise<unknown>): Promise<void> {
    try {
      await fn();
      await load();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }
</script>

{#if d}
  {@const m = d.monitor}
  {#if editing}
    <MonitorForm monitor={m} onsaved={() => ((editing = false), void load())} oncancel={() => (editing = false)} />
  {:else}
    <section class="card">
      <div class="row">
        <span class="dot {statusClass(m.status)}"></span>
        <h2 data-testid="monitor-title">{m.name}</h2>
        <span class="tag" data-testid="monitor-status">{t(`mon.status.${m.status}`)}</span>
        <span class="spacer"></span>
        {#if can("operator")}
          <button onclick={() => void act(() => api.checkNow(id))}>{t("mon.check_now")}</button>
          <button
            onclick={() =>
              void act(() =>
                api.saveMonitor({ ...m, spec: m.spec as unknown as Record<string, unknown>, enabled: !m.enabled }),
              )}>{m.enabled ? t("mon.pause") : t("mon.resume")}</button
          >
          <button onclick={() => (editing = true)}>{t("common.edit")}</button>
          <button
            class="danger"
            onclick={() =>
              confirm(t("set.confirm_delete", { name: m.name })) &&
              void act(async () => {
                await api.deleteMonitor(id);
                navigate("/monitors");
              })}>{t("common.delete")}</button
          >
        {/if}
      </div>
      <div class="muted small">
        {m.spec.type.toUpperCase()} {m.spec.target} · {m.interval_s}s ·
        {t("mon.last_check", { when: when(m.last_check, ui.lang) })}
        {#if m.points.length}· {m.points.join(", ")}{/if}
      </div>
      {#if m.last_message}<div class="small">{m.last_message}</div>{/if}
      {#if m.cert_not_after}
        <div class="small muted">{t("mon.cert", { date: when(m.cert_not_after, ui.lang) })}</div>
      {/if}
      <div class="stats">
        <div><span class="muted small">{t("mon.response")}</span><b>{latency(m.last_latency)}</b></div>
        <div><span class="muted small">{t("mon.day")}</span><b>{pct(d.uptime.day)}</b></div>
        <div><span class="muted small">{t("mon.week")}</span><b>{pct(d.uptime.week)}</b></div>
        <div><span class="muted small">{t("mon.month")}</span><b>{pct(d.uptime.month)}</b></div>
        <div><span class="muted small">{t("mon.year")}</span><b>{pct(d.uptime.year)}</b></div>
      </div>
    </section>
  {/if}

  <section class="card">
    <h3>{t("mon.response")}</h3>
    <div bind:this={chartEl} class="chart"></div>
  </section>

  <section class="card">
    <h3>{t("mon.history")}</h3>
    <div class="bars">
      {#each days as day (day.key)}
        <span
          class="bar bg-v {dayClass(day.stat)}"
          title="{day.label}: {day.stat ? pct(((day.stat.up + day.stat.degraded) * 100) / Math.max(1, day.stat.up + day.stat.degraded + day.stat.down)) : '—'}"
        ></span>
      {/each}
    </div>
  </section>

  <section class="card">
    <h3>{t("mon.incidents")}</h3>
    {#each d.incidents as i (i.id)}
      <IncidentItem incident={{ ...i, monitor: m.name }} onchange={load} />
    {:else}
      <p class="muted">{t("inc.none")}</p>
    {/each}
  </section>
{:else}
  <p class="muted">{t("common.loading")}</p>
{/if}

<style>
  .stats {
    display: flex;
    gap: 24px;
    flex-wrap: wrap;
    margin-top: 10px;
  }
  .stats div {
    display: flex;
    flex-direction: column;
  }
  .chart {
    width: 100%;
    min-height: 180px;
  }
  .bars {
    display: flex;
    gap: 2px;
    height: 32px;
  }
  .bar {
    flex: 1;
    border-radius: 2px;
    min-width: 2px;
  }
  h2 {
    margin: 0;
  }
</style>
