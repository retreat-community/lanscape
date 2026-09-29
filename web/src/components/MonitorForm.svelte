<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { api } from "../lib/api";
  import { latency } from "../lib/format";
  import { t, toast } from "../lib/state.svelte";
  import type { Agent, CheckResult, CheckSpec, Monitor, ServiceView } from "../lib/types";

  let { monitor = null, onsaved, oncancel }: { monitor?: Monitor | null; onsaved: (id: number) => void; oncancel: () => void } =
    $props();

  // the form edits a copy; later changes of the prop do not reset it
  const init = untrack(() => monitor);
  let name = $state(init?.name ?? "");
  let serviceID = $state(init?.service_id ?? 0);
  let interval = $state(init?.interval_s ?? 60);
  let retries = $state(init?.retries ?? 3);
  let points = $state<string[]>(init?.points?.length ? [...init.points] : ["server"]);
  let minFailing = $state(init?.min_failing ?? 1);
  let parents = $state<number[]>(init?.parents ? [...init.parents] : []);
  let monitors = $state<{ id: number; name: string }[]>([]);
  let spec = $state<CheckSpec>(init ? { ...init.spec } : { type: "http", target: "" });
  let codes = $state((init?.spec.expect_status ?? []).join(", "));
  let agents = $state<Agent[]>([]);
  let services = $state<ServiceView[]>([]);
  let test = $state<{ result: CheckResult; points: CheckResult[] } | null>(null);
  let busy = $state(false);

  const resource = $derived(spec.type === "container" || spec.type === "k8s" || spec.type === "vm");
  const serverSide = $derived(spec.type === "heartbeat" || spec.type === "composite" || spec.type === "path" || resource);
  const hint = $derived(
    resource ? t("mon.target_hint_resource") : spec.type === "path" ? t("mon.target_hint_path") : spec.type === "http" ? t("mon.target_hint_http") : spec.type === "icmp" || spec.type === "dns" ? t("mon.target_hint_host") : t("mon.target_hint_hostport"),
  );
  const checkAgents = $derived(agents.filter((a) => a.kind !== "lite"));

  onMount(async () => {
    try {
      [agents, services, monitors] = await Promise.all([api.agents(), api.services(), api.monitors()]);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  });

  function cleanSpec(): CheckSpec {
    const s: CheckSpec = { ...spec, target: spec.target.trim() };
    const c = codes
      .split(/[\s,]+/)
      .map((x) => Number(x))
      .filter((x) => Number.isInteger(x) && x > 0);
    s.expect_status = c.length ? c : undefined;
    // drop empty optional fields so defaults apply on the server
    return Object.fromEntries(Object.entries(s).filter(([, v]) => v !== "" && v !== false && v !== undefined)) as unknown as CheckSpec;
  }

  function toggle(p: string): void {
    points = points.includes(p) ? points.filter((x) => x !== p) : [...points, p];
  }

  async function run(fn: () => Promise<void>): Promise<void> {
    busy = true;
    try {
      await fn();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    } finally {
      busy = false;
    }
  }

  const save = () =>
    run(async () => {
      const m = await api.saveMonitor({
        id: init?.id,
        name: name.trim() || spec.target || spec.type,
        service_id: Number(serviceID),
        spec: cleanSpec(),
        interval_s: Number(interval),
        retries: Number(retries),
        points: serverSide ? [] : points,
        parents,
        min_failing: Number(minFailing),
        enabled: init?.enabled ?? true,
      });
      onsaved(m.id);
    });
  const tryIt = () =>
    run(async () => {
      test = await api.testMonitor(cleanSpec(), points);
    });
</script>

<section class="card form" data-testid="monitor-form">
  <div class="cols">
    <label class="field">{t("set.name")} <input bind:value={name} data-testid="mon-name" /></label>
    <label class="field"
      >{t("mon.type")}
      <select bind:value={spec.type} data-testid="mon-type">
        {#each ["http", "tcp", "udp", "icmp", "dns", "tls", "domain", "heartbeat", "composite", "container", "k8s", "vm", "path"] as ty (ty)}
          <option value={ty}>{t(`mon.type.${ty}`)}</option>
        {/each}
      </select>
    </label>
  </div>
  {#if spec.type === "heartbeat"}
    <p class="small muted">{t("mon.heartbeat_hint")}</p>
    <label class="field">{t("mon.grace")} <input type="number" min="0" bind:value={spec.grace_s} /></label>
  {:else if spec.type === "composite"}
    <label class="field"
      >{t("mon.expr")} <input bind:value={spec.expr} placeholder="#1 && (#2 || #3)" data-testid="mon-expr" /></label
    >
    <div class="small muted">{monitors.filter((m) => m.id !== init?.id).map((m) => `#${m.id} ${m.name}`).join(" · ")}</div>
  {:else}
    <label class="field">{t("mon.target")} <input bind:value={spec.target} placeholder={hint} data-testid="mon-target" /></label>
  {/if}

  {#if spec.type === "http"}
    <div class="cols">
      <label class="field">{t("mon.keyword")} <input bind:value={spec.keyword} /></label>
      <label class="field">{t("mon.expect_status")} <input bind:value={codes} placeholder={t("mon.expect_status_hint")} /></label>
      <label class="field">{t("mon.json_path")} <input bind:value={spec.json_path} placeholder="status" /></label>
      <label class="field">{t("mon.json_value")} <input bind:value={spec.json_value} placeholder="ok" /></label>
    </div>
    <div class="row small">
      <label><input type="checkbox" bind:checked={spec.keyword_regex} /> {t("mon.regex")}</label>
      <label><input type="checkbox" bind:checked={spec.invert_keyword} /> {t("mon.invert")}</label>
      <label><input type="checkbox" bind:checked={spec.ignore_tls_errors} /> {t("mon.ignore_tls")}</label>
      <label><input type="checkbox" bind:checked={spec.no_redirects} /> {t("mon.no_redirects")}</label>
    </div>
  {:else if spec.type === "tcp" || spec.type === "udp"}
    <div class="cols">
      <label class="field">{t("mon.send")} <input bind:value={spec.send} placeholder="\n" /></label>
      <label class="field">{t("mon.expect")} <input bind:value={spec.expect} /></label>
    </div>
  {:else if spec.type === "dns"}
    <div class="cols">
      <label class="field">{t("mon.dns_server")} <input bind:value={spec.server} placeholder="192.168.1.1, tls://1.1.1.1, https://dns.example/dns-query" /></label>
      <label class="field"
        >{t("mon.record")}
        <select bind:value={spec.record}>
          {#each ["A", "AAAA", "CNAME", "MX", "TXT", "NS"] as r (r)}<option>{r}</option>{/each}
        </select>
      </label>
      <label class="field">{t("mon.expect")} <input bind:value={spec.expect} /></label>
    </div>
  {/if}
  {#if spec.type === "tls" || spec.type === "domain" || (spec.type === "http" && spec.target.startsWith("https"))}
    <label class="field">{t("mon.warn_days")} <input type="number" min="1" bind:value={spec.warn_days} placeholder="14" /></label>
  {/if}

  <div class="cols">
    <label class="field">{t("mon.interval")} <input type="number" min="5" bind:value={interval} /></label>
    <label class="field">{t("mon.retries")} <input type="number" min="1" bind:value={retries} /></label>
    <label class="field"
      >{t("mon.service")}
      <select bind:value={serviceID}>
        <option value={0}>{t("mon.no_service")}</option>
        {#each services as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
      </select>
    </label>
  </div>
  {#if !serverSide}
  <div class="field">
    {t("mon.points")}
    <div class="row small">
      <label><input type="checkbox" checked={points.includes("server")} onchange={() => toggle("server")} /> {t("mon.server")}</label>
      {#each checkAgents as a (a.id)}
        <label><input type="checkbox" checked={points.includes(a.id)} onchange={() => toggle(a.id)} /> {a.name}</label>
      {/each}
    </div>
    {#if points.length > 1}
      <label class="small"
        >{t("mon.min_failing")} <input type="number" min="1" max={points.length} bind:value={minFailing} class="num" />
        {t("mon.points_n")}</label
      >
    {/if}
  </div>
  {/if}
  {#if monitors.some((m) => m.id !== init?.id)}
    <div class="field">
      {t("mon.parents")}
      <div class="row small">
        {#each monitors.filter((m) => m.id !== init?.id) as m (m.id)}
          <label>
            <input
              type="checkbox"
              checked={parents.includes(m.id)}
              onchange={() => (parents = parents.includes(m.id) ? parents.filter((x) => x !== m.id) : [...parents, m.id])}
            />
            {m.name}
          </label>
        {/each}
      </div>
    </div>
  {/if}

  {#if test}
    <div class="small">
      <span class="dot v-{test.result.status === 'up' ? 'green' : test.result.status === 'down' ? 'red' : 'yellow'}"></span>
      {t(`mon.status.${test.result.status}`)} · {latency(test.result.latency_ms)}
      {test.result.message ?? ""}
      {#if test.points.length > 1}
        {#each test.points as p (p.point)}<div class="muted">{p.point}: {p.status} {p.message ?? ""}</div>{/each}
      {/if}
    </div>
  {/if}
  <div class="row">
    <button class="primary" disabled={busy || ((!serverSide || resource || spec.type === "path") && !spec.target)} onclick={save} data-testid="mon-save">{t("common.save")}</button>
    <button disabled={busy || serverSide || !spec.target} onclick={tryIt}>{t("mon.test")}</button>
    <button onclick={oncancel}>{t("common.cancel")}</button>
  </div>
</section>

<style>
  .form {
    display: grid;
    gap: 8px;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 8px;
  }
  .num {
    width: 60px;
  }
</style>
