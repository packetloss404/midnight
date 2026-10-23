import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

test("uses the verified environment summary for Grid and keeps Map explicit", async ({
  page,
}) => {
  const requests: string[] = [];
  page.on("request", (request) => {
    requests.push(new URL(request.url()).pathname);
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await openDeterministicConsole(
    page,
    "/projects/project_gate0?env=env_production",
  );

  await expect(page.getByRole("heading", { name: "Resource grid" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open API" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open Worker" })).toBeVisible();
  await expect(
    page.getByText("deployment unavailable", { exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText("not deployed", { exact: true })).toHaveCount(1);
  await expect(page.getByText("dep_api", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("dep_build", { exact: true }).first()).toBeVisible();
  await expect(
    page.getByText(/dep_api is serving traffic while dep_build is deploying/),
  ).toBeVisible();

  const resourceReadPaths = requests.filter(
    (path) =>
      path === "/api/v1alpha/environments/env_production/resources" ||
      path.endsWith("/deployments/latest-active"),
  );
  expect(resourceReadPaths).toEqual([
    "/api/v1alpha/environments/env_production/resources",
  ]);
  expect(
    requests.filter((path) => path.endsWith("/deployments/latest-active")),
  ).toHaveLength(0);
  expect(requests).toContain("/api/v1alpha/services");
  expect(
    requests.some((path) =>
      /^\/api\/v1alpha\/services\/[^/]+\/deployments/.test(path),
    ),
  ).toBe(false);

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth + 1);

  await page.getByRole("button", { name: "Map" }).click();
  await expect(page).toHaveURL(/env=env_production/);
  await expect(page).toHaveURL(/view=map/);
  await expect(
    page.getByRole("region", { name: "Project topology map" }),
  ).toBeVisible();

  await page.getByRole("button", { name: "Grid" }).click();
  await expect(page.getByRole("heading", { name: "Resource grid" })).toBeVisible();
  await expect(page).not.toHaveURL(/view=map/);
});

test("preserves environment, service, tab, and explicit Map deep links", async ({
  page,
}) => {
  await openDeterministicConsole(
    page,
    "/projects/project_gate0?env=env_production&service=svc_api&tab=logs&view=map",
  );

  await expect(
    page.getByRole("complementary", { name: "API" }),
  ).toBeVisible();
  await expect(page.getByRole("tab", { name: "Logs" })).toHaveAttribute(
    "data-state",
    "active",
  );
  await expect(page).toHaveURL(
    /\/projects\/project_gate0\/services\/svc_api\/logs/,
  );
  const url = new URL(page.url());
  expect(url.searchParams.get("env")).toBe("env_production");
  expect(url.searchParams.get("service")).toBeNull();
  expect(url.searchParams.get("tab")).toBeNull();
  expect(url.searchParams.get("view")).toBe("map");
});
