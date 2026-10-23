import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

test("keeps usage evidence truthful and accessible", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await openDeterministicConsole(page, "/usage", { operatorRecovery: false });

  await expect(page.getByRole("heading", { name: "Usage & limits" })).toBeVisible();
  await expect(page.getByText("Enforced").first()).toBeVisible();
  await expect(page.getByText("Incomplete", { exact: true })).toBeVisible();
  await expect(page.getByText("Unavailable", { exact: true })).toBeVisible();
  await expect(page.getByText("Not measured")).toBeVisible();
  const support = page.getByRole("link", {
    name: "Support on Ko-fi (opens in a new tab)",
  });
  await expect(support).toHaveAttribute("href", "https://ko-fi.com/ianwalmsley");
  await expect(support).toHaveAttribute("target", "_blank");
  await expect(support).toHaveAttribute("rel", /\bnoreferrer\b/);

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth + 1);

  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
});

test("keeps the portfolio operable at a 200-percent-equivalent viewport", async ({
  page,
}) => {
  await page.setViewportSize({ width: 720, height: 450 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await openDeterministicConsole(page);

  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await expect(page.getByRole("link", { name: "New project" })).toBeVisible();
  await page.getByRole("searchbox", { name: "Search projects" }).fill("Gate");
  await expect(page.getByRole("link", { name: "Gate Zero" })).toBeVisible();

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth + 1);
});

test("preserves names, focus, and contrast semantics in forced colors", async ({
  page,
}) => {
  await page.emulateMedia({ forcedColors: "active", reducedMotion: "reduce" });
  await openDeterministicConsole(page);

  const primary = page.getByRole("navigation", { name: "Primary" });
  await expect(primary).toBeVisible();
  await expect(primary.locator("svg").first()).toBeVisible();

  const projects = page.getByRole("link", { name: "Projects" });
  await projects.focus();
  await expect(projects).toBeFocused();
  await expect
    .poll(() => projects.evaluate((element) => getComputedStyle(element).outlineStyle))
    .not.toBe("none");

  const results = await new AxeBuilder({ page })
    .include("#root")
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  expect(
    results.violations.filter(
      (violation) =>
        violation.impact === "critical" || violation.impact === "serious",
    ),
  ).toEqual([]);
});
