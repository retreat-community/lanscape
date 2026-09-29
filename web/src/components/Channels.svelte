<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { t, toast } from "../lib/state.svelte";
  import type { Channel, ChannelType } from "../lib/types";

  type Field = { key: string; label: string; type?: "text" | "password" | "number" | "select"; options?: string[]; placeholder?: string };

  const fields: Record<ChannelType, Field[]> = {
    telegram: [
      { key: "bot_token", label: "ch.bot_token", type: "password" },
      { key: "chat_id", label: "ch.chat_id" },
    ],
    webhook: [
      { key: "url", label: "ch.url", placeholder: "https://example.com/hook" },
      { key: "secret", label: "ch.secret", type: "password" },
    ],
    email: [
      { key: "host", label: "ch.host", placeholder: "smtp.example.com" },
      { key: "port", label: "ch.port", type: "number", placeholder: "587" },
      { key: "tls", label: "ch.tls", type: "select", options: ["starttls", "tls", "none"] },
      { key: "username", label: "ch.username" },
      { key: "password", label: "ch.password", type: "password" },
      { key: "from", label: "ch.from", placeholder: "lanscape@example.com" },
      { key: "to", label: "ch.to" },
    ],
    ntfy: [
      { key: "url", label: "ch.server_url", placeholder: "https://ntfy.sh" },
      { key: "topic", label: "ch.topic" },
      { key: "token", label: "ch.token", type: "password" },
    ],
  };

  let channels = $state<Channel[]>([]);
  let edit = $state<{ id?: number; name: string; type: ChannelType; enabled: boolean; cfg: Record<string, string>; quiet: string; repeat: number; noResolved: boolean } | null>(null);

  async function load(): Promise<void> {
    channels = await api.channels();
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

  function start(c?: Channel): void {
    const cfg: Record<string, string> = {};
    for (const [k, v] of Object.entries(c?.config ?? {})) {
      cfg[k] = Array.isArray(v) ? v.join(", ") : v === null || v === undefined ? "" : String(v);
    }
    edit = {
      id: c?.id,
      name: c?.name ?? "",
      type: c?.type ?? "telegram",
      enabled: c?.enabled ?? true,
      cfg,
      quiet: cfg.quiet ?? "",
      repeat: Number(cfg.repeat_minutes ?? 0),
      noResolved: cfg.no_resolved === "true",
    };
  }

  function save(): void {
    const e = edit;
    if (!e) return;
    const config: Record<string, unknown> = {};
    for (const f of fields[e.type]) {
      const v = (e.cfg[f.key] ?? "").trim();
      if (!v) continue;
      config[f.key] = f.type === "number" ? Number(v) : f.key === "to" ? v.split(/[\s,]+/).filter(Boolean) : v;
    }
    if (e.quiet.trim()) config.quiet = e.quiet.trim();
    if (Number(e.repeat) > 0) config.repeat_minutes = Number(e.repeat);
    if (e.noResolved) config.no_resolved = true;
    void act(async () => {
      await api.saveChannel({ id: e.id, name: e.name, type: e.type, enabled: e.enabled, config });
      edit = null;
    });
  }
</script>

<section class="card">
  <div class="row">
    <h2>{t("set.notifications")}</h2>
    <span class="spacer"></span>
    <button class="primary" onclick={() => start()} data-testid="ch-new">{t("set.add")}</button>
  </div>
  {#if edit}
    <div class="form">
      <div class="row">
        <label class="field"
          >{t("ch.type")}
          <select bind:value={edit.type} disabled={!!edit.id}>
            {#each Object.keys(fields) as ty (ty)}<option value={ty}>{ty}</option>{/each}
          </select>
        </label>
        <label class="field">{t("set.name")} <input bind:value={edit.name} /></label>
        <label><input type="checkbox" bind:checked={edit.enabled} /> {t("set.enabled")}</label>
      </div>
      {#each fields[edit.type] as f (f.key)}
        <label class="field"
          >{t(f.label)}
          {#if f.type === "select"}
            <select bind:value={edit.cfg[f.key]}>
              {#each f.options ?? [] as o (o)}<option>{o}</option>{/each}
            </select>
          {:else}
            <input type={f.type === "password" ? "password" : "text"} bind:value={edit.cfg[f.key]} placeholder={f.placeholder ?? ""} autocomplete="off" />
          {/if}
        </label>
      {/each}
      <div class="row">
        <label class="field">{t("ch.quiet")} <input bind:value={edit.quiet} placeholder="22:00-07:00" /></label>
        <label class="field">{t("ch.repeat")} <input type="number" min="0" bind:value={edit.repeat} /></label>
      </div>
      <label class="small"><input type="checkbox" bind:checked={edit.noResolved} /> {t("ch.no_resolved")}</label>
      <div class="row">
        <button class="primary" onclick={save}>{t("common.save")}</button>
        <button onclick={() => (edit = null)}>{t("common.cancel")}</button>
      </div>
    </div>
  {/if}
  {#if channels.length === 0 && !edit}
    <p class="muted">{t("ch.none")}</p>
  {/if}
  <table>
    <tbody>
      {#each channels as c (c.id)}
        <tr>
          <td><span class="dot {c.enabled ? 'v-green' : 'v-none'}"></span></td>
          <td><b>{c.name}</b></td>
          <td><span class="tag">{c.type}</span></td>
          <td class="actions">
            <button onclick={() => void act(async () => (await api.testChannel(c.id), toast(t("ch.sent"))))}>{t("ch.test")}</button>
            <button onclick={() => start(c)}>{t("common.edit")}</button>
            <button class="danger" onclick={() => confirm(t("set.confirm_delete", { name: c.name })) && void act(() => api.deleteChannel(c.id))}>
              {t("common.delete")}
            </button>
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
    max-width: 640px;
  }
  .actions {
    text-align: right;
  }
</style>
