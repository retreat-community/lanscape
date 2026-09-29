<script lang="ts">
  import { mbps, ms, rate } from "../lib/format";
  import { buildMatrix, summarize, type MatrixEndpoint } from "../lib/matrix";
  import { t } from "../lib/state.svelte";
  import type { PathResult, Report } from "../lib/types";

  let { report, names }: { report: Report; names: (id: string) => string } = $props();
  let tab = $state(0);
  let selected = $state<PathResult | null>(null);

  const segments = $derived(report.segments ?? []);
  const seg = $derived(segments[Math.min(tab, Math.max(0, segments.length - 1))]);
  const matrix = $derived(seg ? buildMatrix(seg, segments.indexOf(seg), report.paths ?? []) : null);

  function tip(p: PathResult): string {
    const lines = [
      `${t("detail.ping")}: ${p.ping?.recv ?? 0}/${p.ping?.sent ?? 0} ${ms(p.ping?.rtt_avg_us)}`,
      `${t("detail.echo")}: ${p.echo?.status ?? "—"}`,
      `MTU: ${p.mtu?.status ?? "—"}`,
    ];
    if (p.tcp1) lines.push(`${t("detail.tcp1")}: ${rate(p.tcp1.bps)} (${p.tcp1.status})`);
    if (p.tcpn) lines.push(`${t("detail.tcpn", { n: p.tcpn.streams })}: ${rate(p.tcpn.bps)} (${p.tcpn.status})`);
    return lines.join("\n");
  }

  function key(a: MatrixEndpoint, b: MatrixEndpoint): string {
    return `${a.key}>${b.key}`;
  }
</script>

{#if segments.length}
  <div class="tabs" role="tablist">
    {#each segments as s, i (s.id)}
      <button role="tab" class:on={i === tab} aria-selected={i === tab} onclick={() => (tab = i)}>{s.id}</button>
    {/each}
  </div>
  {#if seg && matrix}
    <div class="wrap">
      <table class="matrix" data-testid="matrix">
        <thead>
          <tr>
            <th>{t("net.from_to")}</th>
            {#each matrix.rows as col (col.key)}
              <th>{names(col.node)}<br /><span class="muted small">{col.iface} {col.ip}</span></th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each matrix.rows as row (row.key)}
            <tr>
              <th>
                {names(row.node)}<br /><span class="muted small"
                  >{row.iface}{row.speed ? ` · ${row.speed}M` : ""}</span
                >
              </th>
              {#each matrix.rows as col (key(row, col))}
                {@const p = row.node === col.node ? undefined : matrix.cell(row, col)}
                {#if !p}
                  <td class="muted na">—</td>
                {:else}
                  {@const s = summarize(p)}
                  <td
                    class="cell bg-v v-{s.verdict}"
                    title={tip(p)}
                    onclick={() => (selected = selected === p ? null : p)}
                    onkeydown={(e) => e.key === "Enter" && (selected = p)}
                    tabindex="0"
                    role="button"
                  >
                    <b>{s.bps ? mbps(s.bps) : s.main}</b>
                    <span class="small muted">
                      {s.icmp ? "ICMP ✓" : "ICMP ✗"}{!s.mtu && s.icmp ? " · MTU ✗" : ""}{s.mismatch
                        ? ` · ${t("cell.path")} ✗`
                        : ""}
                    </span>
                  </td>
                {/if}
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="muted small">
      Mbit/s · {t("net.expected", { v: seg.expected_mbps ? `${seg.expected_mbps} Mbit/s` : t("net.unknown") })}
    </p>
  {/if}
  {#if selected}
    {@const p = selected}
    <div class="detail card">
      <div class="row">
        <b>{names(p.src)} ({p.src_if}) → {names(p.dst)} ({p.dst_if})</b>
        <span class="tag">{p.seg_id}</span>
        <span class="spacer"></span>
        <button onclick={() => (selected = null)}>{t("common.close")}</button>
      </div>
      <table>
        <tbody>
          <tr><th>{t("detail.ping")}</th><td>{p.ping?.recv ?? 0}/{p.ping?.sent ?? 0} · avg {ms(p.ping?.rtt_avg_us)} · p95 {ms(p.ping?.rtt_p95_us)} · {t("detail.jitter")} {ms(p.ping?.jitter_us)}</td></tr>
          <tr><th>{t("detail.echo")}</th><td>{p.echo?.status ?? "—"} {ms(p.echo?.rtt_avg_us)}</td></tr>
          <tr><th>MTU</th><td>{p.mtu?.status ?? "—"}{p.jumbo ? ` · jumbo ${p.jumbo.size}: ${p.jumbo.status}` : ""}</td></tr>
          {#each [["detail.tcp1", p.tcp1], ["detail.tcpn", p.tcpn], ["detail.udp", p.udp], ["detail.bidir", p.bidir]] as const as [label, th] (label)}
            {#if th}
              <tr>
                <th>{t(label, { n: th.streams })}</th>
                <td>
                  {th.status} · {rate(th.bps)}{th.bps_reverse ? ` / ${rate(th.bps_reverse)}` : ""}
                  · {t("detail.cpu")} {Math.round(th.cpu_local / 10)}% / {Math.round(th.cpu_peer / 10)}%
                  {#if th.loss_pct}· {t("detail.loss")} {th.loss_pct.toFixed(2)}%{/if}
                  · {th.path_ok ? t("detail.counters") : t("detail.counters_bad")}
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}

<style>
  .wrap {
    overflow: auto;
  }
  .matrix {
    width: auto;
  }
  .matrix th,
  .matrix td {
    border: 1px solid var(--line);
    text-align: center;
    white-space: nowrap;
  }
  .cell {
    min-width: 96px;
    cursor: pointer;
  }
  .cell b {
    display: block;
    font-size: 15px;
  }
  .detail {
    margin-top: 12px;
  }
</style>
