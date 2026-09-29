<script lang="ts">
  import { api } from "../lib/api";
  import { duration, when } from "../lib/format";
  import { can, t, toast, ui } from "../lib/state.svelte";
  import type { Incident } from "../lib/types";

  let { incident, onchange }: { incident: Incident; onchange: () => void } = $props();
  let open = $state(false);
  let note = $state("");
  let full = $state<Incident | null>(null);

  const spent = $derived(((incident.closed || Date.now()) - incident.opened) / 1000);

  async function act(fn: () => Promise<unknown>): Promise<void> {
    try {
      await fn();
      full = await api.incident(incident.id);
      onchange();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  async function toggle(): Promise<void> {
    open = !open;
    if (open) await act(async () => {});
  }
</script>

<div class="inc" data-testid="incident">
  <div class="row">
    <span class="dot {incident.closed ? 'v-green' : 'v-red'}"></span>
    <a href="#/monitors/{incident.monitor_id}"><b>{incident.monitor}</b></a>
    <span class="small muted">
      {t("inc.opened", { when: when(incident.opened, ui.lang) })} ·
      {incident.closed ? t("inc.closed", { d: duration(spent) }) : t("inc.ongoing", { d: duration(spent) })}
      {#if incident.maintenance}· {t("inc.in_maintenance")}{/if}
      {#if incident.acked_by}· {t("inc.acked", { user: incident.acked_by })}{/if}
    </span>
    <span class="spacer"></span>
    {#if !incident.closed && !incident.acked_by && can("operator")}
      <button onclick={() => void act(() => api.ackIncident(incident.id))}>{t("inc.ack")}</button>
    {/if}
    <button onclick={() => void toggle()}>{t("inc.note")}</button>
  </div>
  <div class="small">{incident.cause}</div>
  {#if open}
    {#each full?.notes ?? [] as n (n.id)}
      <div class="small note"><b>{n.author}</b> <span class="muted">{when(n.ts, ui.lang)}</span><br />{n.text}</div>
    {/each}
    {#if can("operator")}
      <div class="row">
        <input bind:value={note} placeholder={t("inc.note")} class="grow" />
        <button
          disabled={!note.trim()}
          onclick={() =>
            void act(async () => {
              await api.noteIncident(incident.id, note);
              note = "";
            })}>{t("inc.add_note")}</button
        >
      </div>
    {/if}
  {/if}
</div>

<style>
  .inc {
    border-bottom: 1px solid var(--line);
    padding: 8px 0;
  }
  .note {
    margin: 4px 0 4px 16px;
  }
  .grow {
    flex: 1;
  }
</style>
