<script lang="ts">
  import { onMount } from "svelte";
  import Channels from "../components/Channels.svelte";
  import Imports from "../components/Imports.svelte";
  import Rules from "../components/Rules.svelte";
  import StatusPages from "../components/StatusPages.svelte";
  import { api } from "../lib/api";
  import { when } from "../lib/format";
  import { can, t, toast, ui } from "../lib/state.svelte";
  import type { Agent, AgentToken, APIToken, AuditEntry, Schedule, Segment, Settings, User } from "../lib/types";

  type Tab = "general" | "agents" | "users" | "tokens" | "schedules" | "segments" | "notifications" | "discovery" | "status" | "import" | "audit";
  let tab = $state<Tab>(can("admin") ? "agents" : "tokens");
  let settings = $state<Settings | null>(null);
  let webhooks = $state("");
  let agents = $state<Agent[]>([]);
  let regTokens = $state<AgentToken[]>([]);
  let users = $state<User[]>([]);
  let tokens = $state<APIToken[]>([]);
  let schedules = $state<Schedule[]>([]);
  let segments = $state<Segment[]>([]);
  let audit = $state<AuditEntry[]>([]);
  let created = $state<{ token: string; install?: Record<string, string> } | null>(null);
  let platform = $state("linux");
  let form = $state({ name: "", reusable: false, hours: 24, password: "", role: "viewer", spec: "every 5m", kind: "reachability" });

  const tabs: { id: Tab; admin: boolean }[] = [
    { id: "general", admin: true },
    { id: "agents", admin: true },
    { id: "users", admin: true },
    { id: "tokens", admin: false },
    { id: "schedules", admin: false },
    { id: "segments", admin: false },
    { id: "notifications", admin: true },
    { id: "discovery", admin: false },
    { id: "status", admin: false },
    { id: "import", admin: true },
    { id: "audit", admin: true },
  ];

  async function act(fn: () => Promise<unknown>, reload = true): Promise<void> {
    try {
      await fn();
      if (reload) await load();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  async function load(): Promise<void> {
    const admin = can("admin");
    const [s, ag, tk, sc, sg] = await Promise.all([api.settings(), api.agents(), api.tokens(), api.schedules(), api.segments()]);
    settings = s;
    webhooks = (s.webhooks ?? []).join("\n");
    agents = ag;
    tokens = tk;
    schedules = sc;
    segments = sg;
    if (admin) {
      [regTokens, users, audit] = await Promise.all([api.agentTokens(), api.users(), api.audit()]);
    }
  }

  function copy(s: string): void {
    void navigator.clipboard?.writeText(s).then(() => toast(t("common.copied")));
  }

  onMount(() => void act(load, false));
</script>

<div class="grid">
  <div class="tabs">
    {#each tabs.filter((x) => !x.admin || can("admin")) as x (x.id)}
      <button class:on={tab === x.id} onclick={() => (tab = x.id)}>{t(`set.${x.id}`)}</button>
    {/each}
  </div>

  {#if tab === "general" && settings}
    <section class="card narrow">
      <label class="field">{t("set.duration_ms")}<input type="number" min="1000" max="30000" bind:value={settings.duration_ms} /></label>
      <label class="field">{t("set.streams")}<input type="number" min="1" max="16" bind:value={settings.streams} /></label>
      <label class="field">{t("set.ping_count")}<input type="number" min="1" max="100" bind:value={settings.ping_count} /></label>
      <label class="field">{t("set.rtt_warn")}<input type="number" min="0" bind:value={settings.rtt_warn_ms} /></label>
      <label class="field">{t("set.retention")}<input type="number" min="1" bind:value={settings.retention_days} /></label>
      <label class="field">{t("set.mini_token")}<input type="password" autocomplete="off" bind:value={settings.mini_token} /></label>
      <label class="field">{t("set.public_url")}<input bind:value={settings.public_url} placeholder="https://lanscape.example.com" /></label>
      <label class="field">{t("set.webhooks")}<textarea rows="3" bind:value={webhooks}></textarea></label>
      <label><input type="checkbox" bind:checked={settings.guest_dashboard} /> {t("set.guest")}</label>
      <label class="field">{t("set.aggregates")}<input type="number" min="0" bind:value={settings.aggregate_days} /></label>
      <h3>{t("set.internet")}</h3>
      <p class="muted small">{t("set.internet_hint")}</p>
      <div class="row wrap">
        {#each [{ id: "server", name: t("mon.server") }, ...agents.filter((a) => a.kind !== "lite")] as p (p.id)}
          <label>
            <input
              type="checkbox"
              checked={(settings.internet_points ?? ["server"]).includes(p.id)}
              onchange={(e) => {
                const cur = (settings!.internet_points ?? ["server"]).filter((x) => x !== p.id);
                settings!.internet_points = (e.target as HTMLInputElement).checked ? [...cur, p.id] : cur;
              }}
            />
            {p.name}
          </label>
        {/each}
      </div>
      <label class="field">{t("set.internet_every")}<input type="number" min="0" max="1440" bind:value={settings.internet_every_min} /></label>
      <label class="field">{t("set.internet_speed_every")}<input type="number" min="0" max="168" bind:value={settings.internet_speed_every_h} /></label>
      <label class="field">{t("set.internet_ip_url")}<input bind:value={settings.internet_ip_url} placeholder="https://1.1.1.1/cdn-cgi/trace" /></label>
      <label class="field"
        >{t("set.internet_download_url")}<input bind:value={settings.internet_download_url} placeholder="https://speed.cloudflare.com/__down?bytes=50000000" /></label
      >
      <div class="row">
        <button onclick={() => act(async () => { await api.runInternet(false); toast(t("set.internet_started")); })}>{t("set.internet_run")}</button>
        <button onclick={() => act(async () => { await api.runInternet(true); toast(t("set.internet_started")); })}>{t("set.internet_run_speed")}</button>
      </div>
      <button
        class="primary"
        onclick={() =>
          act(async () => {
            await api.saveSettings({ ...settings!, webhooks: webhooks.split("\n").map((w) => w.trim()).filter(Boolean) });
            toast(t("set.saved"));
          })}>{t("set.save")}</button
      >
    </section>
  {/if}

  {#if tab === "agents"}
    <section class="card">
      <h2>{t("set.add_node")}</h2>
      <div class="row">
        <input placeholder={t("set.name")} bind:value={form.name} />
        <label><input type="checkbox" bind:checked={form.reusable} /> {t("set.reusable")}</label>
        <label>{t("set.expires")} <input type="number" min="0" style="width:6em" bind:value={form.hours} /></label>
        <button
          class="primary"
          data-testid="create-agent-token"
          onclick={() =>
            act(async () => {
              created = await api.createAgentToken(form.name, form.reusable, form.hours * 3600);
            })}>{t("set.add")}</button
        >
      </div>
      {#if created}
        <p>{t("set.token_once")}</p>
        <pre data-testid="agent-token">{created.token}</pre>
        {#if created.install}
          <div class="tabs">
            {#each Object.keys(created.install) as p (p)}
              <button class:on={platform === p} onclick={() => (platform = p)}>{p}</button>
            {/each}
          </div>
          <pre>{created.install[platform]}</pre>
          <button onclick={() => copy(created?.install?.[platform] ?? "")}>{t("common.copy")}</button>
        {/if}
      {/if}
    </section>
    <section class="card">
      <h2>{t("dev.agents")}</h2>
      <table>
        <tbody>
          {#each agents as a (a.id)}
            <tr>
              <td><span class="dot {a.online ? 'v-green' : 'v-red'}"></span>{a.name}</td>
              <td class="muted">{a.kind} · {a.version} · {a.arch}</td>
              <td class="muted">{when(a.last_seen, ui.lang)}</td>
              <td>
                <button class="danger" onclick={() => confirm(t("set.confirm_delete", { name: a.name })) && act(() => api.deleteAgent(a.id))}>
                  {t("set.delete")}
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      <h3>{t("set.reg_tokens")}</h3>
      <table class="small">
        <tbody>
          {#each regTokens as rt (rt.id)}
            <tr>
              <td>{rt.name}</td>
              <td>{rt.reusable ? t("set.reusable") : ""} · {rt.uses} {t("set.uses")}</td>
              <td>{rt.expires_at ? when(rt.expires_at, ui.lang) : "∞"}</td>
              <td><button class="danger" onclick={() => act(() => api.deleteAgentToken(rt.id))}>{t("set.delete")}</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}

  {#if tab === "users"}
    <section class="card">
      <div class="row">
        <input placeholder={t("login.username")} bind:value={form.name} />
        <input type="password" placeholder={t("set.password")} autocomplete="new-password" bind:value={form.password} />
        <select bind:value={form.role}>
          <option>viewer</option><option>operator</option><option>admin</option>
        </select>
        <button class="primary" onclick={() => act(() => api.createUser(form.name, form.password, form.role))}>{t("set.add")}</button>
      </div>
      <table>
        <tbody>
          {#each users as u (u.id)}
            <tr>
              <td>{u.username}</td>
              <td>
                <select value={u.role} onchange={(e) => act(() => api.updateUser(u.id, { role: e.currentTarget.value }))}>
                  <option>viewer</option><option>operator</option><option>admin</option>
                </select>
              </td>
              <td>
                <label><input type="checkbox" checked={u.disabled} onchange={(e) => act(() => api.updateUser(u.id, { disabled: e.currentTarget.checked }))} /> {t("set.disabled")}</label>
              </td>
              <td>2FA: {u.totp_enabled ? t("common.yes") : t("common.no")}</td>
              <td>
                {#if u.id !== ui.user?.id}
                  <button class="danger" onclick={() => confirm(t("set.confirm_delete", { name: u.username })) && act(() => api.deleteUser(u.id))}>{t("set.delete")}</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}

  {#if tab === "tokens"}
    <section class="card">
      <div class="row">
        <input placeholder={t("set.name")} bind:value={form.name} />
        <select bind:value={form.role}><option>viewer</option><option>operator</option><option>admin</option></select>
        <button class="primary" onclick={() => act(async () => (created = await api.createToken(form.name, form.role)))}>{t("set.add")}</button>
      </div>
      {#if created && !created.install}
        <p>{t("set.token_once")}</p>
        <pre>{created.token}</pre>
      {/if}
      <table>
        <thead><tr><th>{t("set.name")}</th><th>{t("set.role")}</th><th>{t("set.created")}</th><th>{t("set.last_used")}</th><th></th></tr></thead>
        <tbody>
          {#each tokens as tk (tk.id)}
            <tr>
              <td>{tk.name}</td>
              <td>{tk.role}</td>
              <td>{when(tk.created_at, ui.lang)}</td>
              <td>{when(tk.last_used, ui.lang)}</td>
              <td><button class="danger" onclick={() => act(() => api.deleteToken(tk.id))}>{t("set.delete")}</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}

  {#if tab === "schedules"}
    <section class="card">
      {#if can("admin")}
        <div class="row">
          <input placeholder={t("set.name")} bind:value={form.name} />
          <select bind:value={form.kind}><option>reachability</option><option>full</option><option>aggregate</option></select>
          <input placeholder={t("set.spec_hint")} bind:value={form.spec} />
          <button class="primary" onclick={() => act(() => api.saveSchedule({ name: form.name, kind: form.kind, spec: form.spec, enabled: true }))}>
            {t("set.add")}
          </button>
        </div>
      {/if}
      <table>
        <tbody>
          {#each schedules as s (s.id)}
            <tr>
              <td>{s.name}</td>
              <td>{s.kind}</td>
              <td><code>{s.spec}</code></td>
              <td>
                <label><input type="checkbox" checked={s.enabled} disabled={!can("admin")} onchange={(e) => act(() => api.saveSchedule({ ...s, enabled: e.currentTarget.checked }))} /> {t("set.enabled")}</label>
              </td>
              <td>{when(s.last_run, ui.lang)}</td>
              <td>{#if can("admin")}<button class="danger" onclick={() => act(() => api.deleteSchedule(s.id))}>{t("set.delete")}</button>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}

  {#if tab === "segments"}
    <section class="card">
      <table>
        <thead><tr><th>{t("map.segment")}</th><th>{t("set.name")}</th><th>{t("set.expected_mbps")}</th><th></th></tr></thead>
        <tbody>
          {#each segments as s (s.id)}
            <tr>
              <td>{s.id}<div class="muted small">{s.members.length} · {s.manual ? "manual" : "auto"}</div></td>
              <td><input bind:value={s.name} disabled={!can("admin")} /></td>
              <td><input type="number" min="0" bind:value={s.expected_mbps} disabled={!can("admin")} /></td>
              <td>{#if can("admin")}<button onclick={() => act(() => api.setSegment(s.id, s.name ?? "", s.expected_mbps))}>{t("set.save")}</button>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}

  {#if tab === "notifications"}
    <Channels />
  {/if}

  {#if tab === "discovery"}
    <Rules />
  {/if}

  {#if tab === "status"}
    <StatusPages />
  {/if}

  {#if tab === "import"}
    <Imports />
  {/if}

  {#if tab === "audit"}
    <section class="card">
      <table class="small">
        <tbody>
          {#each audit as e (e.id)}
            <tr><td>{when(e.ts, ui.lang)}</td><td>{e.actor}</td><td>{e.action}</td><td>{e.target}</td><td>{e.result}</td><td class="muted">{e.detail}</td></tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}
</div>

<style>
  .narrow {
    max-width: 560px;
  }
</style>
