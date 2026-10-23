import { expect, type Page, type Route } from "@playwright/test";
import { fulfillDeterministicApi } from "./app";

const PROJECT_ID = "project_gate0";
const ENVIRONMENT_ID = "env_production";
const API_SERVICE_ID = "svc_api";

const project = {
  id: PROJECT_ID,
  slug: "launch-control",
  display_name: "Launch Control",
  created_at: "2026-08-01T09:00:00Z",
  updated_at: "2026-09-21T15:04:00Z",
};

const environment = {
  id: ENVIRONMENT_ID,
  project_id: PROJECT_ID,
  slug: "production",
  name: "Production",
  tier: "production",
  is_protected: true,
  created_at: "2026-08-01T09:00:00Z",
  updated_at: "2026-09-21T15:04:00Z",
};

const services = [
  {
    id: API_SERVICE_ID,
    project_id: PROJECT_ID,
    slug: "api",
    display_name: "API",
    created_at: "2026-08-01T09:00:00Z",
    updated_at: "2026-09-21T15:04:00Z",
  },
  {
    id: "svc_worker",
    project_id: PROJECT_ID,
    slug: "worker",
    display_name: "Worker",
    created_at: "2026-08-01T09:00:00Z",
    updated_at: "2026-09-21T14:58:00Z",
  },
  {
    id: "svc_web",
    project_id: PROJECT_ID,
    slug: "web",
    display_name: "Web",
    created_at: "2026-08-01T09:00:00Z",
    updated_at: "2026-09-21T15:02:00Z",
  },
];

const activeDeployment = {
  id: "dep_api_current",
  deployment_id: "dep_api_current",
  service_id: API_SERVICE_ID,
  project_id: PROJECT_ID,
  environment_id: ENVIRONMENT_ID,
  image: "ghcr.io/midnight-demo/api@sha256:7ef81234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
  operation_id: "op_api_current",
  operation_state: "SUCCEEDED",
  status: "ACTIVE",
  active: true,
  runtime_desired_state: "running",
  runtime_observed_state: "running",
  source_type: "github",
  source_repository: "midnight-demo/launch-control",
  source_ref: "refs/heads/main",
  commit_sha: "7ef81234567890abcdef1234567890abcdef1234",
  trigger_actor_label: "Avery Chen",
  route: "https://api.launch.midnight.test",
  route_intent: "api.launch.midnight.test",
  route_apply_state: "ready",
  route_reachable: true,
  action: "deploy",
  instance_id: "instance_api_7ef8123",
  created_at: "2026-09-21T15:00:00Z",
  started_at: "2026-09-21T15:00:04Z",
  completed_at: "2026-09-21T15:01:12Z",
  last_event: "health checks passed; traffic promoted",
  last_event_at: "2026-09-21T15:01:12Z",
};

const rollbackDeployment = {
  ...activeDeployment,
  id: "dep_api_previous",
  deployment_id: "dep_api_previous",
  image: "ghcr.io/midnight-demo/api@sha256:4ac61234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
  operation_id: "op_api_previous",
  active: false,
  status: "SUCCEEDED",
  commit_sha: "4ac61234567890abcdef1234567890abcdef1234",
  instance_id: "instance_api_4ac6123",
  created_at: "2026-09-20T18:22:00Z",
  started_at: "2026-09-20T18:22:03Z",
  completed_at: "2026-09-20T18:23:08Z",
  last_event: "superseded by dep_api_current",
  last_event_at: "2026-09-20T18:23:08Z",
};

/**
 * Open a deterministic, read-only product tour backed by the real console UI.
 * The records use reserved `.test` hosts and synthetic IDs, and every mutation
 * remains blocked by the fallback deterministic API.
 */
export async function openProductTourConsole(
  page: Page,
  path = `/projects/${PROJECT_ID}?env=${ENVIRONMENT_ID}`,
) {
  await page.addInitScript(() => {
    window.localStorage.setItem("midnight.agent.collapsed", "1");
    window.localStorage.setItem("midnight.agent.pinned", "0");
    window.sessionStorage.setItem("midnight.human-session-token", "midn_user_e2e");
    window.sessionStorage.setItem("midnight.api-token", "operator-e2e");
  });
  await page.route("**/api/v1alpha/**", async (route) => {
    if (await fulfillProductTourApi(route)) return;
    await fulfillDeterministicApi(route);
  });
  await page.goto(path);
  await expect(page.getByTestId("app-shell")).toBeVisible();
}

async function fulfillProductTourApi(route: Route): Promise<boolean> {
  const request = route.request();
  const url = new URL(request.url());
  const pathname = url.pathname;

  if (request.method() !== "GET") {
    await json(
      route,
      { error: { message: "Product-tour fixtures are read-only" } },
      405,
    );
    return true;
  }

  if (pathname === "/api/v1alpha/projects") {
    await json(route, [project]);
    return true;
  }
  if (pathname === `/api/v1alpha/projects/${PROJECT_ID}`) {
    await json(route, project);
    return true;
  }
  if (pathname === `/api/v1alpha/projects/${PROJECT_ID}/access`) {
    const selectedEnvironment = url.searchParams.get("environment_id") ?? undefined;
    await json(route, {
      principal_kind: "human",
      workspace_id: "wrk_gate0",
      project_id: PROJECT_ID,
      ...(selectedEnvironment ? { environment_id: selectedEnvironment } : {}),
      environment_restricted: false,
      permissions: [
        "project.read",
        "service.read",
        "service.write",
        "variables.write",
        "environment.write",
        "deployment.trigger.cli",
        "logs.read",
        "metrics.read",
        "domains.read",
        "domains.write",
        "volume.write",
        "volume.delete",
      ],
    });
    return true;
  }
  if (pathname === "/api/v1alpha/environments") {
    await json(route, [environment]);
    return true;
  }
  if (pathname === "/api/v1alpha/services") {
    await json(route, services);
    return true;
  }
  const service = services.find(
    (candidate) => pathname === `/api/v1alpha/services/${candidate.id}`,
  );
  if (service) {
    await json(route, service);
    return true;
  }
  if (pathname === `/api/v1alpha/environments/${ENVIRONMENT_ID}/resources`) {
    await json(route, environmentResources());
    return true;
  }
  if (pathname === `/api/v1alpha/environments/${ENVIRONMENT_ID}/changeset`) {
    await json(route, savedChangeset());
    return true;
  }
  if (
    pathname ===
    `/api/v1alpha/services/${API_SERVICE_ID}/deployments/latest-active`
  ) {
    await json(route, activeDeployment);
    return true;
  }
  if (pathname === `/api/v1alpha/services/${API_SERVICE_ID}/deployments`) {
    await json(route, { items: [activeDeployment, rollbackDeployment] });
    return true;
  }
  if (/^\/api\/v1alpha\/services\/svc_(worker|web)\/deployments$/.test(pathname)) {
    await json(route, { items: [] });
    return true;
  }
  if (pathname === `/api/v1alpha/services/${API_SERVICE_ID}/preview-environments`) {
    await json(route, {
      service_id: API_SERVICE_ID,
      project_id: PROJECT_ID,
      items: [],
      capability: {
        state: "enabled",
        automatic_lifecycle: true,
        received_event: "pull_request",
        current_disposition: "routed",
        requested_actions: [
          "opened:create",
          "reopened:update",
          "synchronize:update",
          "closed:delete",
        ],
        blockers: [],
      },
    });
    return true;
  }
  if (pathname === "/api/v1alpha/dashboard/summary") {
    await json(route, {
      status: "ready",
      server_version: "0.1.0-alpha",
      api_version: "v1alpha",
      project_count: 1,
      service_count: 3,
      operation_count: 18,
      audit_status: "ready",
    });
    return true;
  }
  if (pathname === "/api/v1alpha/dashboard/runtime-inventory") {
    await json(route, {
      schema_version: "midnight.runtime-inventory/v1alpha1",
      generated_at: "2026-09-21T15:04:00Z",
      services: [
        {
          id: API_SERVICE_ID,
          project_id: PROJECT_ID,
          slug: "api",
          name: "API",
          kind: "service",
          runtime_state: "running",
          replicas: "2/2",
          route: "api.launch.midnight.test",
          health: "healthy",
          updated: "3s ago",
          operation_id: "op_api_current",
          operation_state: "SUCCEEDED",
          last_event: "health checks passed; traffic promoted",
          last_event_at: "2026-09-21T15:03:57Z",
        },
      ],
    });
    return true;
  }
  if (pathname === "/api/v1alpha/observability/metrics") {
    await json(route, metricSeries());
    return true;
  }
  const operation = pathname.match(
    /^\/api\/v1alpha\/operations\/(op_api_(?:current|previous))$/,
  );
  if (operation) {
    const previous = operation[1].endsWith("previous");
    await json(route, {
      id: operation[1],
      kind: "deploy",
      state: "SUCCEEDED",
      created_at: previous
        ? rollbackDeployment.created_at
        : activeDeployment.created_at,
      updated_at: previous
        ? rollbackDeployment.completed_at
        : activeDeployment.completed_at,
    });
    return true;
  }
  const logOperation = pathname.match(
    /^\/api\/v1alpha\/operations\/(op_api_(?:current|previous))\/logs$/,
  );
  if (logOperation) {
    await json(route, operationLogs(logOperation[1]));
    return true;
  }
  const eventOperation = pathname.match(
    /^\/api\/v1alpha\/operations\/(op_api_(?:current|previous))\/events$/,
  );
  if (eventOperation) {
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body:
        "id: 3\n" +
        "event: operation\n" +
        `data: {"id":"evt_${eventOperation[1]}","operation_id":"${eventOperation[1]}","seq":3,"state":"SUCCEEDED","message":"health checks passed; traffic promoted","created_at":"2026-09-21T15:01:12Z"}\n\n`,
    });
    return true;
  }
  return false;
}

function environmentResources() {
  const resource = (
    service: (typeof services)[number],
    deployment?: typeof activeDeployment,
  ) => ({
    service,
    ...(deployment
      ? {
          source: {
            type: "github",
            repository: "midnight-demo/launch-control",
            ref: "main",
            revision: deployment.commit_sha,
            wait_for_ci: true,
            auto_deploy: true,
            verified_at: "2026-09-21T14:59:58Z",
            truth_source: "canonical-github-link",
          },
          endpoint: {
            deployment_id: deployment.id,
            host: "api.launch.midnight.test",
            url: "https://api.launch.midnight.test",
            tls: true,
            evidence_at: "2026-09-21T15:03:57Z",
          },
          private_endpoint: {
            host: "api.production.internal",
            truth_source: "canonical",
          },
          latest_deployment: {
            id: deployment.id,
            status: deployment.status,
            active: deployment.active,
            image: deployment.image,
            source_type: deployment.source_type,
            route: deployment.route,
            route_apply_state: deployment.route_apply_state,
            route_reachable: deployment.route_reachable,
            runtime_desired_state: deployment.runtime_desired_state,
            runtime_observed_state: deployment.runtime_observed_state,
            commit_sha: deployment.commit_sha,
            replicas: {
              desired: 2,
              running: 2,
              ready: 2,
              health: "healthy",
              evidence_at: "2026-09-21T15:03:57Z",
            },
            operation: { id: deployment.operation_id, state: deployment.operation_state },
            truth_source: "canonical",
          },
          active_deployment: {
            id: deployment.id,
            status: deployment.status,
            active: true,
            image: deployment.image,
            source_type: deployment.source_type,
            route: deployment.route,
            route_apply_state: deployment.route_apply_state,
            route_reachable: deployment.route_reachable,
            runtime_desired_state: deployment.runtime_desired_state,
            runtime_observed_state: deployment.runtime_observed_state,
            commit_sha: deployment.commit_sha,
            replicas: {
              desired: 2,
              running: 2,
              ready: 2,
              health: "healthy",
              evidence_at: "2026-09-21T15:03:57Z",
            },
            operation: { id: deployment.operation_id, state: deployment.operation_state },
            truth_source: "canonical",
          },
        }
      : {}),
  });
  return {
    schema_version: "midnight.environment-resources/v1alpha1",
    project,
    environment,
    resources: [
      resource(services[0], activeDeployment),
      resource(services[1]),
      resource(services[2]),
    ],
  };
}

function savedChangeset() {
  return {
    changeset: {
      id: "chg_release_42",
      workspace_id: "wrk_gate0",
      project_id: PROJECT_ID,
      environment_id: ENVIRONMENT_ID,
      created_by: "usr_e2e",
      state: "draft",
      revision: 42,
      items: [
        {
          id: "chg_item_runtime",
          kind: "runtime",
          operation: "edit",
          service_id: API_SERVICE_ID,
          target_id: "runtime_api",
          name: "runtime",
          expected_version: 11,
          value_change: "replicas 1 → 2",
        },
        {
          id: "chg_item_domain",
          kind: "networking",
          operation: "add",
          service_id: API_SERVICE_ID,
          name: "api.launch.midnight.test",
          value_change: "generated domain added",
        },
      ],
      created_at: "2026-09-21T14:57:00Z",
      updated_at: "2026-09-21T14:59:00Z",
    },
    validation: {
      valid: true,
      affected_services: [API_SERVICE_ID],
      affected_resources: [
        {
          service_id: API_SERVICE_ID,
          resource_kinds: ["runtime", "networking"],
          requires_deploy: true,
          deployment_action: "redeploy",
        },
      ],
      errors: [],
      warnings: [],
    },
    capabilities: ["variable", "networking", "runtime", "storage"].map(
      (kind) => ({ kind, supported: true }),
    ),
  };
}

function metricSeries() {
  const values = [14.8, 18.2, 16.7, 24.6, 21.3, 27.4, 22.1, 19.6];
  return {
    mode: "sum",
    step: "2m",
    points: values.map((cpu, index) => ({
      observed_at: new Date(Date.UTC(2026, 8, 21, 14, 50 + index * 2)).toISOString(),
      cpu_percent: cpu,
      memory_bytes: 318_000_000 + index * 5_400_000,
      memory_limit_bytes: 1_073_741_824,
      network_rx_bytes: 18_000_000 + index * 1_300_000,
      network_tx_bytes: 7_500_000 + index * 860_000,
      block_read_bytes: 3_000_000 + index * 120_000,
      block_write_bytes: 2_000_000 + index * 90_000,
      disk_usage_bytes: 143_000_000 + index * 700_000,
      restart_count: 0,
      replicas: 2,
      samples: 2,
    })),
    deployment_markers: [
      {
        deployment_id: activeDeployment.id,
        marker: "promoted",
        observed_at: activeDeployment.completed_at,
      },
    ],
    collectors: ["docker"],
  };
}

function operationLogs(operationId: string) {
  return {
    operation_id: operationId,
    operation_events: {
      available: true,
      kind: "operation",
      url: `/operations/${operationId}/events`,
      count: 4,
    },
    deploy_logs: {
      available: true,
      kind: "deploy",
      url: `/operations/${operationId}/events`,
      count: 4,
    },
    app_logs: [
      {
        surface: "runtime",
        available: true,
        count: 3,
        entries: [
          {
            at: "2026-09-21T15:01:14Z",
            level: "info",
            component: "api",
            message: "HTTP server listening on :8080",
          },
          {
            at: "2026-09-21T15:01:15Z",
            level: "info",
            component: "health",
            message: "readiness probe passed",
          },
          {
            at: "2026-09-21T15:03:57Z",
            level: "info",
            component: "api",
            message: "GET /v1/orders 200 38ms",
          },
        ],
      },
    ],
    platform_logs: [],
    message: "Exact operation-scoped history.",
  };
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}
