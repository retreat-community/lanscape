import { svelte } from "@sveltejs/vite-plugin-svelte";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";

const outDir = resolve(fileURLToPath(new URL(".", import.meta.url)), "../internal/server/webdist");

// Keeps the placeholder so the Go embed directive works in a fresh checkout.
const keepPlaceholder: Plugin = {
  name: "keep-gitkeep",
  closeBundle() {
    writeFileSync(resolve(outDir, ".gitkeep"), "");
  },
};

// Application icons (Simple Icons, CC0) for the signatures of the built-in library only.
const iconsModule = "virtual:app-icons";
const appIcons: Plugin = {
  name: "app-icons",
  resolveId(id) {
    return id === iconsModule ? `\0${iconsModule}` : undefined;
  },
  load(id) {
    if (id !== `\0${iconsModule}`) return undefined;
    const root = fileURLToPath(new URL(".", import.meta.url));
    const sigs = JSON.parse(readFileSync(resolve(root, "../internal/fingerprint/signatures.json"), "utf8")) as { icon?: string }[];
    const meta = JSON.parse(readFileSync(resolve(root, "node_modules/simple-icons/data/simple-icons.json"), "utf8")) as {
      slug: string;
      hex: string;
    }[];
    const hex = new Map(meta.map((m) => [m.slug, m.hex]));
    const out: Record<string, [string, string]> = {};
    for (const s of sigs) {
      if (!s.icon || out[s.icon]) continue;
      const file = resolve(root, "node_modules/simple-icons/icons", `${s.icon}.svg`);
      if (!existsSync(file)) continue;
      const d = /<path d="([^"]+)"/.exec(readFileSync(file, "utf8"));
      if (d) out[s.icon] = [hex.get(s.icon) ?? "888888", d[1]];
    }
    return `export default ${JSON.stringify(out)};`;
  },
};

export default defineConfig({
  plugins: [svelte(), keepPlaceholder, appIcons],
  build: {
    outDir,
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/metrics": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
