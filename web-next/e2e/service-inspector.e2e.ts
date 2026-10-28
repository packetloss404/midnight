import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

test("keeps resources interactive beside the desktop service inspector", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openDeterministicConsole(
    page,
    "/projects/project_gate0?env=env_production",
  );

  const apiButton = page.getByRole("button", { name: "Open API" });
  await apiButton.click();

  await expect(page).toHaveURL(
    /\/projects\/project_gate0\/services\/svc_api\/overview\?env=env_production/,
  );
  const grid = page.getByRole("region", { name: /Resource grid/ });
  const inspector = page.getByRole("complementary", { name: "API" });
  await expect(grid).toBeVisible();
  await expect(inspector).toBeVisible();
  await expect(page.locator(".drawer-overlay")).toHaveCount(0);
  await expect(
    inspector.getByRole("heading", { name: "dep_api" }),
  ).toBeVisible();
  await inspector.getByText("Technical details", { exact: true }).click();
  await expect(inspector.getByText("Deployment record")).toBeVisible();
  await expect(inspector.getByText("New rollout (not serving)")).toBeVisible();
  await expect(inspector.getByText("dep_build", { exact: true })).toBeVisible();

  const boxes = await Promise.all([grid.boundingBox(), inspector.boundingBox()]);
  expect(boxes[0]).not.toBeNull();
  expect(boxes[1]).not.toBeNull();
  expect((boxes[0]?.x ?? 0) + (boxes[0]?.width ?? 0)).toBeLessThanOrEqual(
    (boxes[1]?.x ?? 0) + 1,
  );

  await page.getByRole("button", { name: "Open Worker" }).click();
  await expect(page).toHaveURL(/\/services\/svc_worker\/overview/);
  await expect(
    page.getByRole("complementary", { name: "Worker" }),
  ).toBeVisible();

  await page.getByTestId("agent-dock-fab").click();
  await expect(page.getByTestId("agent-dock")).toBeVisible();
  await expect(
    page.getByRole("complementary", { name: "Worker" }),
  ).toBeHidden();
  await expect(page).toHaveURL(
    /\/projects\/project_gate0\/services\/svc_worker\/overview\?env=env_production$/,
  );
  await page.getByRole("button", { name: "Collapse panel", exact: true }).click();
  await expect(page.getByRole("complementary", { name: "Worker" })).toBeVisible();
  await expect(page).toHaveURL(
    /\/projects\/project_gate0\/services\/svc_worker\/overview\?env=env_production$/,
  );
});

test("uses a full-width mobile route and preserves section/deployment deep links", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await openDeterministicConsole(
    page,
    "/projects/project_gate0/services/svc_api/overview?env=env_production",
    { operatorRecovery: false },
  );

  const inspector = page.getByRole("complementary", { name: "API" });
  await expect(inspector).toBeVisible();
  await expect(page.getByRole("region", { name: /Resource grid/ })).toBeHidden();
  const selector = page.getByLabel("Service section");
  await expect(selector).toBeVisible();

  await selector.selectOption("deployments");
  await expect(page).toHaveURL(/\/services\/svc_api\/deployments/);
  await expect(
    page.getByRole("list", { name: "Deployment timeline" }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Inspect deployment dep_api" })
    .click();
  await expect(page).toHaveURL(/deployment=dep_api/);
  await expect(
    page.getByText("Not recorded by the deployment ledger"),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Deployment actions capability" }),
  ).toHaveAttribute("data-capability-state", "available");
  const deploymentActions = page.getByRole("button", {
    name: "Deployment actions",
  });
  await expect(deploymentActions).toBeEnabled();
  await deploymentActions.click();
  await expect(page.getByRole("menuitem", { name: "Redeploy" })).toBeEnabled();
  await expect(page.getByRole("menuitem", { name: /Cancel/ })).toBeDisabled();
  await deploymentActions.click();

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
    inspectorWidth:
      document.querySelector<HTMLElement>(".service-inspector")?.getBoundingClientRect()
        .width ?? 0,
  }));
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(
    dimensions.clientWidth + 1,
  );
  expect(dimensions.inspectorWidth).toBeGreaterThanOrEqual(389);
  expect(dimensions.inspectorWidth).toBeLessThanOrEqual(390);
});

test("moves keyboard focus into the inspector and restores it on Escape", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openDeterministicConsole(
    page,
    "/projects/project_gate0?env=env_production",
  );

  const apiButton = page.getByRole("button", { name: "Open API" });
  await apiButton.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#service-inspector-title")).toBeFocused();

  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(
    /\/projects\/project_gate0\?env=env_production$/,
  );
  await expect(apiButton).toBeFocused();
});
