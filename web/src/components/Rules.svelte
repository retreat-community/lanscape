<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { can, t, toast } from "../lib/state.svelte";
  import type { Rule } from "../lib/types";

  let rules = $state<Rule[]>([]);

  onMount(async () => {
    try {
      rules = await api.rules();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  });

  async function save(): Promise<void> {
    try {
      rules = await api.saveRules(rules.map((r) => ({ ...r, name: r.name.trim() || r.kind || r.app || "rule" })));
      toast(t("set.saved"));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  function move(i: number, d: number): void {
    const j = i + d;
    if (j < 0 || j >= rules.length) return;
    const r = [...rules];
    [r[i], r[j]] = [r[j], r[i]];
    rules = r;
  }
</script>

<section class="card">
  <h2>{t("set.discovery")}</h2>
  <p class="muted small">{t("rule.hint")}</p>
  {#if rules.length === 0}<p class="muted">{t("rule.none")}</p>{/if}
  <table>
    <thead>
      <tr>
        <th>{t("set.name")}</th><th>{t("rule.kind")}</th><th>{t("rule.app")}</th><th>{t("rule.category")}</th>
        <th>{t("rule.tls_only")}</th><th>{t("rule.action")}</th><th></th><th></th>
      </tr>
    </thead>
    <tbody>
      {#each rules as r, i (i)}
        <tr>
          <td><input bind:value={r.name} /></td>
          <td>
            <select bind:value={r.kind}>
              <option value="">{t("rule.any")}</option>
              {#each ["ingress", "k8s", "container", "socket"] as k (k)}<option>{k}</option>{/each}
            </select>
          </td>
          <td><input bind:value={r.app} placeholder="*" /></td>
          <td><input bind:value={r.category} placeholder="media" /></td>
          <td><input type="checkbox" bind:checked={r.tls_only} /></td>
          <td>
            <select bind:value={r.action}>
              <option value="add">{t("rule.add")}</option>
              <option value="ignore">{t("rule.ignore")}</option>
            </select>
          </td>
          <td class="small">
            {#if r.action === "add"}
              <label><input type="checkbox" bind:checked={r.monitor} /> {t("svc.with_monitor")}</label>
              <label><input type="checkbox" bind:checked={r.tile} /> {t("svc.with_tile")}</label>
              <input bind:value={r.group} placeholder={t("svc.group")} />
            {/if}
          </td>
          <td class="nowrap">
            <button onclick={() => move(i, -1)}>↑</button>
            <button onclick={() => move(i, 1)}>↓</button>
            <button class="danger" onclick={() => (rules = rules.filter((_, j) => j !== i))}>✕</button>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
  {#if can("admin")}
    <div class="row">
      <button onclick={() => (rules = [...rules, { name: "", kind: "", action: "add", monitor: true, tile: true }])}>{t("set.add")}</button>
      <button class="primary" onclick={() => void save()}>{t("common.save")}</button>
    </div>
  {/if}
</section>

<style>
  .nowrap {
    white-space: nowrap;
  }
</style>
