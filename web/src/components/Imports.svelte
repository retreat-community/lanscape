<script lang="ts">
  import { api, applyConfig, importTiles, type ConfigChange } from "../lib/api";
  import { t, toast } from "../lib/state.svelte";

  let prom = $state("");
  let ha = $state({ url: "", token: "" });
  let busy = $state(false);

  async function run(fn: () => Promise<string>): Promise<void> {
    busy = true;
    try {
      toast(await fn());
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    } finally {
      busy = false;
    }
  }

  // declarative configuration: preview with a dry run, then apply the same text
  let yaml = $state("");
  let prune = $state(false);
  let plan = $state<ConfigChange[] | null>(null);
  let planError = $state("");
  const changes = $derived((plan ?? []).filter((c) => c.action !== "unchanged"));

  async function configRun(dryRun: boolean): Promise<void> {
    busy = true;
    try {
      const r = await applyConfig(yaml, { dryRun, prune });
      plan = r.plan;
      planError = r.error ?? "";
      if (!dryRun && !r.error) {
        toast(t("imp.config_applied", { n: changes.length }));
        plan = null;
      }
    } catch (e) {
      planError = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function configFile(ev: Event): Promise<void> {
    const f = (ev.target as HTMLInputElement).files?.[0];
    if (f) {
      yaml = await f.text();
      await configRun(true);
    }
  }

  async function tiles(ev: Event): Promise<void> {
    const f = (ev.target as HTMLInputElement).files?.[0];
    if (!f) return;
    await run(async () => {
      const r = await importTiles(await f.text());
      return t("imp.tiles_done", { n: r.created, format: r.format, skipped: r.skipped.length });
    });
  }

  async function kuma(ev: Event): Promise<void> {
    const f = (ev.target as HTMLInputElement).files?.[0];
    if (!f) return;
    await run(async () => {
      const r = await api.importKuma(JSON.parse(await f.text()) as unknown);
      return t("imp.kuma_done", { n: r.created, skipped: r.skipped.length });
    });
  }
</script>

<section class="card form">
  <h2>{t("set.import")}</h2>
  <label class="field"
    >{t("imp.kuma")}
    <input type="file" accept="application/json,.json" disabled={busy} onchange={(e) => void kuma(e)} data-testid="import-kuma" />
  </label>
  <p class="muted small">{t("imp.kuma_hint")}</p>
  <label class="field"
    >{t("imp.tiles")}
    <input type="file" accept=".yaml,.yml,application/yaml" disabled={busy} onchange={(e) => void tiles(e)} />
  </label>
  <p class="muted small">{t("imp.tiles_hint")}</p>
  <div class="row">
    <label class="field grow">{t("imp.prometheus")} <input bind:value={prom} placeholder="http://prometheus:9090" /></label>
    <button
      disabled={busy || !prom}
      onclick={() => void run(async () => t("imp.items", { n: (await api.importPrometheus(prom)).items }))}>{t("imp.run")}</button
    >
  </div>
  <div class="row">
    <label class="field grow">{t("imp.ha")} <input bind:value={ha.url} placeholder="http://homeassistant.local:8123" /></label>
    <label class="field grow">{t("imp.ha_token")} <input type="password" bind:value={ha.token} autocomplete="off" /></label>
    <button
      disabled={busy || !ha.url || !ha.token}
      onclick={() => void run(async () => t("imp.items", { n: (await api.importHomeAssistant(ha.url, ha.token)).items }))}
      >{t("imp.run")}</button
    >
  </div>
  <p class="muted small">{t("imp.found_hint")}</p>
</section>

<section class="card form">
  <h2>{t("imp.config")}</h2>
  <p class="muted small">{t("imp.config_hint")}</p>
  <div class="row">
    <a class="button" href="/api/v1/config" download="lanscape.yaml">{t("imp.config_export")}</a>
    <input type="file" accept=".yaml,.yml,application/yaml" disabled={busy} onchange={(e) => void configFile(e)} />
  </div>
  <textarea rows="8" spellcheck="false" bind:value={yaml} placeholder="apiVersion: lanscape/v1"></textarea>
  <label><input type="checkbox" bind:checked={prune} /> {t("imp.config_prune")}</label>
  <div class="row">
    <button disabled={busy || !yaml} onclick={() => void configRun(true)}>{t("imp.config_preview")}</button>
    <button class="primary" disabled={busy || !plan || !!planError || changes.length === 0} onclick={() => void configRun(false)}>
      {t("imp.config_apply")}
    </button>
  </div>
  {#if planError}<p class="error">{planError}</p>{/if}
  {#if plan}
    {#if changes.length === 0}
      <p class="muted">{t("imp.config_nochange")}</p>
    {:else}
      <table class="small">
        <tbody>
          {#each changes as c (c.kind + c.name)}
            <tr><td><span class="tag">{c.action}</span></td><td>{c.kind}</td><td>{c.name}</td></tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}
</section>

<style>
  .form {
    display: grid;
    gap: 8px;
  }
  .grow {
    flex: 1;
  }
  textarea {
    width: 100%;
    font-family: ui-monospace, monospace;
  }
</style>
