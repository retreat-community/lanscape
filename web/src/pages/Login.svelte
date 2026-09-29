<script lang="ts">
  import { api, ApiError } from "../lib/api";
  import { t, ui } from "../lib/state.svelte";

  let { setup = false, onauth }: { setup?: boolean; onauth: () => void } = $props();
  let username = $state("admin");
  let password = $state("");
  let code = $state("");
  let needCode = $state(false);
  let error = $state("");
  let busy = $state(false);
  let sso = $state<{ enabled: boolean; name: string } | null>(null);

  $effect(() => {
    if (setup) return;
    api.oidcInfo().then((i) => (sso = i.enabled ? i : null), () => {});
    // the single sign-on callback returns here with ?error=… on failure
    const q = new URLSearchParams(location.hash.split("?")[1] ?? "");
    if (q.get("error")) error = q.get("error") ?? "";
  });

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    busy = true;
    error = "";
    try {
      ui.user = setup ? await api.setup(username, password) : await api.login(username, password, code || undefined);
      onauth();
    } catch (err) {
      if (err instanceof ApiError && err.body.totp_required) needCode = true;
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="center">
  <form class="card" onsubmit={submit}>
    <h1>{setup ? t("setup.title") : t("login.title")}</h1>
    {#if setup}<p class="muted">{t("setup.hint")}</p>{/if}
    <label class="field">{t("login.username")}<input name="username" autocomplete="username" bind:value={username} required /></label>
    <label class="field">
      {t("login.password")}
      <input
        name="password"
        type="password"
        autocomplete={setup ? "new-password" : "current-password"}
        minlength={setup ? 10 : undefined}
        bind:value={password}
        required
      />
      {#if setup}<span class="muted small">{t("setup.password_hint")}</span>{/if}
    </label>
    {#if needCode}
      <label class="field">{t("login.code")}<input name="code" inputmode="numeric" autocomplete="one-time-code" bind:value={code} /></label>
    {/if}
    {#if error}<p class="error">{error}</p>{/if}
    <button class="primary" type="submit" disabled={busy}>{setup ? t("setup.submit") : t("login.submit")}</button>
    {#if sso}
      <a class="button sso" href="/api/v1/auth/oidc/login">{t("login.sso", { name: sso.name })}</a>
    {/if}
  </form>
</div>

<style>
  .center {
    min-height: 80vh;
    display: grid;
    place-items: center;
  }
  form {
    width: min(360px, 92vw);
  }
  input {
    width: 100%;
  }
  .sso {
    display: block;
    margin-top: 10px;
    text-align: center;
  }
</style>
