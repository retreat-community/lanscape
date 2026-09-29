import { api } from "./api";
import { t, toast } from "./state.svelte";
import type { ActionResponse } from "./types";

function report(r: ActionResponse): void {
  const detail = r.results.map((x) => `${x.agent}: ${x.detail}`).join("; ");
  toast(r.ok ? t("act.done", { detail }) : t("act.failed", { detail }));
}

async function run(fn: () => Promise<ActionResponse>): Promise<void> {
  try {
    report(await fn());
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  }
}

// wake sends Wake-on-LAN after confirmation; the server picks agents on the device's subnet.
export async function wake(mac: string, ip: string | undefined, name: string): Promise<void> {
  if (!confirm(t("act.wake_confirm", { name: name || mac }))) return;
  await run(() => api.wake(mac, ip));
}

// restart restarts a container, workload or guest after confirmation.
export async function restart(agent: string, source: string, key: string, name: string): Promise<void> {
  if (!confirm(t("act.restart_confirm", { name }))) return;
  await run(() => api.restart(agent, source, key));
}
