<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { when } from "../lib/format";
  import { t, toast, ui } from "../lib/state.svelte";
  import type { SignatureInfo } from "../lib/types";

  const projectURL = "https://raw.githubusercontent.com/retreat-community/lanscape/main/internal/fingerprint/signatures.yaml";
  let info = $state<SignatureInfo | null>(null);
  let url = $state("");
  let custom = $state("");

  function take(i: SignatureInfo): void {
    info = i;
    url = i.url ?? "";
    custom = i.custom ?? "";
  }

  async function act(fn: () => Promise<SignatureInfo>, done: string): Promise<void> {
    try {
      take(await fn());
      toast(t(done));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  onMount(() => {
    api.signatures().then(take, (e: unknown) => toast(e instanceof Error ? e.message : String(e)));
  });
</script>

{#if info}
  <section class="card">
    <h2>{t("set.signatures")}</h2>
    <p class="muted small">{t("sig.hint", { n: info.builtin })}</p>
    <label class="field"
      >{t("sig.url")}
      <input bind:value={url} placeholder={projectURL} />
    </label>
    <div class="row">
      {#if !url}<button onclick={() => (url = projectURL)}>{t("sig.use_project")}</button>{/if}
      <button disabled={!info.url} onclick={() => act(() => api.updateSignatures(), "sig.updated")}>{t("sig.update_now")}</button>
      <span class="muted small">
        {info.fetched_at ? t("sig.fetched", { n: info.fetched_count, at: when(info.fetched_at, ui.lang) }) : ""}
      </span>
    </div>
    {#if info.fetch_error}<p class="error small">{info.fetch_error}</p>{/if}
    <label class="field"
      >{t("sig.custom", { n: info.custom_count })}
      <textarea rows="12" spellcheck="false" bind:value={custom} placeholder={"- id: myapp\n  name: My App\n  category: development\n  title: [\"^My App\"]\n  images: [\"me/myapp\"]\n  monitor: {type: http, path: /health}"}></textarea>
    </label>
    <button class="primary" onclick={() => act(() => api.saveSignatures(url, custom), "set.saved")}>{t("set.save")}</button>
  </section>
{/if}
