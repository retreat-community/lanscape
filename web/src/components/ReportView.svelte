<script lang="ts">
  import { rate, when } from "../lib/format";
  import { nameMap } from "../lib/matrix";
  import { t, ui } from "../lib/state.svelte";
  import type { Report } from "../lib/types";
  import Matrix from "./Matrix.svelte";
  import Problems from "./Problems.svelte";

  let { report }: { report: Report } = $props();
  const names = $derived(nameMap(report.nodes));
</script>

<section class="card">
  <div class="row">
    <h2>{t("net.problems")}</h2>
    <span class="spacer"></span>
    <span class="muted small">{t("net.last_run", { id: report.id, when: when(report.finished, ui.lang) })} · {report.kind}</span>
  </div>
  <Problems problems={report.problems ?? []} anomalies={report.anomalies ?? []} {names} />
</section>

{#if report.aggregate?.length}
  <section class="card">
    <h2>{t("net.aggregate_result")}</h2>
    <table>
      <tbody>
        {#each report.aggregate as a (a.seg_id)}
          <tr>
            <th>{a.seg_id}</th>
            <td>{t("net.total")}: <b>{rate(a.total_bps)}</b> ({a.pairs})</td>
            <td class="small">
              {#each Object.entries(a.node_out_bps) as [n, v] (n)}
                <span class="tag">{names(n)} ↑ {rate(v)} ↓ {rate(a.node_in_bps[n] ?? 0)}</span>
              {/each}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>
{/if}

<section class="card">
  <h2>{t("net.matrix")}</h2>
  <Matrix {report} {names} />
  <div class="legend row small">
    {#each ["green", "yellow", "red", "purple", "none"] as v (v)}
      <span class="v-{v}"><span class="dot"></span>{t(`verdict.${v}`)}</span>
    {/each}
  </div>
</section>

<style>
  .legend {
    margin-top: 8px;
    gap: 14px;
  }
</style>
