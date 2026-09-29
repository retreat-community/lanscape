<script lang="ts">
  import { t } from "../lib/state.svelte";
  import type { Anomaly, Problem } from "../lib/types";

  let {
    problems,
    anomalies = [],
    names,
  }: { problems: Problem[]; anomalies?: Anomaly[]; names: (id: string) => string } = $props();
</script>

{#if !problems.length && !anomalies.length}
  <p class="muted">{t("net.no_problems")}</p>
{:else}
  <ul class="problems" data-testid="problems">
    {#each problems as p, i (i)}
      <li>
        <b>{t(`problem.${p.kind}`)}</b>
        <span class="tag">{p.seg_id}</span>
        <span class="muted">{names(p.src)} ({p.src_if}) → {names(p.dst)} ({p.dst_if})</span>
        <div>{p.detail}</div>
        <div class="muted small">{t(`hint.${p.kind}`)}</div>
      </li>
    {/each}
    {#each anomalies as a (a.kind + a.key)}
      <li>
        <b>{t(`anomaly.${a.kind}`)}</b>
        <span class="tag">{a.key}</span>
        <div class="muted">{a.detail}</div>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .problems {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .problems li {
    padding: 8px 0;
    border-bottom: 1px solid var(--line);
  }
  .problems li:last-child {
    border: 0;
  }
</style>
