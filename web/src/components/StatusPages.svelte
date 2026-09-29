<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { can, t, toast } from "../lib/state.svelte";
  import type { MonitorView, StatusPage } from "../lib/types";

  let pages = $state<StatusPage[]>([]);
  let monitors = $state<MonitorView[]>([]);
  let edit = $state<StatusPage | null>(null);

  async function load(): Promise<void> {
    [pages, monitors] = await Promise.all([api.statusPages(), api.monitors()]);
  }

  async function act(fn: () => Promise<unknown>): Promise<void> {
    try {
      await fn();
      await load();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  onMount(() => void act(async () => {}));

  function link(p: StatusPage): string {
    return `${location.origin}/status/${p.slug}${p.public ? "" : `?t=${p.token ?? ""}`}`;
  }

  function blank(): StatusPage {
    return { id: 0, slug: "", title: "", public: true, domain: "", config: { groups: [{ name: "", monitors: [] }] }, created_at: 0 };
  }

  function toggle(g: { monitors: number[] }, id: number): void {
    g.monitors = g.monitors.includes(id) ? g.monitors.filter((x) => x !== id) : [...g.monitors, id];
  }
</script>

<section class="card">
  <div class="row">
    <h2>{t("set.status")}</h2>
    <span class="spacer"></span>
    {#if can("admin")}<button class="primary" onclick={() => (edit = blank())}>{t("set.add")}</button>{/if}
  </div>
  {#if edit}
    {@const e = edit}
    <div class="form">
      <div class="row">
        <label class="field">{t("st.slug")} <input bind:value={e.slug} placeholder="home" /></label>
        <label class="field">{t("st.title")} <input bind:value={e.title} /></label>
        <label><input type="checkbox" bind:checked={e.public} /> {t("st.public")}</label>
      </div>
      <label class="field">{t("st.description")} <input bind:value={e.config.description} /></label>
      <div class="row">
        <label class="field">{t("st.domain")} <input bind:value={e.domain} placeholder="status.example.com" /></label>
        <label class="field">{t("st.accent")} <input bind:value={e.config.accent} placeholder="#1f5fd6" /></label>
        <label class="field">{t("st.logo")} <input bind:value={e.config.logo_url} placeholder="https://…" /></label>
        <label class="field"
          >{t("st.theme")}
          <select bind:value={e.config.theme}>
            <option value="">auto</option><option value="light">light</option><option value="dark">dark</option>
          </select>
        </label>
      </div>
      {#each e.config.groups as g, i (i)}
        <div class="group">
          <div class="row">
            <input bind:value={g.name} placeholder={t("svc.group")} />
            <button class="danger" onclick={() => (e.config.groups = e.config.groups.filter((_, j) => j !== i))}>✕</button>
          </div>
          <div class="row small">
            {#each monitors as m (m.id)}
              <label><input type="checkbox" checked={g.monitors.includes(m.id)} onchange={() => toggle(g, m.id)} /> {m.name}</label>
            {/each}
          </div>
        </div>
      {/each}
      <div class="row">
        <button onclick={() => (e.config.groups = [...e.config.groups, { name: "", monitors: [] }])}>{t("st.add_group")}</button>
        <span class="spacer"></span>
        <button
          class="primary"
          onclick={() =>
            void act(async () => {
              await api.saveStatusPage(e);
              edit = null;
            })}>{t("common.save")}</button
        >
        <button onclick={() => (edit = null)}>{t("common.cancel")}</button>
      </div>
    </div>
  {/if}
  {#if pages.length === 0 && !edit}<p class="muted">{t("st.none")}</p>{/if}
  <table>
    <tbody>
      {#each pages as p (p.id)}
        <tr>
          <td><b>{p.title}</b><div class="muted small">{p.public ? t("st.public") : t("st.link_only")}{p.domain ? ` · ${p.domain}` : ""}</div></td>
          <td class="small"><a href={link(p)} target="_blank" rel="noopener">{link(p)}</a></td>
          <td class="actions">
            {#if can("admin")}
              <button onclick={() => (edit = JSON.parse(JSON.stringify(p)) as StatusPage)}>{t("common.edit")}</button>
              <button class="danger" onclick={() => confirm(t("set.confirm_delete", { name: p.title })) && void act(() => api.deleteStatusPage(p.id))}>
                {t("common.delete")}
              </button>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</section>

<style>
  .form {
    display: grid;
    gap: 8px;
    margin-bottom: 12px;
  }
  .group {
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 8px;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
</style>
