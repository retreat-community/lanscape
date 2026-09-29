<script lang="ts">
  import { onMount, untrack } from "svelte";
  import AppIcon from "../components/AppIcon.svelte";
  import { api, subscribe } from "../lib/api";
  import { latency, pct, statusClass, when } from "../lib/format";
  import { can, t, toast, ui } from "../lib/state.svelte";
  import type { FoundCard, FoundStatus, Service, ServiceView } from "../lib/types";

  let { route }: { route: string } = $props();

  let tab = $state<"catalog" | "found">(untrack(() => route).includes("tab=found") ? "found" : "catalog");
  let services = $state<ServiceView[]>([]);
  let found = $state<FoundCard[]>([]);
  let show = $state<FoundStatus>("new");
  let q = $state("");
  let opts = $state({ monitor: true, tile: true });
  let edit = $state<Partial<Service> | null>(null);
  let delMonitors = $state(false);

  const match = (...fields: (string | undefined)[]): boolean =>
    !q || fields.some((f) => f?.toLowerCase().includes(q.toLowerCase()));
  const shownServices = $derived(services.filter((s) => match(s.name, s.internal_url, s.external_url, s.group)));
  const shownFound = $derived(
    found.filter((c) => c.status === show && match(c.name, c.host_port, c.internal_url, c.external_url, c.app?.name)),
  );
  const counts = $derived(
    found.reduce<Record<string, number>>((m, c) => {
      m[c.status] = (m[c.status] ?? 0) + 1;
      return m;
    }, {}),
  );

  async function act(fn: () => Promise<unknown>): Promise<void> {
    try {
      await fn();
      await load();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  async function load(): Promise<void> {
    [services, found] = await Promise.all([api.services(), api.found()]);
  }

  onMount(() => {
    void act(async () => {});
    return subscribe({ discovery: () => void load(), services: () => void load() });
  });

  function save(): void {
    const s = edit;
    if (!s) return;
    void act(async () => {
      await api.saveService(s);
      edit = null;
    });
  }

  function source(c: FoundCard): string {
    return [...new Set(c.refs.map((r) => r.source))].join(", ");
  }
</script>

<div class="grid">
  <div class="tabs">
    <button class:on={tab === "catalog"} onclick={() => (tab = "catalog")}>{t("svc.catalog")}</button>
    <button class:on={tab === "found"} onclick={() => (tab = "found")} data-testid="tab-found">
      {t("svc.found")}{counts.new ? ` (${counts.new})` : ""}
    </button>
    <span class="spacer"></span>
    <input placeholder={t("common.search")} bind:value={q} />
    {#if can("operator")}
      {#if tab === "found"}
        <button onclick={() => void act(async () => toast(t("svc.rescan_done", { n: (await api.rescan()).agents })))}>
          {t("svc.rescan")}
        </button>
      {:else}
        <button class="primary" onclick={() => (edit = { name: "", tile: true, group: "", internal_url: "", external_url: "" })}>
          {t("svc.add_manual")}
        </button>
      {/if}
    {/if}
  </div>

  {#if edit}
    <section class="card form">
      <label class="field">{t("set.name")} <input bind:value={edit.name} data-testid="svc-name" /></label>
      <label class="field">{t("svc.group")} <input bind:value={edit.group} /></label>
      <label class="field">{t("svc.internal_url")} <input bind:value={edit.internal_url} placeholder="http://192.168.1.10:3000" /></label>
      <label class="field">{t("svc.external_url")} <input bind:value={edit.external_url} placeholder="https://git.example.com" /></label>
      <label class="field">{t("svc.notes")} <textarea rows="2" bind:value={edit.notes}></textarea></label>
      <label><input type="checkbox" bind:checked={edit.tile} /> {t("svc.tile")}</label>
      <div class="row">
        <button class="primary" onclick={save}>{t("common.save")}</button>
        <button onclick={() => (edit = null)}>{t("common.cancel")}</button>
        <span class="spacer"></span>
        {#if edit.id}
          {@const id = edit.id}
          <label class="small"><input type="checkbox" bind:checked={delMonitors} /> {t("svc.delete_monitors")}</label>
          <button
            class="danger"
            onclick={() =>
              confirm(t("set.confirm_delete", { name: edit?.name ?? "" })) &&
              void act(async () => {
                await api.deleteService(id, delMonitors);
                edit = null;
              })}>{t("common.delete")}</button
          >
        {/if}
      </div>
    </section>
  {/if}

  {#if tab === "catalog"}
    <section class="card">
      {#if shownServices.length === 0}
        <p class="muted">{t("svc.no_services")}</p>
      {:else}
        <table>
          <tbody>
            {#each shownServices as s (s.id)}
              <tr>
                <td class="ic"><AppIcon icon={s.icon} name={s.name} size={28} /></td>
                <td>
                  <b>{s.name}</b>
                  <div class="muted small">{s.group}{s.category && s.category !== s.group ? ` · ${s.category}` : ""}</div>
                </td>
                <td class="small">
                  {#if s.internal_url}<div><a href={s.internal_url} target="_blank" rel="noopener">{s.internal_url}</a></div>{/if}
                  {#if s.external_url}<div><a href={s.external_url} target="_blank" rel="noopener">{s.external_url}</a></div>{/if}
                </td>
                <td class="small">
                  {#if s.status}
                    <span class="dot {statusClass(s.status)}"></span>{t(`mon.status.${s.status}`)} · {latency(s.latency_ms)} · {pct(s.uptime_day)}
                  {/if}
                </td>
                <td class="small">
                  {#each s.monitors as m (m)}<a href="#/monitors/{m}">#{m}</a> {/each}
                </td>
                <td>
                  {#if can("operator")}<button onclick={() => (edit = { ...s })}>{t("common.edit")}</button>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
  {:else}
    <div class="row">
      {#each ["new", "added", "ignored", "hidden"] as const as st (st)}
        <button class:primary={show === st} onclick={() => (show = st)}>{t(`svc.status_${st}`)} ({counts[st] ?? 0})</button>
      {/each}
      <span class="spacer"></span>
      {#if show === "new" && can("operator")}
        <label class="small"><input type="checkbox" bind:checked={opts.monitor} /> {t("svc.with_monitor")}</label>
        <label class="small"><input type="checkbox" bind:checked={opts.tile} /> {t("svc.with_tile")}</label>
      {/if}
    </div>
    {#if shownFound.length === 0}
      <section class="card muted">{t("svc.no_found")}</section>
    {/if}
    <div class="cards">
      {#each shownFound as c (c.key)}
        <section class="card found" class:gone={c.gone} data-testid="found-card">
          <div class="row">
            <AppIcon icon={c.app?.icon} name={c.name} size={32} />
            <div class="grow">
              <b>{c.name}</b>
              {#if c.gone}<span class="tag">{t("svc.gone")}</span>{/if}
              <div class="muted small">
                {c.app ? `${c.app.name} · ${c.app.category}` : c.kind}{c.state ? ` · ${c.state}` : ""}{c.health ? ` (${c.health})` : ""}
              </div>
            </div>
          </div>
          <div class="small addr">
            {#each c.addresses as a (a.type + a.value)}
              <div>
                <span class="tag">{a.type}</span>
                {#if a.value.startsWith("http")}<a href={a.value} target="_blank" rel="noopener">{a.value}</a>{:else}{a.value}{/if}
              </div>
            {/each}
          </div>
          {#if c.monitor}
            <div class="small muted">{t("svc.recommended")}: {c.monitor.type} {c.monitor.target}</div>
          {/if}
          <div class="small muted">
            {t("svc.sources")} {source(c)} · {when(c.first_seen, ui.lang)}{c.rule ? ` · ${t("svc.rule", { name: c.rule })}` : ""}
          </div>
          {#if can("operator")}
            <div class="row">
              {#if c.status === "new"}
                <button class="primary" data-testid="found-add" onclick={() => void act(() => api.addFound(c.key, opts))}>
                  {t("svc.add")}
                </button>
                <button onclick={() => void act(() => api.setFoundState(c.key, "ignored"))}>{t("svc.ignore")}</button>
                <button onclick={() => void act(() => api.setFoundState(c.key, "hidden"))}>{t("svc.hide")}</button>
              {:else if c.status !== "added"}
                <button onclick={() => void act(() => api.setFoundState(c.key, "new"))}>{t("svc.restore")}</button>
              {/if}
            </div>
          {/if}
        </section>
      {/each}
    </div>
  {/if}
</div>

<style>
  .form {
    display: grid;
    gap: 8px;
    max-width: 640px;
  }
  .ic {
    width: 36px;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: 12px;
  }
  .found {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .found.gone {
    opacity: 0.6;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .addr {
    word-break: break-all;
  }
</style>
