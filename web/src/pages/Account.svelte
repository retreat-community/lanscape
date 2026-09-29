<script lang="ts">
  import { api } from "../lib/api";
  import { t, toast, ui } from "../lib/state.svelte";

  let oldPw = $state("");
  let newPw = $state("");
  let enrol = $state<{ secret: string; url: string } | null>(null);
  let code = $state("");

  async function run(fn: () => Promise<unknown>): Promise<void> {
    try {
      await fn();
      toast(t("set.saved"));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  async function refresh(): Promise<void> {
    ui.user = (await api.me()).user;
  }
</script>

<div class="grid narrow">
  <section class="card">
    <h2>{t("acc.password")}</h2>
    <label class="field">{t("acc.old")}<input type="password" autocomplete="current-password" bind:value={oldPw} /></label>
    <label class="field">{t("acc.new")}<input type="password" autocomplete="new-password" minlength="10" bind:value={newPw} /></label>
    <button class="primary" onclick={() => run(() => api.changePassword(oldPw, newPw))}>{t("set.save")}</button>
  </section>
  <section class="card">
    <h2>{t("acc.totp")}: {ui.user?.totp_enabled ? t("acc.totp_on") : t("acc.totp_off")}</h2>
    {#if ui.user?.totp_enabled}
      <button class="danger" onclick={() => run(async () => { await api.totpDisable(); await refresh(); })}>{t("acc.totp_disable")}</button>
    {:else if enrol}
      <p>{t("acc.totp_scan")}</p>
      <pre>{enrol.secret}</pre>
      <p class="small muted">{enrol.url}</p>
      <div class="row">
        <input inputmode="numeric" bind:value={code} placeholder="123456" />
        <button class="primary" onclick={() => run(async () => { await api.totpConfirm(enrol!.secret, code); enrol = null; await refresh(); })}>
          {t("acc.confirm")}
        </button>
      </div>
    {:else}
      <button onclick={async () => (enrol = await api.totpStart())}>{t("acc.totp_enable")}</button>
    {/if}
  </section>
</div>

<style>
  .narrow {
    max-width: 560px;
  }
</style>
