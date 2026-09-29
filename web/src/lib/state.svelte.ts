import { translate, type Lang } from "./dict";
import type { Progress, User } from "./types";

function stored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function store(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // private mode: preferences are not persisted
  }
}

const initialLang: Lang =
  (stored("lanscape-lang") as Lang | null) ?? (navigator.language.toLowerCase().startsWith("ru") ? "ru" : "en");
const initialTheme =
  stored("lanscape-theme") ?? (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");

/** Global UI state (Svelte 5 runes). */
export const ui = $state({
  lang: initialLang,
  theme: initialTheme,
  user: null as User | null,
  version: "",
  route: location.hash.slice(1) || "/network",
  progress: null as Progress | null,
  offline: false,
  toast: "",
});

export function t(key: string, params?: Record<string, string | number>): string {
  return translate(ui.lang, key, params);
}

export function setLang(l: Lang): void {
  ui.lang = l;
  document.documentElement.lang = l;
  store("lanscape-lang", l);
}

export function setTheme(th: string): void {
  ui.theme = th;
  document.documentElement.dataset.theme = th;
  store("lanscape-theme", th);
}

export function navigate(path: string): void {
  location.hash = path;
}

window.addEventListener("hashchange", () => {
  ui.route = location.hash.slice(1) || "/network";
});

export function can(role: "viewer" | "operator" | "admin"): boolean {
  const rank = { viewer: 1, operator: 2, admin: 3 };
  return !!ui.user && rank[ui.user.role] >= rank[role];
}

let toastTimer: ReturnType<typeof setTimeout> | undefined;
export function toast(msg: string): void {
  ui.toast = msg;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (ui.toast = ""), 4000);
}
