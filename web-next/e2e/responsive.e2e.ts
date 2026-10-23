import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

const VIEWPORTS = [
  { name: "narrow mobile", width: 360, height: 800 },
  { name: "wide desktop", width: 1440, height: 900 },
] as const;

for (const viewport of VIEWPORTS) {
  test(`keeps the primary shell usable at ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize({
      width: viewport.width,
      height: viewport.height,
    });
    await openDeterministicConsole(page);

    await expect(page.getByRole("navigation", { name: "Primary" })).toBeVisible();
    await expect(page.getByRole("main")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();

    const dimensions = await page.evaluate(() => ({
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth,
    }));
    expect(dimensions.scrollWidth).toBeLessThanOrEqual(
      dimensions.clientWidth + 1,
    );
  });
}

test("persists the desktop sidebar collapse preference", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openDeterministicConsole(page);

  const primary = page.getByRole("navigation", { name: "Primary" });
  await page.getByRole("button", { name: "Collapse sidebar" }).click();
  await expect(primary).toHaveAttribute("data-compact", "true");
  await expect(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();

  await page.reload();
  await expect(page.getByRole("navigation", { name: "Primary" })).toHaveAttribute(
    "data-compact",
    "true",
  );
  await page.getByRole("button", { name: "Expand sidebar" }).click();
  await expect(page.getByRole("navigation", { name: "Primary" })).toHaveAttribute(
    "data-compact",
    "false",
  );
});
