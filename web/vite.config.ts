import { svelte } from "@sveltejs/vite-plugin-svelte";
import { writeFileSync } from "node:fs";
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

export default defineConfig({
  plugins: [svelte(), keepPlaceholder],
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
