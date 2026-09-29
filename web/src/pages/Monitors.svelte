<script lang="ts">
  import { onMount, untrack } from "svelte";
  import IncidentItem from "../components/IncidentItem.svelte";
  import MonitorDetail from "../components/MonitorDetail.svelte";
  import MonitorForm from "../components/MonitorForm.svelte";
  import { api, subscribe } from "../lib/api";
  import { latency, pct, statusClass, when } from "../lib/format";
  import { can, navigate, t, toast, ui } from "../lib/state.svelte";
  import type { Incident, Maintenance, MonitorView } from "../lib/types";

  let { route }: { route: string } = $props();

  const path = $derived(route.split("?")[0]);
  const detailID = $derived(Number(path.slice("/monitors/".length)) || 0);
  const initial = untrack(() => route);
  let tab = $state<"monitors" | "incidents" | "maintenance">(
    initial.includes("tab=incidents") ? "incidents" : initial.includes("tab=maintenance") ? "maintenance" : "monitors",
  );
  let monitors = $state<MonitorView[]>([]);
  let incidents = $state<Incident[]>([]);
  let windows = $state<Maintenance[]>([]);
  let expanded = $state<string[]>([]);

  // open incidents suppressed for the same reason ("host prx1 is offline") are shown as one group
  const groups = $derived.by(() => {
    const by: Record<string, Incident[]> = {};
    for (const i of incidents) {
      if (!i.suppressed || i.closed) continue;
      const cause = i.cause.split(";")[0];
      (by[cause] ??= []).push(i);
    }
    return Object.entries(by).filter(([, list]) => list.length > 1);
  });
  const grouped = $derived(new Set(groups.flatMap(([, list]) => list.map((i) => i.id))));
  let creating = $state(initial.includes("new=1"));
  let q = $state("");
  let mw = $state({ name: "", minutes: 30, planned: false, starts: "", ends: "", monitors: [] as number[] });

  const shown = $derived(
    monitors.filter((m) => !q || m.name.toLowerCase().includes(q.toLowerCase()) || (m.spec.target ?? "").includes(q)),
  );

  async function load(): Promise<void> {
    try {
      [monitors, incidents, windows] = await Promise.all([api.monitors(), api.incidents(), api.maintenance()]);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  let pending: ReturnType<typeof setTimeout> | undefined;
  onMount(() => {
    void load();
    const off = subscribe({
      monitor: () => {
        if (!pending) pending = setTimeout(() => ((pending = undefined), void load()), 2000);
      },
      incident: () => void load(),
      maintenance: () => void load(),
    });
    return () => {
      off();
      clearTimeout(pending);
    };
  });

  async function createWindow(): Promise<void> {
    try {
      if (mw.planned) {
        await api.createMaintenance({ name: mw.name, starts: new Date(mw.starts).getTime(), ends: new Date(mw.ends).getTime(), monitors: mw.monitors });
      } else {
        await api.createMaintenance({ name: mw.name, duration_min: Number(mw.minutes), monitors: mw.monitors });
      }
      mw.name = "";
      await load();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }
</script>

{#if detailID}
  <div class="grid">
    <a href="#/monitors">← {t("mon.monitors")}</a>
    {#key detailID}<MonitorDetail id={detailID} />{/key}
  </div>
{:else}
  <div class="grid">
    <div class="tabs">
      <button class:on={tab === "monitors"} onclick={() => (tab = "monitors")}>{t("mon.monitors")}</button>
      <button class:on={tab === "incidents"} onclick={() => (tab = "incidents")}>
        {t("mon.incidents")}{incidents.some((i) => !i.closed) ? ` (${incidents.filter((i) => !i.closed).length})` : ""}
      </button>
      <button class:on={tab === "maintenance"} onclick={() => (tab = "maintenance")}>{t("mon.maintenance")}</button>
      <span class="spacer"></span>
      {#if tab === "monitors"}
        <input placeholder={t("common.search")} bind:value={q} />
        {#if can("operator")}
          <button class="primary" data-testid="mon-new" onclick={() => (creating = true)}>{t("mon.new")}</button>
        {/if}
      {/if}
    </div>

    {#if tab === "monitors"}
      {#if creating}
        <MonitorForm onsaved={(id) => ((creating = false), navigate(`/monitors/${id}`))} oncancel={() => (creating = false)} />
      {/if}
      <section class="card">
        {#if shown.length === 0}
          <p class="muted">{t("mon.none")}</p>
        {:else}
          <table>
            <thead>
              <tr>
                <th></th><th>{t("set.name")}</th><th>{t("mon.target")}</th><th>{t("mon.response")}</th>
                <th>{t("mon.day")}</th><th>{t("mon.month")}</th><th></th>
              </tr>
            </thead>
            <tbody>
              {#each shown as m (m.id)}
                <tr data-testid="monitor-row">
                  <td><span class="dot {statusClass(m.status)}" title={t(`mon.status.${m.status}`)}></span></td>
                  <td>
                    <a href="#/monitors/{m.id}"><b>{m.name}</b></a>
                    <div class="muted small">{t(`mon.status.${m.status}`)}{m.last_message ? ` · ${m.last_message}` : ""}</div>
                  </td>
                  <td class="small">{t(`mon.type.${m.spec.type}`)} {m.spec.target || m.spec.expr || ""}</td>
                  <td>{latency(m.last_latency)}</td>
                  <td>{pct(m.uptime.day)}</td>
                  <td>{pct(m.uptime.month)}</td>
                  <td>{#if m.incident_id}<span class="tag v-red">!</span>{/if}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </section>
    {:else if tab === "incidents"}
      <section class="card">
        {#each groups as [cause, list] (cause)}
          <div class="group">
            <button class="link" onclick={() => (expanded = expanded.includes(cause) ? expanded.filter((c) => c !== cause) : [...expanded, cause])}>
              <span class="dot v-red"></span>
              <b>{t("inc.group", { n: list.length, cause })}</b>
            </button>
            {#if expanded.includes(cause)}
              {#each list as i (i.id)}<IncidentItem incident={i} onchange={load} />{/each}
            {/if}
          </div>
        {/each}
        {#each incidents.filter((i) => !grouped.has(i.id)) as i (i.id)}
          <IncidentItem incident={i} onchange={load} />
        {:else}
          <p class="muted">{t("inc.none")}</p>
        {/each}
      </section>
    {:else}
      {#if can("operator")}
        <section class="card form">
          <div class="row">
            <label><input type="radio" bind:group={mw.planned} value={false} /> {t("mnt.now")}</label>
            <input type="number" min="1" bind:value={mw.minutes} class="num" disabled={mw.planned} />
            {t("mnt.minutes")}
            <label><input type="radio" bind:group={mw.planned} value={true} /> {t("mnt.planned")}</label>
          </div>
          {#if mw.planned}
            <div class="row">
              <label class="field">{t("mnt.starts")} <input type="datetime-local" bind:value={mw.starts} /></label>
              <label class="field">{t("mnt.ends")} <input type="datetime-local" bind:value={mw.ends} /></label>
            </div>
          {/if}
          <label class="field">{t("set.name")} <input bind:value={mw.name} placeholder={t("mon.maintenance")} /></label>
          <div class="row small">
            {#each monitors as m (m.id)}
              <label>
                <input
                  type="checkbox"
                  checked={mw.monitors.includes(m.id)}
                  onchange={() => (mw.monitors = mw.monitors.includes(m.id) ? mw.monitors.filter((x) => x !== m.id) : [...mw.monitors, m.id])}
                />
                {m.name}
              </label>
            {/each}
            {#if mw.monitors.length === 0}<span class="muted">({t("mnt.all")})</span>{/if}
          </div>
          <div class="row"><button class="primary" onclick={() => void createWindow()}>{t("mnt.create")}</button></div>
        </section>
      {/if}
      <section class="card">
        {#each windows as w (w.id)}
          <div class="row">
            {#if w.starts <= Date.now()}<span class="tag v-purple">{t("mnt.active")}</span>{/if}
            <b>{w.name}</b>
            <span class="small muted">
              {when(w.starts, ui.lang)} — {when(w.ends, ui.lang)} ·
              {w.monitors.length ? w.monitors.map((id) => monitors.find((m) => m.id === id)?.name ?? `#${id}`).join(", ") : t("mnt.all")}
              · {w.created_by}
            </span>
            <span class="spacer"></span>
            {#if can("operator")}
              <button onclick={() => void api.deleteMaintenance(w.id).then(load)}>{t("common.delete")}</button>
            {/if}
          </div>
        {:else}
          <p class="muted">{t("mnt.none")}</p>
        {/each}
      </section>
    {/if}
  </div>
{/if}

<style>
  .group {
    border-bottom: 1px solid var(--line);
    padding: 6px 0;
  }
  .group > button {
    background: none;
    border: 0;
    padding: 0;
    color: inherit;
    cursor: pointer;
    text-align: left;
  }
  .form {
    display: grid;
    gap: 8px;
  }
  .num {
    width: 80px;
  }
</style>
