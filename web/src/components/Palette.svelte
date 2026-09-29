<script lang="ts">
  import { tick } from "svelte";
  import { wake } from "../lib/actions";
  import { api } from "../lib/api";
  import { statusClass } from "../lib/format";
  import { can, navigate, t, toast } from "../lib/state.svelte";
  import type { SearchResult } from "../lib/types";

  let open = $state(false);
  let q = $state("");
  let results = $state<SearchResult[]>([]);
  let sel = $state(0);
  let input = $state<HTMLInputElement | null>(null);
  let seq = 0;

  interface Action {
    id: string;
    title: string;
    role: "viewer" | "operator" | "admin";
    run: () => Promise<void> | void;
  }

  const actions: Action[] = [
    {
      id: "check_all",
      title: "pal.action.check_all",
      role: "operator",
      run: async () => {
        await api.startRun({ kind: "full" });
        navigate("/network");
      },
    },
    {
      id: "rescan",
      title: "pal.action.rescan",
      role: "operator",
      run: async () => toast(t("svc.rescan_done", { n: (await api.rescan()).agents })),
    },
    {
      id: "maintenance",
      title: "pal.action.maintenance",
      role: "operator",
      run: async () => {
        await api.createMaintenance({ name: t("mon.maintenance"), duration_min: 30 });
        navigate("/monitors?tab=maintenance");
      },
    },
    { id: "new_monitor", title: "pal.action.new_monitor", role: "operator", run: () => navigate("/monitors?new=1") },
  ];

  type Item = { kind: "result"; r: SearchResult } | { kind: "action"; a: Action };

  const macRe = /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/i;

  // devices with a known MAC get a Wake-on-LAN action next to them
  const wakeActions = $derived<Action[]>(
    can("operator")
      ? results
          .filter((r) => r.type === "device" && macRe.test(r.id))
          .map((r) => ({
            id: `wake:${r.id}`,
            title: t("act.palette_wake", { name: r.title }),
            role: "operator" as const,
            run: () => wake(r.id, r.title, r.title),
          }))
      : [],
  );

  const items = $derived<Item[]>([
    ...results.map((r) => ({ kind: "result" as const, r })),
    ...wakeActions.map((a) => ({ kind: "action" as const, a })),
    ...actions
      .filter((a) => can(a.role) && (!q || t(a.title).toLowerCase().includes(q.toLowerCase())))
      .map((a) => ({ kind: "action" as const, a })),
  ]);

  async function show(): Promise<void> {
    open = true;
    q = "";
    sel = 0;
    void search();
    await tick();
    input?.focus();
  }

  async function search(): Promise<void> {
    const my = ++seq;
    try {
      const r = await api.search(q);
      if (my === seq) {
        results = r;
        sel = 0;
      }
    } catch {
      results = [];
    }
  }

  async function choose(it: Item | undefined, newTab = false): Promise<void> {
    if (!it) return;
    open = false;
    try {
      if (it.kind === "action") {
        await it.a.run();
      } else if (it.r.url && (it.r.type === "service" || newTab)) {
        window.open(it.r.url, "_blank", "noopener");
      } else {
        navigate(it.r.href);
      }
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  }

  function onKey(e: KeyboardEvent): void {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      if (open) open = false;
      else void show();
      return;
    }
    if (!open) return;
    if (e.key === "Escape") open = false;
    else if (e.key === "ArrowDown") {
      e.preventDefault();
      sel = Math.min(sel + 1, items.length - 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      sel = Math.max(sel - 1, 0);
    } else if (e.key === "Enter") {
      e.preventDefault();
      void choose(items[sel], e.shiftKey);
    }
  }

  let debounce: ReturnType<typeof setTimeout> | undefined;
  function onInput(): void {
    clearTimeout(debounce);
    debounce = setTimeout(() => void search(), 150);
  }
</script>

<svelte:window onkeydown={onKey} />

<button class="launcher" onclick={() => void show()} title={t("pal.placeholder")} data-testid="palette-open">
  ⌕ <span class="muted small">{t("pal.hint")}</span>
</button>

{#if open}
  <div class="backdrop" role="presentation" onclick={() => (open = false)}></div>
  <div class="palette" role="dialog" aria-label={t("common.search")} data-testid="palette">
    <input bind:this={input} bind:value={q} oninput={onInput} placeholder={t("pal.placeholder")} />
    <ul>
      {#each items as it, i (it.kind === "action" ? `a:${it.a.id}` : `${it.r.type}:${it.r.id}`)}
        <li class:sel={i === sel}>
          <button onmouseenter={() => (sel = i)} onclick={() => void choose(it)}>
            {#if it.kind === "action"}
              <span class="tag">{t("pal.type.action")}</span> {t(it.a.title)}
            {:else}
              <span class="tag">{t(`pal.type.${it.r.type}`)}</span>
              {#if it.r.status}<span class="dot {statusClass(it.r.status === 'online' ? 'up' : it.r.status === 'offline' ? 'down' : it.r.status)}"></span>{/if}
              <b>{it.r.title}</b>
              <span class="muted small">{it.r.subtitle ?? ""}</span>
            {/if}
          </button>
        </li>
      {:else}
        <li class="muted pad">{t("pal.nothing")}</li>
      {/each}
    </ul>
  </div>
{/if}

<style>
  .launcher {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgb(0 0 0 / 35%);
    z-index: 40;
  }
  .palette {
    position: fixed;
    top: 12vh;
    left: 50%;
    transform: translateX(-50%);
    width: min(640px, 94vw);
    background: var(--card);
    border: 1px solid var(--line);
    border-radius: 12px;
    z-index: 41;
    box-shadow: 0 10px 40px rgb(0 0 0 / 30%);
    overflow: hidden;
  }
  .palette input {
    width: 100%;
    border: 0;
    border-bottom: 1px solid var(--line);
    border-radius: 0;
    padding: 14px;
    font-size: 16px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 4px;
    max-height: 55vh;
    overflow: auto;
  }
  li button {
    width: 100%;
    text-align: left;
    border: 0;
    background: none;
    display: flex;
    gap: 8px;
    align-items: baseline;
    padding: 8px;
  }
  li.sel button {
    background: color-mix(in srgb, var(--accent) 14%, transparent);
  }
  .pad {
    padding: 12px;
  }
</style>
