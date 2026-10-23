import { expect, type Page, type Route } from "@playwright/test";

const PROJECT = {
  id: "project_gate0",
  slug: "gate-zero",
  display_name: "Gate Zero",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const ENVIRONMENT = {
  id: "env_production",
  project_id: PROJECT.id,
  slug: "production",
  name: "Production",
  tier: "production",
  is_protected: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const SERVICES = [
  {
    id: "svc_api",
    project_id: PROJECT.id,
    slug: "api",
    display_name: "API",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-07-27T12:00:00Z",
  },
  {
    id: "svc_worker",
    project_id: PROJECT.id,
    slug: "worker",
    display_name: "Worker",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-07-27T12:00:00Z",
  },
];

const CREATED_SERVICE = {
  id: "svc_created",
  project_id: PROJECT.id,
  slug: "created-service",
  display_name: "Created service",
  created_at: "2026-07-28T12:00:00Z",
  updated_at: "2026-07-28T12:00:00Z",
};

const ENVIRONMENT_RESOURCES = {
  schema_version: "midnight.environment-resources/v1alpha1",
  project: {
    id: PROJECT.id,
    slug: PROJECT.slug,
    display_name: PROJECT.display_name,
  },
  environment: {
    id: ENVIRONMENT.id,
    project_id: ENVIRONMENT.project_id,
    slug: ENVIRONMENT.slug,
    name: ENVIRONMENT.name,
    tier: ENVIRONMENT.tier,
    is_protected: ENVIRONMENT.is_protected,
  },
  resources: [
    {
      service: {
        id: SERVICES[0].id,
        project_id: SERVICES[0].project_id,
        slug: SERVICES[0].slug,
        display_name: SERVICES[0].display_name,
      },
      latest_deployment: {
        id: "dep_build",
        status: "DEPLOYING",
        active: false,
        image: "registry.test/api@sha256:bcde",
        operation: { id: "op_build", state: "RUNNING" },
        truth_source: "canonical",
      },
      active_deployment: {
        id: "dep_api",
        status: "ACTIVE",
        active: true,
        image: "registry.test/api@sha256:abcd",
        runtime_desired_state: "running",
        runtime_observed_state: "running",
        operation: { id: "op_api", state: "SUCCEEDED" },
        truth_source: "canonical",
      },
    },
    {
      service: {
        id: SERVICES[1].id,
        project_id: SERVICES[1].project_id,
        slug: SERVICES[1].slug,
        display_name: SERVICES[1].display_name,
      },
    },
  ],
};

const DEPLOYMENT_PAGE_ONE = [
  {
    id: "dep_build",
    deployment_id: "dep_build",
    service_id: "svc_api",
    project_id: PROJECT.id,
    environment_id: ENVIRONMENT.id,
    image: "registry.test/api@sha256:bcde",
    operation_id: "op_build",
    operation_state: "RUNNING",
    status: "DEPLOYING",
    active: false,
    last_event: "deploying newer candidate",
    last_event_at: "2026-07-28T12:00:00Z",
  },
];

const DEPLOYMENT_PAGE_TWO = [
  {
    id: "dep_api",
    deployment_id: "dep_api",
    service_id: "svc_api",
    project_id: PROJECT.id,
    environment_id: ENVIRONMENT.id,
    image: "registry.test/api@sha256:abcd",
    operation_id: "op_api",
    operation_state: "SUCCEEDED",
    status: "ACTIVE",
    active: true,
    runtime_desired_state: "running",
    last_event: "canonical deployment active",
    last_event_at: "2026-07-27T12:00:00Z",
  },
];

const SUMMARY = {
  status: "ready",
  server_version: "e2e",
  api_version: "v1alpha",
  project_count: 1,
  service_count: 0,
  operation_count: 0,
  audit_status: "ready",
};

type GeneratedDomainFixture = {
  id: string;
  workspace_id: string;
  project_id: string;
  service_id: string;
  environment_id: string;
  kind: "generated";
  hostname: string;
  path: "/";
  target_port: number;
  public: true;
  tls: false;
  version: number;
  route_apply_state: "pending";
  route_reachable: false;
  created_at: string;
  updated_at: string;
};

let generatedDomains: GeneratedDomainFixture[] = [];
let createdServiceVisible = false;
let createdDeploymentQueued = false;

/**
 * Exercise the real browser UI without coupling Gate 0 to a running Midnight
 * API or mutable operator data. Broader integration suites can skip this
 * helper and point MIDNIGHT_E2E_BASE_URL at a fully provisioned stack.
 */
export async function openDeterministicConsole(
  page: Page,
  path = "/",
  options: { operatorRecovery?: boolean; instanceAdmin?: boolean } = {},
) {
  const operatorRecovery = options.operatorRecovery ?? true;
  const instanceAdmin = options.instanceAdmin ?? true;
  generatedDomains = [];
  createdServiceVisible = false;
  createdDeploymentQueued = false;
  await page.addInitScript(({ withOperatorRecovery, withInstanceAdmin }) => {
    window.localStorage.setItem("midnight.agent.collapsed", "1");
    window.localStorage.setItem("midnight.agent.pinned", "0");
    window.sessionStorage.setItem(
      "midnight.human-session-token",
      withInstanceAdmin ? "midn_user_e2e" : "midn_user_member_e2e",
    );
    if (withOperatorRecovery) {
      window.sessionStorage.setItem("midnight.api-token", "operator-e2e");
    } else {
      window.sessionStorage.removeItem("midnight.api-token");
    }
  }, { withOperatorRecovery: operatorRecovery, withInstanceAdmin: instanceAdmin });

  await page.route("**/api/v1alpha/**", fulfillDeterministicApi);
  await page.goto(path);
  await expect(page.getByTestId("app-shell")).toBeVisible();
}

export async function fulfillDeterministicApi(route: Route) {
  const request = route.request();
  const url = new URL(request.url());
  const pathname = url.pathname;
  const authorization = request.headers().authorization;

  if (
    request.method() === "GET" &&
    pathname === "/api/v1alpha/auth/me"
  ) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
    await json(route, {
      principal: {
        kind: "user",
        user_id: "usr_e2e",
        session_id: "ses_e2e",
        auth_method: "password",
        email: "operator@example.test",
        display_name: "Gate Operator",
        instance_admin: isInstanceAdminAuthorization(authorization),
      },
      session: {
        id: "ses_e2e",
        user_id: "usr_e2e",
        token_prefix: "midn_user_e",
        client_kind: "dashboard",
        created_at: "2026-01-01T00:00:00Z",
        last_seen_at: "2026-01-01T00:00:00Z",
        expires_at: "2027-01-01T00:00:00Z",
        idle_expires_at: "2027-01-01T00:00:00Z",
        current: true,
      },
    });
    return;
  }

  if (
    request.method() === "GET" &&
    pathname === "/api/v1alpha/workspaces"
  ) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
    await json(route, [
      {
        id: "wrk_gate0",
        slug: "gate-zero",
        display_name: "Gate Zero",
        role: "admin",
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
      },
    ]);
    return;
  }

  if (
    request.method() === "GET" &&
    pathname === "/api/v1alpha/usage/summary"
  ) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
    const since = url.searchParams.get("since");
    const until = url.searchParams.get("until");
    if (
      [...url.searchParams.keys()].sort().join("\0") !==
        ["since", "until", "workspace_id"].join("\0") ||
      url.searchParams.get("workspace_id") !== "wrk_gate0" ||
      !since ||
      !until ||
      !Number.isFinite(Date.parse(since)) ||
      !Number.isFinite(Date.parse(until)) ||
      Date.parse(since) > Date.parse(until)
    ) {
      await json(route, { error: { message: "Invalid usage scope" } }, 400);
      return;
    }
    await json(route, {
      workspace_id: "wrk_gate0",
      policy_profile: "self_hosted",
      window_start: "2026-08-01T00:00:00Z",
      window_end: "2026-08-13T12:00:00Z",
      as_of: "2026-08-13T12:00:00Z",
      replay_after: 9,
      usage: [
        {
          dimension: "source_artifact_bytes",
          used: 134217728,
          limit: 5368709120,
          enforceable: true,
          unit: "bytes",
          coverage: { status: "complete", source: "source_artifact_workspace_charges", missing_intervals: 0 },
        },
        {
          dimension: "project_count",
          used: 1,
          limit: 100,
          enforceable: true,
          unit: "count",
          coverage: { status: "complete", source: "projects", missing_intervals: 0 },
        },
        {
          dimension: "cpu_seconds",
          used: 7200,
          enforceable: false,
          unit: "seconds",
          coverage: { status: "partial", source: "runtime_metrics", missing_intervals: 1, note: "derived from bounded samples" },
        },
        {
          dimension: "build_seconds",
          used: 0,
          enforceable: false,
          unit: "seconds",
          coverage: { status: "unavailable", source: "unavailable", missing_intervals: 0, note: "no trustworthy producer is wired" },
        },
      ],
      disclosures: [
        "Contributions and donations do not change limits or policy assignment.",
        "Volume capacity is requested configuration, not kernel-enforced quota.",
      ],
    });
    return;
  }

  if (
    request.method() === "POST" &&
    pathname === "/api/v1alpha/services"
  ) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    if (
      !hasExactKeys(body, ["display_name", "project_id", "slug"]) ||
      body.project_id !== PROJECT.id ||
      typeof body.slug !== "string" ||
      typeof body.display_name !== "string" ||
      !request.headers()["idempotency-key"]
    ) {
      await json(route, { error: { message: "Invalid service create" } }, 400);
      return;
    }
    createdServiceVisible = true;
    await json(route, CREATED_SERVICE, 201);
    return;
  }

  if (
    request.method() === "POST" &&
    pathname === `/api/v1alpha/services/${CREATED_SERVICE.id}/deployments`
  ) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    if (
      !hasExactKeys(body, ["environment_id", "image"]) ||
      body.environment_id !== ENVIRONMENT.id ||
      body.image !==
        `registry.example/created@sha256:${"a".repeat(64)}` ||
      !request.headers()["idempotency-key"]
    ) {
      await json(
        route,
        { error: { message: "Invalid tenant image deploy" } },
        400,
      );
      return;
    }
    createdDeploymentQueued = true;
    await json(
      route,
      {
        id: "op_created",
        kind: "deploy",
        state: "queued",
        created_at: "2026-07-28T12:00:00Z",
        updated_at: "2026-07-28T12:00:00Z",
      },
      202,
    );
    return;
  }

  const generatedDomainCollection =
    `/api/v1alpha/services/${SERVICES[0].id}` +
    `/environments/${ENVIRONMENT.id}/domains`;

  if (
    request.method() === "POST" &&
    pathname === generatedDomainCollection
  ) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    if (
      !hasExactKeys(body, []) ||
      !hasValidIdempotencyKey(request.headers()["idempotency-key"]) ||
      url.search !== ""
    ) {
      await json(
        route,
        { error: { message: "Invalid generated-domain create" } },
        400,
      );
      return;
    }
    if (generatedDomains.length > 0) {
      await json(
        route,
        { error: { message: "Generated domain already exists" } },
        409,
      );
      return;
    }
    const created: GeneratedDomainFixture = {
      id: "dom_generated_api",
      workspace_id: "wrk_gate0",
      project_id: PROJECT.id,
      service_id: SERVICES[0].id,
      environment_id: ENVIRONMENT.id,
      kind: "generated",
      hostname: "api-production.apps.midnight.test",
      path: "/",
      target_port: 8080,
      public: true,
      tls: false,
      version: 1,
      route_apply_state: "pending",
      route_reachable: false,
      created_at: "2026-07-29T12:00:00Z",
      updated_at: "2026-07-29T12:00:00Z",
    };
    generatedDomains = [created];
    await json(route, created, 201);
    return;
  }

  const generatedDomainItemMatch = pathname.match(
    new RegExp(`^${generatedDomainCollection}/([^/]+)$`),
  );
  if (request.method() === "DELETE" && generatedDomainItemMatch) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    const current = generatedDomains.find(
      (domain) => domain.id === generatedDomainItemMatch[1],
    );
    if (
      !current ||
      !hasExactKeys(body, ["expected_version"]) ||
      body.expected_version !== current.version ||
      !hasValidIdempotencyKey(request.headers()["idempotency-key"]) ||
      url.search !== ""
    ) {
      await json(
        route,
        {
          error: {
            message: current
              ? "Generated domain version conflict"
              : "Generated domain not found",
          },
        },
        current ? 409 : 404,
      );
      return;
    }
    generatedDomains = [];
    await route.fulfill({ status: 204, body: "" });
    return;
  }

  if (request.method() !== "GET") {
    await json(route, { error: { message: "Unexpected Gate 0 mutation" } }, 405);
    return;
  }

  if (isHumanTenantRead(pathname)) {
    if (!isHumanAuthorization(authorization)) {
      await unauthorized(route, "Human session required");
      return;
    }
  } else if (isOperatorRead(pathname)) {
    if (
      authorization !== "Bearer operator-e2e" &&
      !isInstanceAdminAuthorization(authorization)
    ) {
      await unauthorized(route, "Installation administrator required");
      return;
    }
  } else {
    await json(
      route,
      { error: { message: `Unexpected deterministic API route: ${pathname}` } },
      404,
    );
    return;
  }

  if (pathname === "/api/v1alpha/projects") {
    await json(route, [PROJECT]);
    return;
  }

  if (pathname === `/api/v1alpha/projects/${PROJECT.id}`) {
    await json(route, PROJECT);
    return;
  }

  if (pathname === `/api/v1alpha/projects/${PROJECT.id}/access`) {
    const environmentId = url.searchParams.get("environment_id");
    if (
      (environmentId !== null && environmentId !== ENVIRONMENT.id) ||
      [...url.searchParams.keys()].some((key) => key !== "environment_id")
    ) {
      await json(route, { error: { message: "Invalid access scope" } }, 400);
      return;
    }
    await json(route, {
      principal_kind: "human",
      workspace_id: "wrk_gate0",
      project_id: PROJECT.id,
      ...(environmentId ? { environment_id: environmentId } : {}),
      environment_restricted: false,
      permissions: [
        "project.read",
        "service.read",
        "service.write",
        "environment.write",
        "deployment.trigger.cli",
        "logs.read",
        "domains.read",
        "domains.write",
      ],
    });
    return;
  }

  if (pathname === "/api/v1alpha/environments") {
    await json(route, [ENVIRONMENT]);
    return;
  }

  if (pathname === `/api/v1alpha/environments/${ENVIRONMENT.id}/resources`) {
    await json(route, {
      ...ENVIRONMENT_RESOURCES,
      resources: [
        ...ENVIRONMENT_RESOURCES.resources,
        ...(createdServiceVisible
          ? [{
              service: {
                id: CREATED_SERVICE.id,
                project_id: CREATED_SERVICE.project_id,
                slug: CREATED_SERVICE.slug,
                display_name: CREATED_SERVICE.display_name,
              },
              ...(createdDeploymentQueued
                ? {
                    latest_deployment: {
                      id: "dep_created",
                      status: "QUEUED",
                      active: false,
                      image: `registry.example/created@sha256:${"a".repeat(64)}`,
                      operation: { id: "op_created", state: "QUEUED" },
                      truth_source: "canonical",
                    },
                  }
                : {}),
            }]
          : []),
      ],
    });
    return;
  }

  if (pathname === `/api/v1alpha/environments/${ENVIRONMENT.id}/changeset`) {
    await json(route, {
      changeset: null,
      validation: {
        valid: true,
        affected_services: [],
        errors: [],
        warnings: [],
      },
      capabilities: ["variable", "networking", "runtime", "storage"].map(
        (kind) => ({ kind, supported: true }),
      ),
    });
    return;
  }

  if (pathname === "/api/v1alpha/services") {
    await json(route, SERVICES);
    return;
  }

  if (pathname === generatedDomainCollection) {
    if (url.search !== "") {
      await json(
        route,
        { error: { message: "Generated-domain list must not widen scope" } },
        400,
      );
      return;
    }
    await json(route, generatedDomains);
    return;
  }

  const serviceMatch = pathname.match(/^\/api\/v1alpha\/services\/(svc_[^/]+)$/);
  if (serviceMatch) {
    const service = [...SERVICES, CREATED_SERVICE].find(
      (item) => item.id === serviceMatch[1],
    );
    await json(
      route,
      service ?? { error: { message: "Service not found" } },
      service ? 200 : 404,
    );
    return;
  }

  if (/\/api\/v1alpha\/services\/[^/]+\/deployments\/latest-active$/.test(pathname)) {
    await json(route, { error: { message: "No active deployment" } }, 404);
    return;
  }

  const deploymentListMatch = pathname.match(
    /^\/api\/v1alpha\/services\/(svc_[^/]+)\/deployments$/,
  );
  if (deploymentListMatch) {
    if (
      url.searchParams.get("environment_id") !== ENVIRONMENT.id ||
      url.searchParams.get("limit") !== "100"
    ) {
      await json(
        route,
        { error: { message: "Deployment page lost its environment scope" } },
        400,
      );
      return;
    }
    if (deploymentListMatch[1] !== "svc_api") {
      if (url.searchParams.has("cursor")) {
        await json(route, { error: { message: "Unexpected cursor" } }, 400);
        return;
      }
      await json(route, { items: [] });
      return;
    }
    const cursor = url.searchParams.get("cursor");
    if (!cursor) {
      await json(route, {
        items: DEPLOYMENT_PAGE_ONE,
        next_cursor: "dc1.e2e/page+2=",
      });
      return;
    }
    if (cursor === "dc1.e2e/page+2=") {
      await json(route, { items: DEPLOYMENT_PAGE_TWO });
      return;
    }
    await json(route, { error: { message: "Unexpected cursor" } }, 400);
    return;
  }

  if (pathname === "/api/v1alpha/operations/op_api") {
    await json(route, {
      id: "op_api",
      kind: "deploy",
      state: "RUNNING",
      created_at: "2026-07-27T11:59:00Z",
      updated_at: "2026-07-27T12:00:00Z",
    });
    return;
  }

  if (pathname === "/api/v1alpha/operations/op_api/logs") {
    await json(route, {
      operation_id: "op_api",
      operation_events: {
        available: true,
        kind: "operation",
        url: "/operations/op_api/events",
        count: 1,
      },
      deploy_logs: {
        available: true,
        kind: "deploy",
        url: "/operations/op_api/events",
        count: 1,
      },
      app_logs: [
        {
          surface: "runtime",
          available: true,
          count: 1,
          entries: [
            {
              at: "2026-07-27T12:00:00Z",
              level: "info",
              component: "api",
              message: "runtime record for exact deployment",
            },
          ],
        },
      ],
      platform_logs: [],
      message: "Exact operation-scoped history.",
    });
    return;
  }

  if (pathname === "/api/v1alpha/operations/op_api/events") {
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body:
        "id: 1\n" +
        "event: operation\n" +
        'data: {"id":"evt_api","operation_id":"op_api","seq":1,"state":"SUCCEEDED","message":"canonical deployment active","created_at":"2026-07-27T12:00:00Z"}\n\n',
    });
    return;
  }

  if (pathname === "/api/v1alpha/dashboard/summary") {
    await json(route, SUMMARY);
    return;
  }

  if (pathname === "/api/v1alpha/dashboard/runtime-inventory") {
    await json(route, { services: [] });
    return;
  }

  if (
    pathname === "/api/v1alpha/agent/conversations" ||
    pathname === "/api/v1alpha/github/installations"
  ) {
    await json(route, []);
    return;
  }

  await json(
    route,
    { error: { message: `Deterministic route has no fixture: ${pathname}` } },
    500,
  );
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

async function unauthorized(route: Route, message: string) {
  await json(route, { error: { message } }, 401);
}

function isHumanTenantRead(pathname: string): boolean {
  return (
    pathname === "/api/v1alpha/projects" ||
    pathname === `/api/v1alpha/projects/${PROJECT.id}` ||
    pathname === `/api/v1alpha/projects/${PROJECT.id}/access` ||
    pathname === "/api/v1alpha/environments" ||
    pathname ===
      `/api/v1alpha/environments/${ENVIRONMENT.id}/resources` ||
    pathname ===
      `/api/v1alpha/environments/${ENVIRONMENT.id}/changeset` ||
    pathname === "/api/v1alpha/services" ||
    /^\/api\/v1alpha\/services\/svc_[^/]+$/.test(pathname) ||
    /^\/api\/v1alpha\/services\/svc_[^/]+\/deployments(?:\/latest-active)?$/.test(
      pathname,
    ) ||
    pathname ===
      `/api/v1alpha/services/${SERVICES[0].id}` +
        `/environments/${ENVIRONMENT.id}/domains` ||
    /^\/api\/v1alpha\/operations\/op_[^/]+(?:\/logs|\/events)?$/.test(pathname)
  );
}

function isOperatorRead(pathname: string): boolean {
  return (
    pathname === "/api/v1alpha/dashboard/summary" ||
    pathname === "/api/v1alpha/dashboard/runtime-inventory" ||
    pathname === "/api/v1alpha/agent/conversations" ||
    pathname === "/api/v1alpha/github/installations"
  );
}

function isHumanAuthorization(authorization: string | undefined): boolean {
  return (
    authorization === "Bearer midn_user_e2e" ||
    authorization === "Bearer midn_user_member_e2e"
  );
}

function isInstanceAdminAuthorization(
  authorization: string | undefined,
): boolean {
  return authorization === "Bearer midn_user_e2e";
}

function hasExactKeys(
  value: Record<string, unknown>,
  expected: string[],
): boolean {
  return (
    value !== null &&
    typeof value === "object" &&
    Object.keys(value).sort().join("\0") === [...expected].sort().join("\0")
  );
}

function hasValidIdempotencyKey(value: string | undefined): boolean {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 255 &&
    !/\s/.test(value)
  );
}
