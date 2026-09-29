<script lang="ts">
  import { onMount } from "svelte";
  import { api, subscribe } from "../lib/api";
  import { when } from "../lib/format";
  import { t, toast, ui } from "../lib/state.svelte";
  import type { Change } from "../lib/types";

  let changes = $state<Change[]>([]);
  let more = $state(true);
  let kind = $state("");

  const kinds = $derived([...new Set(changes.map((c) => c.kind))].sort());
  const shown = $derived(kind ? changes.filter((c) => c.kind === kind) : changes);

  async function page(): Promise<void> {
    try {
      const before = changes.length ? changes[changes.length - 1].id : 0;
      const next = await api.changes(before, 100);
      changes = [...changes, ...next];
      more = next.length === 100;
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  onMount(() => {
    void page();
    return subscribe({
      change: (c) => {
        const ch = c as Change;
        if (!changes.some((x) => x.id === ch.id)) changes = [ch, ...changes];
      },
    });
  });
</script>

<div class="grid">
  <div class="tabs">
    <button class:on={kind === ""} onclick={() => (kind = "")}>{t("common.all")}</button>
    {#each kinds as k (k)}
      <button class:on={kind === k} onclick={() => (kind = k)}>{t(`chg.${k}`)}</button>
    {/each}
  </div>
  <section class="card">
    {#if shown.length === 0}
      <p class="muted">{t("chg.none")}</p>
    {:else}
      <table>
        <tbody>
          {#each shown as c (c.id)}
            <tr data-testid="change">
              <td class="small muted nowrap">{when(c.ts, ui.lang)}</td>
              <td><span class="tag">{t(`chg.${c.kind}`)}</span></td>
              <td><b>{c.subject}</b></td>
              <td class="small muted">{c.detail ?? ""}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    {#if more && changes.length}
      <button onclick={() => void page()}>{t("chg.more")}</button>
    {/if}
  </section>
</div>

<style>
  .nowrap {
    white-space: nowrap;
  }
</style>
