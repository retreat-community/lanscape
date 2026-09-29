import { expect, test } from "@playwright/test";

// Runs against the Full server started by test/e2e/full.sh (TestFull with E2E_KEEP=1).
const base = process.env.FULL_URL ?? "http://127.0.0.1:18090";
const shots = process.env.SCREENSHOTS ?? "../e2e/screenshots";

test("full: login, check all, matrix and map", async ({ page }) => {
  await page.goto(base + "/");
  await page.locator('input[name="username"]').fill("admin");
  await page.locator('input[name="password"]').fill("e2e-admin-password");
  await page.locator('button[type="submit"]').click();
  const check = page.getByTestId("check-all");
  await expect(check).toBeVisible();
  // shorter tests for the smoke run
  await page.getByRole("button", { name: /Options|Параметры/ }).click();
  await page.locator('input[type="number"]').first().fill("1");
  await expect(check).toBeEnabled({ timeout: 60_000 });
  await check.click();
  await expect(page.getByTestId("progress")).toBeVisible();
  await page.screenshot({ path: `${shots}/full-running.png`, fullPage: true });
  await expect(page.getByTestId("progress")).toBeHidden({ timeout: 14 * 60 * 1000 });
  await expect(page.getByTestId("matrix")).toBeVisible();
  await expect(page.locator('[data-testid="matrix"] td.cell').first()).toBeVisible();
  await expect(page.getByTestId("problems")).toBeVisible();
  await page.screenshot({ path: `${shots}/full-network.png`, fullPage: true });

  await page.goto(base + "/#/map");
  await expect(page.getByTestId("map").locator("canvas").first()).toBeVisible({ timeout: 30_000 });
  await page.waitForTimeout(2000);
  await page.screenshot({ path: `${shots}/full-map.png`, fullPage: true });

  await page.goto(base + "/#/devices");
  await expect(page.locator("table").first()).toBeVisible();
  await page.getByTestId("lang").click();
  await page.goto(base + "/#/network");
  await expect(page.getByTestId("check-all")).toHaveText("Проверить всё");
  await page.screenshot({ path: `${shots}/full-network-ru.png`, fullPage: true });
});
