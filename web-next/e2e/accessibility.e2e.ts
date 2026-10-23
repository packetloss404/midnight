import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

test("has no serious or critical automated accessibility violations", async ({
  page,
}) => {
  await openDeterministicConsole(page);
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();

  const results = await new AxeBuilder({ page })
    .include("#root")
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const blocking = results.violations.filter(
    (violation) =>
      violation.impact === "critical" || violation.impact === "serious",
  );

  expect(
    blocking,
    blocking
      .map(
        (violation) =>
          `${violation.id}: ${violation.help} (${violation.nodes.length} nodes)`,
      )
      .join("\n"),
  ).toEqual([]);
});

for (const viewport of [
  { name: "compact mobile", width: 320, height: 720 },
  { name: "mobile", width: 390, height: 844 },
  { name: "desktop", width: 1440, height: 900 },
] as const) {
  test(`keeps the human service setup accessible at ${viewport.name} width`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await openDeterministicConsole(
      page,
      "/projects/project_gate0/services/svc_api/overview?env=env_production",
      { operatorRecovery: false },
    );
    await expect(
      page.getByRole("heading", { name: "dep_api", exact: true }),
    ).toBeVisible();

    const dimensions = await page.evaluate(() => ({
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth,
    }));
    expect(dimensions.scrollWidth).toBeLessThanOrEqual(
      dimensions.clientWidth + 1,
    );

    const results = await new AxeBuilder({ page })
      .include(".service-inspector")
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
      .analyze();
    const blocking = results.violations.filter(
      (violation) =>
        violation.impact === "critical" || violation.impact === "serious",
    );
    expect(
      blocking,
      blocking
        .map(
          (violation) =>
            `${violation.id}: ${violation.help} (${violation.nodes.length} nodes)`,
        )
        .join("\n"),
    ).toEqual([]);
  });
}

test("preserves service state in forced colors and reduced motion", async ({
  page,
}) => {
  await page.emulateMedia({
    forcedColors: "active",
    reducedMotion: "reduce",
  });
  await page.setViewportSize({ width: 320, height: 720 });
  await openDeterministicConsole(
    page,
    "/projects/project_gate0/services/svc_api/overview?env=env_production",
    { operatorRecovery: false },
  );

  const inspector = page.getByRole("complementary", { name: "API" });
  await expect(inspector).toBeVisible();
  await expect(
    inspector.getByText(/is serving traffic/).first(),
  ).toBeVisible();
  await expect(
    inspector.getByText("New rollout (not serving)", { exact: false }),
  ).toBeVisible();

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth + 1);
});
