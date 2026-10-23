import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

test("keeps documentation reachable from the rail and within the docs shell", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openDeterministicConsole(page, "/", {
    operatorRecovery: false,
    instanceAdmin: false,
  });

  await page.getByRole("link", { name: "Docs home" }).click();
  await expect(page).toHaveURL(/\/docs$/);
  await expect(
    page.getByRole("heading", {
      name: "Ship software on infrastructure you control.",
    }),
  ).toBeVisible();

  const docs = page.getByRole("navigation", { name: "Documentation" });
  await docs.getByRole("link", { name: "Agent Setup" }).click();
  await expect(page).toHaveURL(/\/docs\/agent-setup$/);
  await expect(
    page.getByRole("heading", { name: "Set up a Midnight agent" }),
  ).toBeVisible();

  await docs.getByRole("link", { name: "API reference" }).click();
  await expect(page).toHaveURL(/\/docs\/api$/);
  await expect(
    page.getByRole("heading", { name: "Practical API reference" }),
  ).toBeVisible();

  await docs.getByRole("link", { name: "CLI reference" }).click();
  await expect(page).toHaveURL(/\/docs\/cli$/);
  await expect(
    page.getByRole("heading", { name: "Midnight CLI" }),
  ).toBeVisible();
});

test("keeps the compact Docs entry and internal navigation usable on mobile", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await openDeterministicConsole(page, "/docs", {
    operatorRecovery: false,
    instanceAdmin: false,
  });

  await expect(
    page.getByRole("link", { name: "Docs home" }).first(),
  ).toBeVisible();
	await expect(
		page.getByRole("navigation", { name: "Documentation" }),
	).toBeVisible();

	await page.goto("/docs/cli");
	const activeCLI = page.getByRole("link", { name: "CLI reference" }).last();
	await expect(activeCLI).toBeVisible();
	await expect(activeCLI).toHaveAttribute("aria-current", "page");
	const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
	expect(dimensions.scrollWidth).toBeLessThanOrEqual(
		dimensions.clientWidth + 1,
	);
});

test("renders a docs typo as not found for an ordinary human", async ({ page }) => {
	await openDeterministicConsole(page, "/docs/internal", {
		operatorRecovery: false,
		instanceAdmin: false,
	});

	await expect(page.getByRole("heading", { name: "Page not found" })).toBeVisible();
	await expect(
		page.getByRole("heading", { name: "Installation administrator required" }),
	).toHaveCount(0);
});
