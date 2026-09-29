<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import ReportView from "../components/ReportView.svelte";
  import { api, ApiError, subscribe } from "../lib/api";
  import { duration } from "../lib/format";
  import { can, t, toast, ui } from "../lib/state.svelte";
  import type { Progress, Report, RunOptions } from "../lib/types";

  let report = $state<Report | null>(null);
  let loading = $state(true);
  let agents = $state(0);
  let showOptions = $state(false);
  let opts = $state({ udp: false, bidir: false, duration: 5, streams: 4 });
  let estimate = $state<{ paths: number; seconds: number } | null>(null);

  async function load(): Promise<void> {
    try {
      const [last, active, list] = await Promise.all([api.lastRun(), api.activeRun(), api.agents()]);
      report = last && Array.isArray(last.paths) ? last : null;
      ui.progress = active;
      agents = list.filter((a) => a.online).length;
      estimate = await api.estimate("full", opts.udp, opts.bidir);
    } catch (e) {
      if (e instanceof ApiError) toast(e.message);
    } finally {
      loading = false;
    }
  }

  async function start(kind: RunOptions["kind"]): Promise<void> {
    try {
      const o: RunOptions = { kind, udp: opts.udp, bidir: opts.bidir, duration_ms: opts.duration * 1000, streams: opts.streams };
      const r = await api.startRun(o);
      ui.progress = { id: r.id, kind, done: 0, total: 1, eta_s: estimate?.seconds ?? 0 };
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  let unsub: (() => void) | undefined;
  onMount(() => {
    void load();
    unsub = subscribe({
      "run.started": (d) => (ui.progress = d as Progress),
      "run.progress": (d) => (ui.progress = d as Progress),
      "run.done": () => {
        ui.progress = null;
        void load();
      },
      agent: () => void load(),
    });
  });
  onDestroy(() => unsub?.());

  const pct = $derived(ui.progress ? Math.round((100 * ui.progress.done) / Math.max(1, ui.progress.total)) : 0);
</script>

<div class="grid">
  <section class="card">
    <div class="row">
      <button class="primary" data-testid="check-all" disabled={!!ui.progress || !can("operator")} onclick={() => start("full")}>
        {t("net.check_all")}
      </button>
      <button disabled={!!ui.progress || !can("operator")} onclick={() => start("reachability")}>{t("net.reachability")}</button>
      <button disabled={!!ui.progress || !can("operator")} onclick={() => start("aggregate")}>{t("net.aggregate")}</button>
      <button onclick={() => (showOptions = !showOptions)}>{t("net.options")}</button>
      {#if ui.progress}
        <button class="danger" disabled={!can("operator")} onclick={() => api.cancelRun()}>{t("net.cancel")}</button>
      {/if}
      <span class="spacer"></span>
      {#if estimate && !ui.progress}
        <span class="muted small">{t("net.estimate", { paths: estimate.paths, time: duration(estimate.seconds) })}</span>
      {/if}
    </div>
    {#if showOptions}
      <div class="row options">
        <label><input type="checkbox" bind:checked={opts.udp} /> {t("net.udp")}</label>
        <label><input type="checkbox" bind:checked={opts.bidir} /> {t("net.bidir")}</label>
        <label>{t("net.duration")} <input type="number" min="1" max="30" bind:value={opts.duration} style="width:5em" /></label>
        <label>{t("net.streams")} <input type="number" min="1" max="16" bind:value={opts.streams} style="width:5em" /></label>
      </div>
    {/if}
    {#if ui.progress}
      <div class="progress" data-testid="progress"><i style="width:{pct}%"></i></div>
      <div class="muted small">
        {t("net.running", { done: ui.progress.done, total: ui.progress.total, eta: duration(ui.progress.eta_s) })}
        {#if ui.progress.step}· {ui.progress.step}{/if}
      </div>
    {/if}
  </section>

  {#if loading}
    <p class="muted">{t("common.loading")}</p>
  {:else if report}
    <ReportView {report} />
  {:else}
    <section class="card">
      <p class="muted">{agents < 2 ? t("net.no_agents") : t("net.no_run")}</p>
    </section>
  {/if}
</div>

<style>
  .options {
    margin-top: 10px;
    gap: 16px;
  }
  .progress {
    height: 6px;
    background: var(--line);
    border-radius: 3px;
    overflow: hidden;
    margin: 12px 0 6px;
  }
  .progress i {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.3s;
  }
</style>
