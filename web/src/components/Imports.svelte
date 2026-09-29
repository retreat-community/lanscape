<script lang="ts">
  import { api } from "../lib/api";
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

<style>
  .form {
    display: grid;
    gap: 8px;
  }
  .grow {
    flex: 1;
  }
</style>
