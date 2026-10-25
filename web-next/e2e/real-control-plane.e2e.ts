import { expect, test, type Page } from "@playwright/test";

// This file never intercepts requests. The default Playwright config owns an
// isolated, ephemeral Go API. Never run these mutations against an operator's
// externally configured URL.
test.skip(Boolean(process.env.MIDNIGHT_E2E_BASE_URL), "local throwaway API only");

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("midnight.agent.collapsed", "1");
    window.localStorage.setItem("midnight.agent.pinned", "0");
  });
});

async function createProject(page: Page, starter = false) {
  const name = `Browser ${crypto.randomUUID().slice(0, 8)}`;
  await page.goto("/new");
  await page.getByRole("button", { name: /Empty project/ }).click();
  await page.getByPlaceholder("my-app").fill(name);
  if (starter) {
    await page.getByLabel("Also create a starter service").check();
    await page.getByPlaceholder("api").fill("Browser API");
  }
  await page.getByRole("button", { name: "Create project", exact: true }).click();
  await expect(page).toHaveURL(/\/projects\/[^/?]+/);
  await expect(page.getByRole("heading", { name: "Resource grid" })).toBeVisible();
  const projectId = new URL(page.url()).pathname.split("/")[2];
  const environmentId = new URL(page.url()).searchParams.get("env");
  expect(environmentId).toBeTruthy();
  return { name, projectId, environmentId };
}

test("creates projects and services through the browser and reloads real API state", async ({ page, request }) => {
  const { name, projectId, environmentId } = await createProject(page, true);
  const servicesResponse = await request.get(`/api/v1alpha/services?project_id=${projectId}`);
  expect(servicesResponse.ok()).toBe(true);
  const services = await servicesResponse.json();
  const service = services.find((item: { display_name: string }) => item.display_name === "Browser API");
  expect(service?.project_id).toBe(projectId);

  await page.reload();
  await expect(page.getByRole("heading", { name })).toBeVisible();
  await page.getByRole("button", { name: "Open Browser API" }).click();
  const inspector = page.getByRole("complementary", { name: "Browser API" });
  await expect(inspector.getByRole("heading", { name: "No active release" })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/services/${service.id}/overview`));
  await page.getByRole("tab", { name: "Deployments", exact: true }).click();
  await expect(inspector.getByText("No deployments yet", { exact: true })).toBeVisible();

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/projects/${projectId}?env=${environmentId}&view=map`);
  await expect(page.getByRole("region", { name: "Project canvas" })).toBeVisible();
  await page.getByRole("button", { name: "Grid", exact: true }).click();
  await page.getByRole("button", { name: "Open Browser API" }).click();
  await expect(page.getByRole("complementary", { name: "Browser API" })).toBeVisible();
  const widths = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
  expect(widths.scroll).toBeLessThanOrEqual(widths.client + 1);
});

test("queues an immutable image through real GraphQL without inventing a running workload", async ({ page, request }) => {
  const { projectId, environmentId } = await createProject(page);
  await page.getByRole("button", { name: "+ Add resource" }).click();
  await page.getByRole("option", { name: /Container image/i }).click();
  await page.getByPlaceholder("api").fill("Image API");
  const image = `registry.example/browser@sha256:${"a".repeat(64)}`;
  await page.getByPlaceholder("registry.example/app@sha256:…").fill(image);
  const intake = page.waitForResponse((response) => new URL(response.url()).pathname === "/graphql/v2" && response.request().method() === "POST");
  await page.getByRole("button", { name: "Create and deploy" }).click();
  const response = await intake;
  expect(response.headers()["content-type"]).toContain("application/json");
  const result = await response.json();
  expect(result.errors).toBeUndefined();
  expect(result.data.serviceInstanceDeployV2.status.toLowerCase()).toBe("queued");
  await expect(page).toHaveURL(/\/services\/[^/]+\/overview/);
  const serviceId = new URL(page.url()).pathname.split("/")[4];
  const deployments = await request.get(`/api/v1alpha/services/${serviceId}/deployments?environment_id=${environmentId}&limit=100`);
  expect(deployments.ok()).toBe(true);
  const body = await deployments.json();
  const deployment = (Array.isArray(body) ? body : body.items)[0];
  expect(deployment.image).toBe(image);
  expect(deployment.project_id).toBe(projectId);
  expect(deployment.active).toBe(false);
  await expect(page.getByRole("heading", { name: "No active release" })).toBeVisible();
});

test("navigates actual docs and reports unavailable usage without fixture success", async ({ page }) => {
  await page.goto("/docs");
  const docs = page.getByRole("navigation", { name: "Documentation" });
  await docs.getByRole("link", { name: "API reference" }).click();
  await expect(page.getByRole("heading", { name: "Practical API reference" })).toBeVisible();
  await docs.getByRole("link", { name: "CLI reference" }).click();
  await expect(page.getByRole("heading", { name: "Midnight CLI" })).toBeVisible();
  await page.goto("/usage");
  await expect(page.getByRole("heading", { name: "Workspace sign-in required" })).toBeVisible();
  await expect(page.getByText(/Sign in with your workspace account/)).toBeVisible();
});
