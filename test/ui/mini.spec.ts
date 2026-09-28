import { expect, test } from "@playwright/test";

// Runs against the Mini server started by test/e2e/mini.sh on the e2e topology.
const base = process.env.MINI_URL ?? "http://127.0.0.1:18080";
const shots = process.env.SCREENSHOTS ?? "../e2e/screenshots";

test("mini: check all renders matrix and map", async ({ page }) => {
  test.skip(!!process.env.SKIP_MINI, "mini server not running");
  await page.goto(base + "/");
  await expect(page.locator("h1")).toContainText("Lanscape Mini");
  const run = page.locator("#run");
  await expect(run).toBeEnabled();
  await run.click();
  await expect(page.locator("#bar")).toBeVisible();
  await page.screenshot({ path: `${shots}/mini-running.png`, fullPage: true });
  await expect(run).toBeEnabled({ timeout: 14 * 60 * 1000 });
  // matrix: one tab per segment and at least one measured cell
  await expect(page.locator("#tabs button")).toHaveCount(3);
  await expect(page.locator("#matrix td.c").first()).toBeVisible();
  // map: segment buses and agent cards
  await expect(page.locator("#map svg")).toBeVisible();
  expect(await page.locator("#map svg rect").count()).toBeGreaterThan(5);
  await expect(page.locator("#problems li").first()).toBeVisible();
  await page.screenshot({ path: `${shots}/mini.png`, fullPage: true });
  await page.locator("#lang").click();
  await expect(page.locator("#run")).toHaveText("Проверить всё");
  await page.screenshot({ path: `${shots}/mini-ru.png`, fullPage: true });
});
