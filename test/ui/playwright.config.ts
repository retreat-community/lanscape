import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  timeout: 15 * 60 * 1000,
  retries: 0,
  reporter: [["list"]],
  outputDir: "../e2e/out/playwright",
  use: {
    headless: true,
    viewport: { width: 1400, height: 1000 },
    launchOptions: process.env.PW_CHROMIUM ? { executablePath: process.env.PW_CHROMIUM } : {},
  },
});
