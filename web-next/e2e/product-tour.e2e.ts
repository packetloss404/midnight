import { mkdir } from "node:fs/promises";
import { join, resolve } from "node:path";
import { expect, test } from "@playwright/test";
import { openProductTourConsole } from "./support/product-tour";

const requestedCaptureDir = process.env.MIDNIGHT_CAPTURE_DIR?.trim();
const captureDir = requestedCaptureDir
  ? resolve(requestedCaptureDir)
  : undefined;

test.use({
  viewport: { width: 1440, height: 900 },
  video: captureDir ? "on" : "off",
});

test("walks the truthful product tour and optionally captures reusable assets", async ({
  page,
}) => {
  test.setTimeout(60_000);
  if (captureDir) await mkdir(captureDir, { recursive: true });
  await page.emulateMedia({ reducedMotion: "reduce", colorScheme: "dark" });
  await openProductTourConsole(page);

  const mutationStatus = await page.evaluate(() =>
    fetch("/api/v1alpha/services", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ slug: "must-not-exist" }),
    }).then((response) => response.status),
  );
  expect(mutationStatus).toBe(405);

  await expect(page.getByRole("heading", { name: "Resource grid" })).toBeVisible();
  await expect(page.getByText("Launch Control").first()).toBeVisible();
  await capture(page, "01-resource-grid.png");

  await page.getByRole("button", { name: "Map" }).click();
  await expect(
    page.getByRole("region", { name: "Project topology map" }),
  ).toBeVisible();
  await capture(page, "02-topology-map.png");

  await page.getByRole("button", { name: "Grid" }).click();
  await expect(page.getByRole("heading", { name: "Resource grid" })).toBeVisible();
  await page.getByRole("button", { name: "Review" }).click();
  await expect(page.getByRole("dialog", { name: "Review changes" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Apply & deploy" })).toBeEnabled();
  await capture(page, "03-changes-apply-deploy.png");
  await page.keyboard.press("Escape");

  await page.getByRole("button", { name: "Open API" }).click();
  await expect(page.getByRole("tab", { name: /Overview/ })).toHaveAttribute(
    "data-state",
    "active",
  );
  await expect(
    page
      .getByRole("tabpanel", { name: /Overview/ })
      .getByText("Current release", { exact: true }),
  ).toBeVisible();
  await capture(page, "04-service-overview.png");

  await page.getByRole("tab", { name: /Deployments/ }).click();
  await expect(page.getByRole("heading", { name: "Release timeline" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Inspect deployment dep_api_previous" }),
  ).toBeVisible();
  await capture(page, "05-deployments.png");

  await page
    .getByRole("button", { name: "Inspect deployment dep_api_previous" })
    .click();
  await page.getByRole("button", { name: "Deployment actions" }).click();
  await expect(page.getByRole("menuitem", { name: "Rollback" })).toBeEnabled();
  await capture(page, "06-rollback.png");
  await page.getByRole("button", { name: "Deployment actions" }).click();

  await page.getByRole("tab", { name: /Logs/ }).click();
  await expect(page.getByText("HTTP server listening on :8080")).toBeVisible();
  await capture(page, "07-logs.png");

  await page.getByRole("tab", { name: /Metrics/ }).click();
  await expect(page.getByText("Resource usage")).toBeVisible();
  await expect(page.getByText("19.6%", { exact: true })).toBeVisible();
  await capture(page, "08-metrics.png");

  if (captureDir) {
    const video = page.video();
    await page.close();
    await video?.saveAs(join(captureDir, "midnight-console-tour.webm"));
  }
});

async function capture(page: Parameters<typeof openProductTourConsole>[0], name: string) {
  if (!captureDir) return;
  await page.screenshot({
    path: join(captureDir, name),
    animations: "disabled",
  });
}
