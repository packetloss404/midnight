import { expect, test } from "@playwright/test";
import { join } from "node:path";

const projectID = process.env.MIDNIGHT_E2E_PROJECT_ID;
const environmentID = process.env.MIDNIGHT_E2E_ENVIRONMENT_ID;
const serviceID = process.env.MIDNIGHT_E2E_SERVICE_ID;
const serviceName = process.env.MIDNIGHT_E2E_SERVICE_NAME || "Parity API";
const evidenceDir = process.env.MIDNIGHT_E2E_EVIDENCE_DIR;

test.skip(
  !projectID || !environmentID || !serviceID,
  "live API resource IDs are supplied by parity-ui-acceptance.sh",
);

test("uses the seeded real API for mobile grid, map, and inspector flows", async ({
  page,
}) => {
  const apiResponses: Array<{ path: string; status: number }> = [];
  page.on("response", (response) => {
    const url = new URL(response.url());
    if (url.pathname.startsWith("/api/v1alpha/")) {
      apiResponses.push({ path: url.pathname, status: response.status() });
    }
  });
  await page.addInitScript(() => {
    window.localStorage.setItem("midnight.agent.collapsed", "1");
    window.localStorage.setItem("midnight.agent.pinned", "0");
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/projects/${projectID}?env=${environmentID}`);

  await expect(page.getByRole("heading", { name: "Resource grid" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: `Open ${serviceName}` }),
  ).toBeVisible();
  await expect(page.getByText("not deployed", { exact: true })).toBeVisible();
  if (evidenceDir) {
    await page.screenshot({
      path: join(evidenceDir, "mobile-grid.png"),
      fullPage: true,
      animations: "disabled",
    });
  }
  await expect
    .poll(() =>
      apiResponses.some(
        (response) =>
          response.path ===
            `/api/v1alpha/environments/${environmentID}/resources` &&
          response.status === 200,
      ),
    )
    .toBe(true);

  const beforeMap = apiResponses.filter((response) =>
    response.path.endsWith("/deployments/latest-active"),
  );
  expect(beforeMap).toHaveLength(0);

  await page.getByRole("button", { name: "Map" }).click();
  await expect(page).toHaveURL(/view=map/);
  await expect(page.getByRole("region", { name: "Project canvas" })).toBeVisible();
  if (evidenceDir) {
    await page.screenshot({
      path: join(evidenceDir, "mobile-map.png"),
      fullPage: true,
      animations: "disabled",
    });
  }
  await page.getByRole("button", { name: "Grid" }).click();

  await page
    .getByRole("button", { name: `Open ${serviceName}` })
    .click();
  await expect(page).toHaveURL(
    new RegExp(`/projects/${projectID}/services/${serviceID}/overview`),
  );
  await expect(
    page.getByRole("complementary", { name: serviceName }),
  ).toBeVisible();
  await expect(
    page.getByRole("complementary", { name: serviceName }),
  ).toHaveCSS("opacity", "1");
  await expect(
    page.getByRole("heading", { name: "No deployment recorded" }),
  ).toBeVisible();
  if (evidenceDir) {
    await page.waitForTimeout(250);
    await page.screenshot({
      path: join(evidenceDir, "mobile-inspector.png"),
      fullPage: true,
      animations: "disabled",
    });
  }

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth + 1);

  await page.setViewportSize({ width: 1440, height: 960 });
  await page.goto(`/projects/${projectID}?env=${environmentID}`);
  await page
    .getByRole("button", { name: `Open ${serviceName}` })
    .click();
  await expect(
    page.getByRole("complementary", { name: serviceName }),
  ).toBeVisible();
  await expect(
    page.getByRole("complementary", { name: serviceName }),
  ).toHaveCSS("opacity", "1");
  await expect(page.getByRole("heading", { name: "Resource grid" })).toBeVisible();
  if (evidenceDir) {
    await page.waitForTimeout(250);
    await page.screenshot({
      path: join(evidenceDir, "desktop-split-inspector.png"),
      fullPage: true,
      animations: "disabled",
    });
  }
});
