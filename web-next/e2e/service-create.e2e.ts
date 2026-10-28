import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

type RecordedAPIRequest = {
  method: string;
  path: string;
  search: string;
  authorization?: string;
};

function isScopedHumanConversationRead(
  request: RecordedAPIRequest,
  serviceIds: readonly string[],
): boolean {
  if (
    request.method !== "GET" ||
    request.path !== "/api/v1alpha/agent/conversations" ||
    request.authorization !== "Bearer midn_user_member_e2e"
  ) return false;
  const params = new URLSearchParams(request.search);
  return (
    params.getAll("project_id").length === 1 &&
    params.get("project_id") === "project_gate0" &&
    [...params.keys()].every((key) => key === "project_id" || key === "service_id") &&
    (params.getAll("service_id").length === 0 ||
      (params.getAll("service_id").length === 1 &&
        serviceIds.includes(params.get("service_id") ?? "")))
  );
}

test("creates a tenant service and queues an immutable image without privileged fallback", async ({
  page,
}) => {
  const requests: Array<{
    method: string;
    path: string;
    search: string;
    authorization?: string;
    idempotencyKey?: string;
    body?: string | null;
  }> = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    requests.push({
      method: request.method(),
      path: url.pathname,
      search: url.search,
      authorization: request.headers().authorization,
      idempotencyKey: request.headers()["idempotency-key"],
      body: request.postData(),
    });
  });

  await page.setViewportSize({ width: 1440, height: 900 });
  await openDeterministicConsole(
    page,
    "/projects/project_gate0?env=env_production",
    { operatorRecovery: false, instanceAdmin: false },
  );

  await page.getByRole("button", { name: "+ Add resource" }).click();
  await expect(page).toHaveURL(/tab=add-service/);
  await page.getByRole("option", { name: /Container image/i }).click();
  await expect(
    page.getByText(/Immutable image deployment is available/),
  ).toBeVisible();
  const serviceName = page.getByPlaceholder("api");
  await serviceName.fill("Created service");
  const image =
    `registry.example/created@sha256:${"a".repeat(64)}`;
  await page
    .getByPlaceholder("registry.example/app@sha256:…")
    .fill(image);
  await page.getByRole("button", { name: "Create and deploy" }).click();

  await expect(page).toHaveURL(
    /\/projects\/project_gate0\/services\/svc_created\/overview/,
  );
  const createdService = page.getByRole("complementary", {
    name: "Created service",
  });
  await expect(createdService).toBeVisible();
  await expect(
    createdService.getByRole("heading", { name: "No active release" }),
  ).toBeVisible();
  await expect(
    createdService.getByText("New rollout (not serving)", { exact: false }),
  ).toBeVisible();
  await expect(createdService.getByText("dep_created", { exact: true })).toBeVisible();

  await page.goto(
    "/projects/project_gate0/services/svc_api/deployments?env=env_production&deployment=dep_api",
  );
  await expect(
    page.getByRole("complementary", { name: "API" }),
  ).toBeVisible();
  await expect(
    page.getByText("Not recorded by the deployment ledger"),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Logs for this deployment" }),
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

  await page.getByRole("tab", { name: "Logs" }).click();
  await expect(page).toHaveURL(/\/services\/svc_api\/logs/);
  await expect(page.getByText("runtime record for exact deployment")).toBeVisible();

  const createRequest = requests.find(
    (request) =>
      request.method === "POST" &&
      request.path === "/api/v1alpha/services",
  );
  expect(createRequest?.authorization).toBe("Bearer midn_user_member_e2e");
  expect(createRequest?.idempotencyKey).toBeTruthy();
  expect(JSON.parse(createRequest?.body ?? "{}")).toEqual({
    project_id: "project_gate0",
    slug: "created-service",
    display_name: "Created service",
  });

  const deployRequest = requests.find(
    (request) =>
      request.method === "POST" &&
      request.path ===
        "/api/v1alpha/services/svc_created/deployments",
  );
  expect(deployRequest?.authorization).toBe("Bearer midn_user_member_e2e");
  expect(deployRequest?.idempotencyKey).toBeTruthy();
  expect(JSON.parse(deployRequest?.body ?? "{}")).toEqual({
    environment_id: "env_production",
    image,
  });

  const accessRequest = requests.find(
    (request) =>
      request.method === "GET" &&
      request.path === "/api/v1alpha/projects/project_gate0/access" &&
      new URLSearchParams(request.search).get("environment_id") ===
        "env_production",
  );
  expect(new URLSearchParams(accessRequest?.search).get("environment_id")).toBe(
    "env_production",
  );

  const deploymentPages = requests.filter(
    (request) =>
      request.method === "GET" &&
      request.path === "/api/v1alpha/services/svc_api/deployments",
  );
  const firstPages = deploymentPages.filter(
    (request) => !request.search.includes("cursor="),
  );
  const secondPages = deploymentPages.filter((request) =>
    request.search.includes("cursor=dc1.e2e%2Fpage%2B2%3D"),
  );
  expect(firstPages.length).toBeGreaterThanOrEqual(1);
  expect(secondPages.length).toBeGreaterThanOrEqual(1);
  for (const request of deploymentPages) {
    const params = new URLSearchParams(request.search);
    expect(params.get("environment_id")).toBe("env_production");
    expect(params.get("limit")).toBe("100");
  }
  for (const request of secondPages) {
    expect(new URLSearchParams(request.search).get("cursor")).toBe(
      "dc1.e2e/page+2=",
    );
  }
  expect(
    deploymentPages.every(
      (request) => request.authorization === "Bearer midn_user_member_e2e",
    ),
  ).toBe(true);

  for (const path of [
    "/api/v1alpha/operations/op_api",
    "/api/v1alpha/operations/op_api/logs",
    "/api/v1alpha/operations/op_api/events",
  ]) {
    expect(
      requests.some(
        (request) =>
          request.path === path &&
          request.authorization === "Bearer midn_user_member_e2e",
      ),
    ).toBe(true);
  }

  expect(
    requests.some(
      (request) =>
        request.path.includes("/dashboard/") ||
        request.path.includes("/observability/") ||
        request.authorization === "Bearer operator-e2e",
    ),
  ).toBe(false);
  expect(
    requests.some(
      (request) =>
        request.method === "POST" &&
        (request.path.includes("/graphql") ||
          request.path.endsWith("/github")),
    ),
  ).toBe(false);

  const scopedRequests = requests.filter(
    (request) =>
      request.path.startsWith("/api/v1alpha/") ||
      request.path === "/graphql/v2",
  );
  const conversationReads = scopedRequests.filter((request) =>
    request.path.includes("/agent/"),
  );
  expect(conversationReads.length).toBeGreaterThan(0);
  expect(conversationReads.every((request) =>
    isScopedHumanConversationRead(request, ["svc_created", "svc_api"]),
  )).toBe(true);
  const unexpectedRequests = scopedRequests.filter((request) => {
    if (request.path.includes("/agent/")) {
      return !isScopedHumanConversationRead(request, ["svc_created", "svc_api"]);
    }
    if (
      request.method === "POST" &&
      (request.path === "/api/v1alpha/services" ||
        request.path ===
          "/api/v1alpha/services/svc_created/deployments")
    ) {
      return false;
    }
    if (request.method !== "GET") return true;
    return !(
      request.path === "/api/v1alpha/auth/me" ||
      request.path === "/api/v1alpha/projects" ||
      request.path === "/api/v1alpha/projects/project_gate0" ||
      request.path === "/api/v1alpha/projects/project_gate0/access" ||
      request.path === "/api/v1alpha/environments" ||
      request.path ===
        "/api/v1alpha/environments/env_production/resources" ||
      request.path ===
        "/api/v1alpha/environments/env_production/changeset" ||
      request.path === "/api/v1alpha/services" ||
      /^\/api\/v1alpha\/services\/svc_[^/]+$/.test(request.path) ||
      /^\/api\/v1alpha\/services\/svc_[^/]+\/deployments(?:\/latest-active)?$/.test(
        request.path,
      ) ||
      /^\/api\/v1alpha\/operations\/op_[^/]+(?:\/logs|\/events)?$/.test(
        request.path,
      )
    );
  });
  expect(unexpectedRequests).toEqual([]);
  expect(
    scopedRequests.every(
      (request) => request.authorization === "Bearer midn_user_member_e2e",
    ),
  ).toBe(true);
});

test("non-admin human routes expose tenant administration but keep fleet observability behind recovery", async ({
  page,
}) => {
  const requests: RecordedAPIRequest[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    const pathname = url.pathname;
    if (
      pathname.startsWith("/api/v1alpha/") ||
      pathname === "/graphql/v2"
    ) {
      requests.push({
        method: request.method(),
        path: pathname,
        search: url.search,
        authorization: request.headers().authorization,
      });
    }
  });

  await openDeterministicConsole(
    page,
    "/projects/project_gate0/observability?env=env_production",
    { operatorRecovery: false, instanceAdmin: false },
  );
  await expect(
    page.getByRole("heading", { name: "Installation administrator required" }),
  ).toBeVisible();

  await page.goto("/projects/project_gate0/settings?env=env_production");
  await expect(page.getByText("Configure", { exact: true })).toBeVisible();
  await expect(page.getByText("Environments", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Danger zone" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /Delete project/i }),
  ).toHaveCount(0);

  await page.goto("/projects/project_gate0/deployments?env=env_production");
  await expect(
    page.getByRole("heading", { name: "Deployments" }),
  ).toBeVisible();
  await expect
    .poll(
      () => requests.filter((request) => request.path.includes("/deployments")).length,
    )
    .toBeGreaterThan(0);

  const conversationReads = requests.filter((request) =>
    request.path.includes("/agent/"),
  );
  expect(conversationReads.length).toBeGreaterThan(0);
  expect(conversationReads.every((request) =>
    isScopedHumanConversationRead(request, []),
  )).toBe(true);
  expect(
    requests.filter(
      (request) =>
        request.path.includes("/dashboard/") ||
        request.path.includes("/observability/") ||
        request.path.includes("/variables") ||
        request.path.includes("/github/") ||
        (request.path.includes("/agent/") &&
          !isScopedHumanConversationRead(request, [])) ||
        request.path.endsWith("/resources") ||
        request.path === "/graphql/v2" ||
        request.authorization === "Bearer operator-e2e",
    ),
  ).toEqual([]);
});
