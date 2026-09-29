<script lang="ts">
  import { onMount } from "svelte";
  import ReportView from "../components/ReportView.svelte";
  import { api } from "../lib/api";
  import { duration, when } from "../lib/format";
  import { navigate, t, toast, ui } from "../lib/state.svelte";
  import type { Report, RunSummary } from "../lib/types";

  let { id = 0 }: { id?: number } = $props();
  let runs = $state<RunSummary[]>([]);
  let report = $state<Report | null>(null);

  $effect(() => {
    if (id) {
      api
        .run(id)
        .then((r) => (report = Array.isArray(r.paths) ? r : null))
        .catch((e: unknown) => toast(e instanceof Error ? e.message : String(e)));
    } else {
      report = null;
    }
  });

  onMount(async () => {
    try {
      runs = await api.runs(100);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  });
</script>

<div class="grid">
  {#if report}
    <div><button onclick={() => navigate("/runs")}>← {t("runs.title")}</button></div>
    <ReportView {report} />
  {:else}
    <section class="card">
      <h2>{t("runs.title")}</h2>
      <table>
        <thead>
          <tr><th>#</th><th>{t("runs.kind")}</th><th>{t("runs.status")}</th><th>{t("runs.started")}</th><th>{t("runs.duration")}</th><th></th></tr>
        </thead>
        <tbody>
          {#each runs as r (r.id)}
            <tr>
              <td>{r.id}</td>
              <td>{r.kind}</td>
              <td>{r.status}</td>
              <td>{when(r.started, ui.lang)}</td>
              <td>{r.finished ? duration((r.finished - r.started) / 1000) : "—"}</td>
              <td><button onclick={() => navigate(`/runs/${r.id}`)}>{t("runs.open")}</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}
</div>
