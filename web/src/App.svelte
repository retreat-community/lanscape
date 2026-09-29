<script lang="ts">
  import { onMount } from "svelte";
  import { api, setUnauthorizedHandler } from "./lib/api";
  import { can, navigate, setLang, setTheme, t, ui } from "./lib/state.svelte";
  import Palette from "./components/Palette.svelte";
  import Account from "./pages/Account.svelte";
  import Changes from "./pages/Changes.svelte";
  import Dashboard from "./pages/Dashboard.svelte";
  import Devices from "./pages/Devices.svelte";
  import Login from "./pages/Login.svelte";
  import Network from "./pages/Network.svelte";
  import Monitors from "./pages/Monitors.svelte";
  import Runs from "./pages/Runs.svelte";
  import Services from "./pages/Services.svelte";
  import Settings from "./pages/Settings.svelte";

  let phase = $state<"loading" | "setup" | "login" | "guest" | "app">("loading");

  const nav = [
    { path: "/dashboard", key: "nav.dashboard" },
    { path: "/services", key: "nav.services" },
    { path: "/monitors", key: "nav.monitors" },
    { path: "/network", key: "nav.network" },
    { path: "/map", key: "nav.map" },
    { path: "/devices", key: "nav.devices" },
    { path: "/changes", key: "nav.changes" },
    { path: "/runs", key: "nav.runs" },
    { path: "/settings", key: "nav.settings" },
  ];

  const page = $derived(ui.route.split("?")[0]);
  const runId = $derived(page.startsWith("/runs/") ? Number(page.slice(6)) || 0 : 0);

  async function init(): Promise<void> {
    try {
      const me = await api.me();
      ui.user = me.user;
      ui.version = me.version;
      phase = "app";
      ui.offline = false;
    } catch (e) {
      if (!navigator.onLine) {
        ui.offline = true;
      }
      try {
        const st = await api.setupState();
        ui.version = st.version;
        phase = st.needs_setup ? "setup" : st.guest ? "guest" : "login";
      } catch {
        phase = "login";
      }
      void e;
    }
  }

  async function logout(): Promise<void> {
    await api.logout();
    ui.user = null;
    phase = "login";
  }

  onMount(() => {
    setUnauthorizedHandler(() => {
      ui.user = null;
      phase = "login";
    });
    window.addEventListener("online", () => (ui.offline = false));
    window.addEventListener("offline", () => (ui.offline = true));
    void init();
  });
</script>

{#if phase === "loading"}
  <p class="muted pad">{t("common.loading")}</p>
{:else if phase === "setup" || phase === "login"}
  <Login setup={phase === "setup"} onauth={init} />
{:else if phase === "guest"}
  <header>
    <a class="brand" href="#/dashboard"><img src="/icon.svg" alt="" width="22" height="22" /> Lanscape</a>
    <span class="grow"></span>
    <button class="primary" onclick={() => (phase = "login")}>{t("login.submit")}</button>
  </header>
  <main><Dashboard /></main>
{:else}
  <header>
    <a class="brand" href="#/dashboard"><img src="/icon.svg" alt="" width="22" height="22" /> Lanscape</a>
    <nav>
      {#each nav as n (n.path)}
        <a href="#{n.path}" class:on={page.startsWith(n.path)}>{t(n.key)}</a>
      {/each}
    </nav>
    <span class="spacer"></span>
    <Palette />
    <button title={t("theme.toggle")} onclick={() => setTheme(ui.theme === "dark" ? "light" : "dark")}>
      {ui.theme === "dark" ? "☀" : "☾"}
    </button>
    <button data-testid="lang" onclick={() => setLang(ui.lang === "en" ? "ru" : "en")}>{t("lang.toggle")}</button>
    <a href="#/account" class="user">{ui.user?.username} <span class="tag">{ui.user?.role}</span></a>
    <button onclick={logout}>{t("nav.logout")}</button>
  </header>
  {#if ui.offline}
    <div class="offline">{t("common.offline_data")}</div>
  {/if}
  <main>
    {#if page.startsWith("/map")}
      {#await import("./pages/MapPage.svelte")}
        <p class="muted">{t("common.loading")}</p>
      {:then m}
        <m.default />
      {/await}
    {:else if page.startsWith("/services")}
      <Services route={ui.route} />
    {:else if page.startsWith("/monitors")}
      {#key page}<Monitors route={ui.route} />{/key}
    {:else if page.startsWith("/changes")}
      <Changes />
    {:else if page.startsWith("/network")}
      <Network />
    {:else if page.startsWith("/devices")}
      <Devices />
    {:else if page.startsWith("/runs")}
      <Runs id={runId} />
    {:else if page.startsWith("/settings") && can("viewer")}
      <Settings />
    {:else if page.startsWith("/account")}
      <Account />
    {:else}
      <Dashboard />
    {/if}
  </main>
  <footer class="muted small">Lanscape {ui.version}</footer>
{/if}

{#if ui.toast}
  <div class="toast" role="status">{ui.toast}</div>
{/if}

<svelte:window onkeydown={(e) => e.key === "g" && e.altKey && navigate("/dashboard")} />

<style>
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    padding: 8px 16px;
    background: var(--card);
    border-bottom: 1px solid var(--line);
    position: sticky;
    top: 0;
    z-index: 5;
  }
  .brand {
    display: flex;
    gap: 6px;
    align-items: center;
    font-weight: 700;
    color: var(--fg);
    text-decoration: none;
    margin-right: 12px;
  }
  nav {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  nav a {
    padding: 6px 10px;
    border-radius: 6px;
    color: var(--fg);
    text-decoration: none;
  }
  nav a.on {
    background: color-mix(in srgb, var(--accent) 15%, transparent);
    color: var(--accent);
    font-weight: 600;
  }
  .user {
    color: var(--fg);
    text-decoration: none;
  }
  main {
    max-width: 1400px;
    margin: 0 auto;
    padding: 16px;
  }
  footer {
    text-align: center;
    padding: 16px;
  }
  .pad {
    padding: 24px;
  }
  .grow {
    flex: 1;
  }
  .offline {
    background: var(--yellow);
    color: #000;
    text-align: center;
    padding: 4px;
  }
  .toast {
    position: fixed;
    bottom: 16px;
    left: 50%;
    transform: translateX(-50%);
    background: var(--fg);
    color: var(--bg);
    padding: 8px 14px;
    border-radius: 8px;
    z-index: 50;
    max-width: 90vw;
  }
</style>
