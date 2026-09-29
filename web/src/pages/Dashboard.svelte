<script lang="ts">
  import { onMount } from "svelte";
  import AppIcon from "../components/AppIcon.svelte";
  import { api, subscribe } from "../lib/api";
  import { bytes, daysLeft, latency, pct, statusClass, when } from "../lib/format";
  import { can, t, toast, ui } from "../lib/state.svelte";
  import type { Dashboard, ServiceView } from "../lib/types";

  let d = $state<Dashboard | null>(null);
  let dragging = $state<ServiceView | null>(null);

  async function load(): Promise<void> {
    try {
      d = await api.dashboard();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  // several events arrive together (a check round); refresh at most every 2 s
  let pending: ReturnType<typeof setTimeout> | undefined;
  function reload(): void {
    if (pending) return;
    pending = setTimeout(() => {
      pending = undefined;
      void load();
    }, 2000);
  }

  onMount(() => {
    void load();
    const off = subscribe({ monitor: reload, incident: reload, services: reload, change: reload, maintenance: reload });
    return () => {
      off();
      clearTimeout(pending);
    };
  });

  async function drop(group: string, before: ServiceView | null): Promise<void> {
    const moved = dragging;
    dragging = null;
    if (!d || !moved || moved === before || !can("operator")) return;
    const groups = d.groups.map((g) => ({ name: g.name, tiles: g.tiles.filter((x) => x.id !== moved.id) }));
    let target = groups.find((g) => g.name === group);
    if (!target) {
      target = { name: group, tiles: [] };
      groups.push(target);
    }
    const i = before ? target.tiles.findIndex((x) => x.id === before.id) : -1;
    target.tiles.splice(i < 0 ? target.tiles.length : i, 0, { ...moved, group });
    d.groups = groups.filter((g) => g.tiles.length > 0);
    const order = target.tiles.map((x, n) => ({ id: x.id, group, sort: n }));
    try {
      await api.orderServices(order);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
      void load();
    }
  }

  const hwClass = (st: string): string =>
    ["online", "passed"].includes(st) ? "v-green" : ["warning", "on_battery", "degraded"].includes(st) ? "v-yellow" : "v-red";
  // pools always, disks only when something is wrong (healthy disks are counted)
  const disksShown = $derived((d?.storage ?? []).filter((h) => h.kind === "pool" || h.state !== "passed"));
  const healthyDisks = $derived((d?.storage ?? []).filter((h) => h.kind === "disk" && h.state === "passed").length);

  const mem = (a: Dashboard["agents"][number]): number =>
    a.mem_total ? Math.round(((a.mem_total - a.mem_available) * 100) / a.mem_total) : 0;
</script>

{#if d}
  <div class="grid">
    {#if d.maintenance.some((m) => m.starts <= Date.now())}
      {#each d.maintenance.filter((m) => m.starts <= Date.now()) as m (m.id)}
        <div class="banner maint">{t("dash.maintenance", { name: m.name, until: when(m.ends, ui.lang) })}</div>
      {/each}
    {/if}
    {#if d.found_new > 0}
      <div class="banner">
        {t("dash.found_new", { n: d.found_new })}
        <a href="#/services?tab=found">{t("dash.review")}</a>
      </div>
    {/if}

    <div class="summary" data-testid="summary">
      <span><span class="dot v-green"></span>{d.summary.up ?? 0} {t("dash.up")}</span>
      <span><span class="dot v-yellow"></span>{d.summary.degraded ?? 0} {t("dash.degraded")}</span>
      <span><span class="dot v-red"></span>{d.summary.down ?? 0} {t("dash.down")}</span>
      <span class="spacer"></span>
      <a href="#/monitors?tab=incidents">{t("dash.incidents")}: <b>{d.summary.incidents ?? 0}</b></a>
    </div>

    {#if d.groups.length === 0}
      <section class="card muted">{t("dash.empty")}</section>
    {/if}
    {#each d.groups as g (g.name)}
      <section
        class="card"
        role="list"
        ondragover={(e) => e.preventDefault()}
        ondrop={(e) => {
          e.preventDefault();
          void drop(g.name, null);
        }}
      >
        <h2>{g.name || t("dash.ungrouped")}</h2>
        <div class="tiles">
          {#each g.tiles as s (s.id)}
            <a
              class="tile"
              data-testid="tile"
              href={s.url || `#/services/${s.id}`}
              target={s.url ? "_blank" : undefined}
              rel="noopener"
              draggable={can("operator")}
              ondragstart={() => (dragging = s)}
              ondragover={(e) => e.preventDefault()}
              ondrop={(e) => {
                e.preventDefault();
                e.stopPropagation();
                void drop(g.name, s);
              }}
            >
              <AppIcon icon={s.icon} name={s.name} size={36} />
              <span class="tname">
                <b>{s.name}</b>
                <span class="muted small">
                  {#if s.status}<span class="dot {statusClass(s.status)}" title={t(`mon.status.${s.status}`)}></span>{/if}
                  {s.status ? latency(s.latency_ms) : ""}
                  {s.uptime_day !== null ? ` · ${pct(s.uptime_day)}` : ""}
                </span>
              </span>
            </a>
          {/each}
        </div>
      </section>
    {/each}

    <div class="widgets">
      <section class="card">
        <h3>{t("dash.incidents")}</h3>
        {#each d.incidents.filter((i) => !i.suppressed || !d?.incidents.some((p) => p.id === i.parent_id)) as i (i.id)}
          {@const deps = d.incidents.filter((x) => x.suppressed && x.parent_id === i.id)}
          <div class="line">
            <span class="dot {i.suppressed ? 'v-yellow' : 'v-red'}"></span>
            <a href="#/monitors/{i.monitor_id}">{i.monitor}</a>
            <span class="muted small">{when(i.opened, ui.lang)} · {i.cause}</span>
            {#if deps.length}
              <span class="tag" title={deps.map((x) => x.monitor).join(", ")}>{t("dash.dependents", { n: deps.length })}</span>
            {/if}
          </div>
        {:else}
          <div class="muted">{t("dash.no_incidents")}</div>
        {/each}
      </section>

      <section class="card">
        <h3><a href="#/network">{t("dash.network")}</a></h3>
        {#if d.network}
          <div class="small">#{d.network.run_id} · {when(d.network.finished, ui.lang)} · {t("dash.paths", { n: d.network.paths })}</div>
          <div class="verdicts">
            {#each ["green", "yellow", "purple", "red", "none"] as v (v)}
              {#if d.network.verdicts[v]}
                <span><span class="dot v-{v}"></span>{d.network.verdicts[v]}</span>
              {/if}
            {/each}
          </div>
        {:else}
          <div class="muted">{t("dash.network_none")}</div>
        {/if}
      </section>

      <section class="card">
        <h3>{t("dash.resources")}</h3>
        {#each d.agents as a (a.id)}
          <div class="line small">
            <span class="dot {a.online ? 'v-green' : 'v-red'}"></span>
            <b>{a.name}</b>
            {#if a.cpus}
              <span class="muted">load {a.load1.toFixed(2)}/{a.cpus} · RAM {mem(a)}% of {bytes(a.mem_total)}</span>
            {/if}
            {#if a.disk_pct}<span class="muted" class:warn={a.disk_pct > 90}> · {a.disk_mount} {Math.round(a.disk_pct)}%</span>{/if}
            {#if a.temp_c}<span class="muted"> · {a.temp_c.toFixed(0)}°C</span>{/if}
            {#if a.updates}<span class="muted"> · {t("dash.updates", { n: a.updates })}</span>{/if}
          </div>
        {/each}
      </section>

      <section class="card">
        <h3>{t("dash.certs")}</h3>
        {#each d.certificates as c (c.name + c.not_after)}
          <div class="line small">
            <span class="dot {daysLeft(c.not_after) < 7 ? 'v-red' : 'v-yellow'}"></span>
            {c.name} <span class="muted">{t("dash.days_left", { n: daysLeft(c.not_after) })}</span>
          </div>
        {:else}
          <div class="muted">{t("dash.no_certs")}</div>
        {/each}
      </section>

      {#if d.backups.length}
        <section class="card">
          <h3>{t("dash.backups")}</h3>
          {#each d.backups as b (b.monitor_id)}
            <div class="line small">
              <span class="dot {statusClass(b.status)}"></span>
              <a href="#/monitors/{b.monitor_id}">{b.name}</a>
              <span class="muted">{b.last_push ? when(b.last_push, ui.lang) : t("dash.never")}</span>
            </div>
          {/each}
        </section>
      {/if}

      {#if d.ups.length}
        <section class="card">
          <h3>{t("dash.ups")}</h3>
          {#each d.ups as u (u.agent + u.name)}
            <div class="line small">
              <span class="dot {hwClass(u.state)}"></span>
              <b>{u.name}</b>
              <span class="muted">
                {t(`dash.ups_${u.state}`)} · {u.labels.charge ?? "?"}% · {t("dash.load")} {u.labels.load ?? "?"}%
                {#if u.labels.runtime_s}· {Math.round(Number(u.labels.runtime_s) / 60)} min{/if}
              </span>
            </div>
          {/each}
        </section>
      {/if}

      {#if d.storage.length}
        <section class="card">
          <h3>{t("dash.storage")}</h3>
          {#each disksShown as h (h.agent + h.kind + h.name)}
            <div class="line small">
              <span class="dot {hwClass(h.state)}"></span>
              <b>{h.name}</b>
              <span class="muted">
                {h.agent} · {h.state}
                {#if h.kind === "pool" && h.labels.size}· {Math.round((Number(h.labels.alloc) * 100) / Number(h.labels.size))}%{/if}
                {#if h.kind === "disk"}· {h.labels.model ?? ""} {h.labels.temp_c ? `${h.labels.temp_c}°C` : ""}{/if}
              </span>
            </div>
          {/each}
          {#if healthyDisks}<div class="muted small">{t("dash.disks_ok", { n: healthyDisks })}</div>{/if}
        </section>
      {/if}

      <section class="card">
        <h3><a href="#/changes">{t("dash.changes")}</a></h3>
        {#each d.changes as c (c.id)}
          <div class="line small">
            <span class="tag">{t(`chg.${c.kind}`)}</span>
            {c.subject} <span class="muted">{c.detail ?? ""}</span>
          </div>
        {:else}
          <div class="muted">{t("chg.none")}</div>
        {/each}
      </section>
    </div>
  </div>
{:else}
  <p class="muted">{t("common.loading")}</p>
{/if}

<style>
  .banner {
    padding: 8px 12px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--accent) 12%, transparent);
  }
  .banner.maint {
    background: color-mix(in srgb, var(--purple) 15%, transparent);
  }
  .summary {
    display: flex;
    gap: 16px;
    flex-wrap: wrap;
    align-items: center;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 10px;
  }
  .tile {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 10px;
    border: 1px solid var(--line);
    border-radius: 10px;
    color: var(--fg);
    text-decoration: none;
    background: var(--bg);
  }
  .tile:hover {
    border-color: var(--accent);
  }
  .tname {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .tname b {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .widgets {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
    gap: 16px;
  }
  .line {
    display: flex;
    gap: 6px;
    align-items: baseline;
    flex-wrap: wrap;
    padding: 2px 0;
  }
  .verdicts {
    display: flex;
    gap: 12px;
    margin-top: 6px;
  }
  .warn {
    color: var(--red);
  }
</style>
