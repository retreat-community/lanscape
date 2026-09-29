import { mount } from "svelte";
import "./app.css";
import App from "./App.svelte";
import { ui } from "./lib/state.svelte";

document.documentElement.dataset.theme = ui.theme;
document.documentElement.lang = ui.lang;

const app = mount(App, { target: document.getElementById("app")! });

if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {
    // service workers need a secure context; the app works without one
  });
}

export default app;
