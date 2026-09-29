<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { bytes, duration, when } from "../lib/format";
  import { t, toast, ui } from "../lib/state.svelte";
  import type { Agent, DiscoveredDevice, IPAM } from "../lib/types";

  let agents = $state<Agent[]>([]);
  let ipam = $state<IPAM[]>([]);
  let open = $state<string | null>(null);
  let tab = $state<"agents" | "discovered" | "ipam">("agents");
  let found = $state<DiscoveredDevice[]>([]);
  const foundShown = $derived(
    found.filter((d) => !q || [d.ip, d.mac, d.name, d.vendor, d.model].some((f) => f?.toLowerCase().includes(q.toLowerCase()))),
  );
  let q = $state("");

  const filtered = $derived(
    agents.filter((a) => {
      if (!q) return true;
      const s = q.toLowerCase();
      return (
        a.name.toLowerCase().includes(s) ||
        (a.inventory.ifaces ?? []).some(
          (i) => i.mac.toLowerCase().includes(s) || (i.addrs ?? []).some((ad) => ad.ip.includes(s)),
        )
      );
    }),
  );

  onMount(async () => {
    try {
      [agents, ipam, found] = await Promise.all([api.agents(), api.ipam(), api.discoveredDevices()]);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  });
</script>

<div class="grid">
  <div class="tabs">
    <button class:on={tab === "agents"} onclick={() => (tab = "agents")}>{t("dev.agents")}</button>
    <button class:on={tab === "discovered"} onclick={() => (tab = "discovered")}>{t("dev.discovered")} ({found.length})</button>
    <button class:on={tab === "ipam"} onclick={() => (tab = "ipam")}>{t("dev.ipam")}</button>
    <span class="spacer"></span>
    <input placeholder={t("common.search")} bind:value={q} />
  </div>

  {#if tab === "agents"}
    <section class="card">
      <table>
        <thead>
          <tr><th></th><th>{t("set.name")}</th><th>{t("dev.kind")}</th><th>{t("dev.env")}</th><th>OS</th><th>{t("dev.uptime")}</th><th></th></tr>
        </thead>
        <tbody>
          {#each filtered as a (a.id)}
            <tr>
              <td><span class="dot {a.online ? 'v-green' : 'v-red'}"></span></td>
              <td><b>{a.name}</b><div class="muted small">{a.hostname} · {a.version}</div></td>
              <td>{a.kind === "lite" ? t("dev.lite") : a.kind} · {a.arch}</td>
              <td>{a.inventory.env?.kind ?? "—"}{a.inventory.env?.virt ? ` (${a.inventory.env.virt})` : ""}</td>
              <td>{a.inventory.resources?.os ?? a.os}</td>
              <td>{a.inventory.resources?.uptime_s ? duration(a.inventory.resources.uptime_s) : "—"}</td>
              <td><button onclick={() => (open = open === a.id ? null : a.id)}>{t("dev.interfaces")}</button></td>
            </tr>
            {#if open === a.id}
              <tr>
                <td colspan="7">
                  <table class="small">
                    <thead><tr><th>{t("set.name")}</th><th>IP</th><th>MAC</th><th>{t("dev.kind")}</th><th>VLAN</th><th>MTU</th><th>{t("dev.speed")}</th></tr></thead>
                    <tbody>
                      {#each a.inventory.ifaces ?? [] as i (i.name)}
                        <tr>
                          <td><span class="dot {i.carrier ? 'v-green' : 'v-red'}"></span>{i.name}{i.parent ? ` ← ${i.parent}` : ""}{i.master ? ` → ${i.master}` : ""}</td>
                          <td>{(i.addrs ?? []).map((ad) => `${ad.ip}/${ad.prefix}`).join(", ")}</td>
                          <td>{i.mac}</td>
                          <td>{i.kind}{i.bond_mode ? ` (${i.bond_mode})` : ""}</td>
                          <td>{i.vlan || ""}</td>
                          <td>{i.mtu}</td>
                          <td>{i.speed ? `${i.speed} Mbit/s` : t("net.unknown")}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                  {#if a.inventory.resources}
                    {@const r = a.inventory.resources}
                    <h3>{t("dev.resources")}</h3>
                    <div class="small">
                      {r.cpus} CPU {r.cpu_model ?? ""} · {t("dev.memory")} {bytes(r.mem_total - r.mem_available)} / {bytes(r.mem_total)}
                      · load {r.load1} · {r.kernel}{r.temp_c ? ` · ${r.temp_c.toFixed(0)}°C` : ""}
                    </div>
                    {#each r.disks ?? [] as d (d.mount)}
                      <div class="small">{d.mount} ({d.fs}) {bytes(d.used)} / {bytes(d.total)}</div>
                    {/each}
                  {/if}
                  {#if a.inventory.routes?.length}
                    <h3>{t("dev.routes")}</h3>
                    <pre>{a.inventory.routes.map((r) => `${r.dst}${r.gateway ? ` via ${r.gateway}` : ""} dev ${r.dev}${r.table !== 254 ? ` table ${r.table}` : ""}`).join("\n")}</pre>
                  {/if}
                  {#if a.inventory.rules?.length}
                    <h3>{t("dev.rules")}</h3>
                    <pre>{a.inventory.rules.join("\n")}</pre>
                  {/if}
                  <div class="muted small">{when(a.last_seen, ui.lang)}</div>
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </section>
  {:else if tab === "discovered"}
    <section class="card">
      {#if foundShown.length === 0}
        <p class="muted">{t("dev.none_discovered")}</p>
      {:else}
        <table>
          <thead>
            <tr><th>IP</th><th>{t("set.name")}</th><th>{t("dev.kind")}</th><th>MAC</th><th>{t("dev.vendor")}</th><th>{t("dev.sources")}</th></tr>
          </thead>
          <tbody>
            {#each foundShown as d (d.ip)}
              <tr>
                <td>{#if d.url}<a href={d.url} target="_blank" rel="noopener">{d.ip}</a>{:else}{d.ip}{/if}</td>
                <td><b>{d.name ?? ""}</b>{#if d.model}<div class="muted small">{d.model}</div>{/if}</td>
                <td>{d.type}</td>
                <td class="small">{d.mac ?? ""}</td>
                <td class="small">{d.vendor ?? ""}</td>
                <td class="small">{d.sources.join(", ")}{d.seen_by.length ? ` · ${d.seen_by.join(", ")}` : ""}{d.wifi ? ` · ${d.wifi}` : ""}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
  {:else}
    {#each ipam as s (s.segment)}
      <section class="card">
        <div class="row">
          <h2>{s.segment}</h2>
          <span class="spacer"></span>
          <span class="muted">{t("dev.used")} {s.used} · {t("dev.free")} {s.free} / {s.size}{s.pool_size ? ` · ${t("dev.pool")} ${s.pool_size}` : ""}</span>
        </div>
        <table class="small">
          <tbody>
            {#each s.entries as e (e.ip)}
              <tr>
                <td>{e.ip}</td>
                <td>{e.node ?? e.name ?? ""}</td>
                <td>{e.iface ?? ""}</td>
                <td>{e.mac ?? ""}</td>
                <td><span class="tag">{e.source}</span>{e.in_pool ? " DHCP" : ""}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </section>
    {/each}
  {/if}
</div>
