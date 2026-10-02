<script lang="ts">
  import { wallets } from "../lib/donate";
  import { t, toast } from "../lib/state.svelte";

  function copy(address: string): void {
    void navigator.clipboard?.writeText(address).then(
      () => toast(t("common.copied")),
      () => toast(address),
    );
  }
</script>

<div class="grid">
  <section class="card">
    <h2>{t("donate.title")}</h2>
    <p>{t("donate.text")}</p>
    <p class="muted small">{t("donate.network_hint")}</p>
  </section>
  {#each wallets as w (w.address)}
    <section class="card wallet">
      <div class="row">
        <b>{w.network}</b>
        {#each w.assets as a (a)}<span class="tag">{a}</span>{/each}
      </div>
      <div class="row">
        <code>{w.address}</code>
        <button onclick={() => copy(w.address)}>{t("common.copy")}</button>
      </div>
    </section>
  {/each}
</div>

<style>
  .wallet code {
    word-break: break-all;
    font-size: 13px;
  }
  .wallet .row {
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .wallet .row + .row {
    margin-top: 8px;
  }
</style>
