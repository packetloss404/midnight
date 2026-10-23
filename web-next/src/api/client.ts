// Typed fetch wrapper for the Midnight v1alpha REST API.
//
// All requests are routed through Vite's dev proxy at `/api` (see
// `vite.config.ts`) so this file does not hardcode `http://localhost:8080`.
// Credentials are kept in sessionStorage so they are never compiled into a
// production JavaScript bundle. An installation administrator's human session
// is used across the console; the separately stored recovery credential is a
// break-glass fallback (see `credentials.ts`).

export const API_PREFIX = "/api/v1alpha";
export const DEFAULT_BOOTSTRAP_WORKSPACE_ID = "wrk_default";

import {
  CREDENTIALS_CHANGED_EVENT,
  OPERATOR_RECOVERY_TOKEN_STORAGE_KEY,
  getHumanSessionToken,
  getOperatorRecoveryToken,
  setOperatorRecoveryToken,
} from "./credentials";

// Compatibility names remain recovery-credential-only. Normal browser API
// requests select the signed-in human administrator first in authHeader().
export const TOKEN_STORAGE_KEY = OPERATOR_RECOVERY_TOKEN_STORAGE_KEY;
export const TOKEN_CHANGED_EVENT = CREDENTIALS_CHANGED_EVENT;
export const getToken = getOperatorRecoveryToken;
export const setToken = setOperatorRecoveryToken;

export type ApiErrorBody = {
  error?: {
    code?: string;
    message?: string;
    request_id?: string;
    details?: Record<string, unknown>;
  };
};

// ── identity/session types ───────────────────────────────────────

export type IdentityUser = {
  id: string;
  email: string;
  display_name: string;
  instance_admin: boolean;
  mfa_enabled: boolean;
  last_login_at?: string;
};

export type IdentityPrincipal = {
  kind: "user";
  user_id: string;
  session_id: string;
  auth_method: "password" | "password+totp";
  email: string;
  display_name: string;
  instance_admin: boolean;
};

export type IdentitySession = {
  id: string;
  user_id: string;
  token_prefix: string;
  client_kind: "dashboard" | "cli" | "api";
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  idle_expires_at: string;
  revoked_at?: string;
  revoked_reason?: string;
  current: boolean;
};

export type IdentityLoginRequest = {
  email: string;
  password: string;
  totp_code?: string;
  client_kind?: "dashboard" | "cli" | "api";
};

export type IdentityLoginResult = {
  access_token: string;
  token_type: "Bearer";
  expires_in: number;
  user: IdentityUser;
  session: IdentitySession;
};

export type IdentityMe = {
  principal: IdentityPrincipal;
  session: IdentitySession;
};

// First-run setup. `claimed` is the only field the unauthenticated status
// route returns, deliberately: the console must decide whether to show
// administrator setup before any credential exists, and anything richer would
// make this an anonymous tenant-enumeration surface.
export type IdentityClaimStatus = {
  claimed: boolean;
};

export type IdentityClaimRequest = {
  email: string;
  display_name: string;
  password: string;
  totp_secret: string;
  totp_code: string;
  workspace_slug: string;
  workspace_display_name: string;
};

export type IdentityClaimResult = {
  user: IdentityUser;
  workspace: IdentityWorkspace;
};

// Operator break-glass credential recovery. `/auth/claim` is one-shot, so this
// is the only supported way back into a claimed installation whose
// administrator password was lost. The second factor is never silently
// dropped: either `totp_secret` plus its current `totp_code` enrols a
// replacement, or `preserve_totp` keeps the authenticator already on the
// account. The two are mutually exclusive and the server refuses both-or-
// neither, so the console never has to guess which factor survives.
export type IdentityPasswordResetRequest = {
  email: string;
  password: string;
  totp_secret?: string;
  totp_code?: string;
  preserve_totp?: boolean;
};

// No credential material comes back. `sessions_revoked` is the blast radius:
// a reset closes every live session for the account, so the operator can see
// that a stolen session did not outlive the recovery.
export type IdentityPasswordResetResult = {
  user: IdentityUser;
  totp_reenrolled: boolean;
  sessions_revoked: number;
};

export type IdentityWorkspace = {
  id: string;
  slug: string;
  display_name: string;
  role: "admin" | "member" | "deployer" | "viewer";
  created_at: string;
  updated_at: string;
};

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly requestId?: string;
  readonly details?: Record<string, unknown>;
  readonly retryAfterSeconds?: number;

  constructor(
    status: number,
    body: ApiErrorBody | string,
    requestId?: string,
    retryAfter?: string,
  ) {
    const message =
      typeof body === "string"
        ? safeNonJSONErrorMessage(status, body)
        : body?.error?.message ?? `Request failed (${status})`;
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.requestId =
      requestId?.trim() || (typeof body === "string" ? undefined : body.error?.request_id);
    if (typeof body !== "string") {
      this.code = body?.error?.code;
      this.details = body?.error?.details;
    }
    const retryAfterSeconds = Number.parseInt(retryAfter ?? "", 10);
    if (Number.isFinite(retryAfterSeconds) && retryAfterSeconds > 0) {
      this.retryAfterSeconds = retryAfterSeconds;
    }
  }
}

type RequestOptions = {
  signal?: AbortSignal;
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  auth?: RequestAuthPolicy;
};

export type RequestAuthPolicy = "none" | "human" | "product" | "operator";

export function authHeader(
  policy: RequestAuthPolicy = "operator",
): Record<string, string> {
  const token =
    policy === "none"
      ? undefined
      : policy === "human"
        ? getHumanSessionToken()
        : policy === "product" || policy === "operator"
          ? getHumanSessionToken() ?? getOperatorRecoveryToken()
          : getOperatorRecoveryToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export async function apiFetch<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...authHeader(options.auth),
    ...options.headers,
  };

  let body: BodyInit | undefined;
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(options.body);
  }

  const response = await fetch(`${API_PREFIX}${path}`, {
    method: options.method ?? "GET",
    headers,
    body,
    signal: options.signal,
  });

  // 204 / empty body — return undefined cast to T.
  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  const parsed = text ? safeParse(text) : undefined;

  if (!response.ok) {
    const errorBody =
      typeof parsed === "object" && parsed !== null
        ? (parsed as ApiErrorBody)
        : typeof parsed === "string"
          ? parsed
          : "";
    throw new ApiError(
      response.status,
      errorBody,
      response.headers.get("x-request-id") ??
        response.headers.get("x-midnight-request-id") ??
        undefined,
      response.headers.get("retry-after") ?? undefined,
    );
  }

  return parsed as T;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

const MAX_NON_JSON_ERROR_LENGTH = 280;
const HTML_ERROR_BODY = /<!doctype|<!--|<\/?[a-z][^>]*>/i;

function safeNonJSONErrorMessage(status: number, value: string): string {
  const normalized = value
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!normalized || HTML_ERROR_BODY.test(normalized)) {
    return `Request failed (${status})`;
  }
  if (normalized.length <= MAX_NON_JSON_ERROR_LENGTH) return normalized;
  return `${normalized.slice(0, MAX_NON_JSON_ERROR_LENGTH - 1).trimEnd()}…`;
}

// ── resource types ────────────────────────────────────────────────

export type Project = {
  id: string;
  workspace_id: string;
  slug: string;
  display_name: string;
  created_at: string;
  updated_at: string;
};

export type CreateProjectInput = {
  workspace_id: string;
  slug: string;
  display_name?: string;
};

export type Service = {
  id: string;
  project_id: string;
  slug: string;
  display_name: string;
  created_at: string;
  updated_at: string;
  /**
   * Strong validator required by `If-Match` on delete. Older servers omit it;
   * callers must treat a missing value as "no conditional delete available"
   * rather than guessing a version.
   */
  resource_version?: number;
  spec?: Record<string, unknown>;
};

/** What one service delete actually removed. Counts are exact row counts. */
export type ServiceDeletionReceipt = {
  service_id: string;
  project_id: string;
  slug: string;
  deleted_at: string;
  retained_deployments: number;
  removed: {
    variables: number;
    volumes: number;
    published_domains: number;
    routes: number;
    tcp_proxies: number;
    environment_configs: number;
    source_links: number;
    pending_changes: number;
    monitors: number;
  };
};

export type CreateServiceInput = {
  project_id: string;
  slug: string;
  display_name?: string;
};

export type TopologyLayoutNode = {
  service_id: string;
  x: number;
  y: number;
};

export type TopologyLayout = {
  workspace_id: string;
  project_id: string;
  environment_id: string;
  nodes: TopologyLayoutNode[];
  resource_version: number;
  created_at?: string;
  updated_at?: string;
};

export type DeploymentResource = {
  id: string;
  deployment_id?: string;
  service_id: string;
  project_id?: string;
  environment_id?: string;
  instance_id?: string;
  image?: string;
  action?: string;
  source_type?: string;
  source_repository?: string;
  source_ref?: string;
  commit_sha?: string;
  trigger_actor_type?: string;
  trigger_actor_label?: string;
  route_intent?: string;
  route?: string;
  route_apply_state?:
    | "pending"
    | "private"
    | "blocked_tls"
    | "blocked_backend"
    | "file_applied"
    | "ready"
    | "failed";
  route_reachable?: boolean;
  operation_id: string;
  operation_state: string;
  failure_code?: string;
  failure_reason?: string;
  status: string;
  active: boolean;
  runtime_desired_state?: "running" | "stopped" | "removed";
  runtime_observed_state?: "unknown" | "running" | "stopped" | "removed";
  runtime_generation?: number;
  last_event?: string;
  last_event_at?: string;
  created_at?: string;
  started_at?: string;
  completed_at?: string;
};

export type DeploymentResourcePage = {
  items: DeploymentResource[];
  next_cursor?: string;
};

export type VariableMeta = {
  id: string;
  scope: string;
  workspace_id: string;
  project_id: string;
  environment_id: string;
  service_id?: string;
  name: string;
  version: number;
  last_rotated_at: string;
  length_bucket: string;
  kek_fingerprint: string;
  value_kind?: "literal" | "reference";
  references?: Array<{
    kind: "service_var" | "shared" | "local" | "builtin";
    service?: string;
    name: string;
  }>;
};

export type CreateVariableInput = {
  name: string;
  workspace_id: string;
  project_id: string;
  environment_id: string;
  service_id: string;
  scope: "plain" | "secret" | "sealed";
  value: string;
};

export type UpdateVariableInput = {
  workspace_id: string;
  project_id: string;
  environment_id: string;
  service_id: string;
  scope: "plain" | "secret" | "sealed";
  value: string;
  expected_version: number;
};

export type DeleteVariableInput = {
  workspace_id: string;
  project_id: string;
  environment_id: string;
  service_id: string;
  expected_version: number;
};

// The API mirrors `internal/api/resource_store_pg.go#Environment`.
export type Environment = {
  id: string;
  project_id: string;
  slug: string;
  name: string;
  tier: string;
  is_protected: boolean;
  resource_version: number;
  created_at: string;
  updated_at: string;
};

export type EnvironmentResourceSummary = {
  schema_version: string;
  project: {
    id: string;
    slug: string;
    display_name: string;
  };
  environment: {
    id: string;
    project_id: string;
    slug: string;
    name: string;
    tier: string;
    is_protected: boolean;
  };
  resources: EnvironmentServiceResource[];
};

export type EnvironmentServiceResource = {
  service: {
    id: string;
    project_id: string;
    slug: string;
    display_name: string;
  };
  latest_deployment?: EnvironmentDeploymentSummary;
  active_deployment?: EnvironmentDeploymentSummary;
  source?: {
    type: string;
    repository?: string;
    ref?: string;
    revision?: string;
    watch_paths?: string[];
    root_directory?: string;
    config_path?: string;
    wait_for_ci?: boolean;
    required_checks?: string[];
    auto_deploy?: boolean;
    verified_at?: string;
    updated_at?: string;
    truth_source: string;
  };
  endpoint?: {
    deployment_id: string;
    host?: string;
    url?: string;
    tls: boolean;
    evidence_at: string;
  };
  private_endpoint?: {
    host: string;
    truth_source: string;
  };
};

export type EnvironmentDeploymentSummary = {
  id: string;
  status: string;
  active: boolean;
  image?: string;
  action?: string;
  source_type?: string;
  source_repository?: string;
  source_ref?: string;
  commit_sha?: string;
  root_directory?: string;
  dockerfile_path?: string;
  builder?: string;
  runtime_desired_state?: "running" | "stopped" | "removed";
  runtime_observed_state?: "unknown" | "running" | "stopped" | "removed";
  runtime_generation?: number;
  replicas?: EnvironmentReplicaSummary;
  operation?: {
    id: string;
    state: string;
    failure_code?: string;
    failure_reason?: string;
  };
  created_at?: string;
  updated_at?: string;
  started_at?: string;
  completed_at?: string;
  truth_source: string;
};

export type EnvironmentReplicaSummary = {
  desired: number;
  ready: number;
  running: number;
  health: "healthy" | "degraded" | "progressing" | "unhealthy" | "stopped" | "removed" | string;
  evidence_at: string;
  truth_source: string;
};

// Mirrors `internal/api/operation_store.go#OperationEvent` — the payload of
// each `event: operation` SSE frame on `/operations/{id}/events?stream=sse`.
export type OperationEvent = {
  id: string;
  operation_id: string;
  seq: number;
  state: string;
  message?: string;
  created_at: string;
};

// Mirrors `orailtypes.Operation` returned by `GET /operations/{id}`.
export type OperationResource = {
  id: string;
  kind?: string;
  state: string;
  code?: string;
  reason?: string;
  created_at?: string;
  updated_at?: string;
};

export type EnvironmentChangesetItem = {
  id: string;
  kind: "variable" | "networking" | "runtime" | "storage";
  operation: "add" | "edit" | "remove";
  service_id: string;
  target_id?: string;
  name: string;
  scope?: "plain" | "secret" | "sealed";
  expected_version?: number;
  value_change: string;
  runtime?: EnvironmentRuntimeSpec;
  networking?: EnvironmentNetworkingSpec;
  storage?: EnvironmentStorageSpec;
};

export type EnvironmentChangeset = {
  id: string;
  workspace_id: string;
  project_id: string;
  environment_id: string;
  created_by: string;
  note?: string;
  state: "draft" | "applied" | "discarded";
  revision: number;
  applied_revision?: number;
  items: EnvironmentChangesetItem[];
  created_at: string;
  updated_at: string;
  applied_at?: string;
  discarded_at?: string;
};

export type EnvironmentChangesetView = {
  changeset?: EnvironmentChangeset | null;
  validation: {
    valid: boolean;
    affected_services: string[];
    /** Present on servers that can explain deployment impact per service. */
    affected_resources?: Array<{
      service_id: string;
      resource_kinds: EnvironmentChangesetItem["kind"][];
      requires_deploy: boolean;
      deployment_action: EnvironmentDeploymentAction;
    }>;
    errors: string[];
    warnings: string[];
  };
  capabilities: Array<{
    kind: "variable" | "networking" | "runtime" | "storage";
    supported: boolean;
    reason?: string;
  }>;
  configuration_state?: {
    applied_revision: number;
    services: Array<{
      service_id: string;
      applied_revision: number;
      deployed_revision: number;
      deployment_required: boolean;
      deployed_deployment_id?: string;
      pending_deployment_id?: string;
    }>;
  };
};

export type EnvironmentDeploymentAction = "none" | "redeploy" | "rebuild";

export type EnvironmentOperationGroupService = {
  service_id: string;
  resource_kinds: EnvironmentChangesetItem["kind"][];
  requires_deploy: boolean;
  deployment_action: EnvironmentDeploymentAction;
  intake_state: string;
  deployment_id?: string;
  operation_id?: string;
  operation_state?: string;
  deployment_state?: string;
  failure_code?: string;
  failure_reason?: string;
};

export type EnvironmentOperationGroup = {
  id: string;
  environment_id: string;
  changeset_id: string;
  configuration_revision: number;
  state: string;
  items: EnvironmentOperationGroupService[];
  created_at: string;
};

/**
 * The apply-and-deploy coordinator returns one environment-level receipt.
 * `operation_group` remains optional while older servers roll forward; callers
 * must never invent deployment success when it is absent.
 */
export type ApplyAndDeployEnvironmentChangesetResult = {
  applied_revision: number;
  operation_group_id: string;
  affected_services: string[];
  changeset?: EnvironmentChangeset | null;
  validation: EnvironmentChangesetView["validation"];
  operation_group?: EnvironmentOperationGroup | null;
};

export type EnvironmentRuntimeSpec = {
  root_directory: string;
  builder: "auto" | "dockerfile" | "railpack";
  dockerfile_path: string;
  build_command: string;
  start_command: string;
  predeploy_command: string;
  target_port: number;
  health_kind: "none" | "http" | "tcp";
  health_path: string;
  health_port: number;
  health_timeout_seconds: number;
  health_interval_seconds: number;
  restart_policy: "no" | "always" | "unless-stopped" | "on-failure";
  restart_retries: number;
  replicas: number;
  overlap_enabled: boolean;
  drain_grace_seconds: number;
  runtime_kind: "service" | "background" | "cron";
  cron_schedule: string;
  cron_timeout_seconds: number;
  cron_max_attempts: number;
  cron_retry_delay_seconds: number;
};

export type EnvironmentNetworkingSpec = { kind: "generated"; public: true };
export type EnvironmentStorageSpec = { size_bytes: number; mount_path: string };

export type EnvironmentChangesetInput = {
  kind: "variable" | "runtime" | "networking" | "storage";
  operation: "add" | "edit" | "remove";
  service_id: string;
  target_id?: string;
  name: string;
  scope?: "plain" | "secret" | "sealed";
  expected_version?: number;
  value?: string;
  runtime?: EnvironmentRuntimeSpec;
  networking?: EnvironmentNetworkingSpec;
  storage?: EnvironmentStorageSpec;
};

// GitHub installation summary from `GET /github/installations`.
export type GithubInstallation = {
  id: string;
  account?: string;
  account_login?: string;
  target_type?: string;
};

export type GithubRepo = {
  id: string;
  full_name: string;
  name?: string;
  private?: boolean;
  default_branch?: string;
};

export type GithubSetupInitiation = {
  install_url: string;
  expires_at: string;
};

/**
 * Environment-scoping helper. Project-scoped list endpoints accept an
 * optional `environment_id` filter; the URL search param `?env=` is the
 * single source of truth (see `lib/env-scope.ts`). `undefined` means
 * "all environments".
 */
export type EnvScope = { projectId?: string; environmentId?: string };

function scopeQuery(scope: EnvScope, extra?: Record<string, string>): string {
  const params = new URLSearchParams();
  if (scope.projectId) params.set("project_id", scope.projectId);
  if (scope.environmentId) params.set("environment_id", scope.environmentId);
  for (const [k, v] of Object.entries(extra ?? {})) {
    if (v) params.set(k, v);
  }
  const s = params.toString();
  return s ? `?${s}` : "";
}

// ── resource endpoints ────────────────────────────────────────────

export const api = {
  verifyOperatorRecoveryToken: (token: string, signal?: AbortSignal) =>
    apiFetch<Record<string, unknown>>("/dashboard/summary", {
      auth: "none",
      headers: { Authorization: `Bearer ${token.trim()}` },
      signal,
    }),
  getIdentityClaimStatus: (signal?: AbortSignal) =>
    apiFetch<IdentityClaimStatus>("/auth/claim/status", {
      auth: "none",
      signal,
    }),
  claimDefaultWorkspace: (
    input: IdentityClaimRequest,
    recoveryToken: string,
    idempotencyKey = makeIdempotencyKey("identity-claim"),
  ) =>
    apiFetch<IdentityClaimResult>("/auth/claim", {
      auth: "none",
      method: "POST",
      body: input,
      headers: {
        Authorization: `Bearer ${recoveryToken.trim()}`,
        "Idempotency-Key": idempotencyKey,
      },
    }),
  // Sent with an explicit operator bearer and `auth: "none"`: the route
  // accepts only the installation recovery credential and refuses a human
  // session, so the ambient token must never be substituted here.
  resetAdministratorPassword: (
    input: IdentityPasswordResetRequest,
    recoveryToken: string,
    idempotencyKey = makeIdempotencyKey("identity-password-reset"),
  ) =>
    apiFetch<IdentityPasswordResetResult>("/auth/password-reset", {
      auth: "none",
      method: "POST",
      body: input,
      headers: {
        Authorization: `Bearer ${recoveryToken.trim()}`,
        "Idempotency-Key": idempotencyKey,
      },
    }),
  createIdentitySession: (input: IdentityLoginRequest, signal?: AbortSignal) =>
    apiFetch<IdentityLoginResult>("/auth/sessions", {
      auth: "none",
      method: "POST",
      body: { ...input, client_kind: input.client_kind ?? "dashboard" },
      signal,
    }),
  getIdentityMe: (signal?: AbortSignal) =>
    apiFetch<IdentityMe>("/auth/me", { auth: "human", signal }),
  listIdentitySessions: (signal?: AbortSignal) =>
    apiFetch<IdentitySession[]>("/auth/sessions", {
      auth: "human",
      signal,
    }),
  revokeIdentitySession: (sessionId: string, signal?: AbortSignal) =>
    apiFetch<void>(`/auth/sessions/${encodeURIComponent(sessionId)}`, {
      auth: "human",
      method: "DELETE",
      signal,
    }),
  listIdentityWorkspaces: (signal?: AbortSignal) =>
    apiFetch<IdentityWorkspace[]>("/workspaces", {
      auth: "human",
      signal,
    }),
  listProjects: (signal?: AbortSignal) =>
    apiFetch<Project[]>("/projects", { auth: "product", signal }),
  createProject: (
    input: CreateProjectInput,
    idempotencyKey = makeIdempotencyKey("project"),
  ) =>
    apiFetch<Project>("/projects", {
      auth: "operator",
      method: "POST",
      body: input,
      headers: { "Idempotency-Key": idempotencyKey },
    }),
  getProject: (id: string, signal?: AbortSignal) =>
    apiFetch<Project>(`/projects/${encodeURIComponent(id)}`, {
      auth: "product",
      signal,
    }),
  getTopologyLayout: (
    projectId: string,
    environmentId: string,
    signal?: AbortSignal,
  ) =>
    apiFetch<TopologyLayout>(
      `/projects/${encodeURIComponent(projectId)}/environments/${encodeURIComponent(environmentId)}/topology-layout`,
      { auth: "product", signal },
    ),
  putTopologyLayout: (
    projectId: string,
    environmentId: string,
    nodes: TopologyLayoutNode[],
    expectedVersion: number,
  ) =>
    apiFetch<TopologyLayout>(
      `/projects/${encodeURIComponent(projectId)}/environments/${encodeURIComponent(environmentId)}/topology-layout`,
      {
        auth: "product",
        method: "PUT",
        body: { nodes },
        headers: { "If-Match": `"${expectedVersion}"` },
      },
    ),
  listServices: (scope: EnvScope = {}, signal?: AbortSignal) =>
    apiFetch<Service[]>(`/services${scopeQuery(scope)}`, {
      auth: "product",
      signal,
    }),
  getService: (id: string, signal?: AbortSignal) =>
    apiFetch<Service>(`/services/${encodeURIComponent(id)}`, {
      auth: "product",
      signal,
    }),
  createService: (
    input: CreateServiceInput,
    idempotencyKey = makeIdempotencyKey("service"),
  ) =>
    apiFetch<Service>("/services", {
      auth: "product",
      method: "POST",
      body: input,
      headers: { "Idempotency-Key": idempotencyKey },
    }),
  /**
   * Delete a service that is no longer running.
   *
   * The server refuses this while the service still has a running or in-flight
   * deployment (`SERVICE_DELETE_RUNNING`) or a dependent record that must be
   * cleared first (`SERVICE_DELETE_BLOCKED`). Those arrive as an ApiError with
   * that `code`, and the caller is expected to show it rather than retry.
   */
  deleteService: (
    id: string,
    expectedVersion: number,
    idempotencyKey = makeIdempotencyKey("service-delete"),
  ) =>
    apiFetch<ServiceDeletionReceipt>(`/services/${encodeURIComponent(id)}`, {
      auth: "product",
      method: "DELETE",
      headers: {
        "Idempotency-Key": idempotencyKey,
        "If-Match": `"${expectedVersion}"`,
      },
    }),
  listDeployments: async (
    serviceId: string,
    environmentId?: string,
    signal?: AbortSignal,
  ) => {
    const deployments: DeploymentResource[] = [];
    const seenCursors = new Set<string>();
    let cursor = "";
    for (;;) {
      const response = await apiFetch<
        DeploymentResource[] | DeploymentResourcePage
      >(
        `/services/${encodeURIComponent(serviceId)}/deployments${scopeQuery(
          { environmentId },
          { limit: "100", cursor },
        )}`,
        { auth: "product", signal },
      );
      // Transitional compatibility for an older server during a rolling
      // upgrade. Current servers return the bounded page envelope.
      if (Array.isArray(response)) {
        deployments.push(...response);
        return deployments;
      }
      if (!response || !Array.isArray(response.items)) {
        throw new Error("deployment response is missing a page items array");
      }
      deployments.push(...response.items);
      const nextCursor = response.next_cursor?.trim() ?? "";
      if (!nextCursor) return deployments;
      if (seenCursors.has(nextCursor)) {
        throw new Error("deployment server repeated a pagination cursor");
      }
      seenCursors.add(nextCursor);
      cursor = nextCursor;
    }
  },
  latestActiveDeployment: async (
    serviceId: string,
    environmentId?: string,
    signal?: AbortSignal,
  ) =>
    (await apiFetch<DeploymentResource | undefined>(
      `/services/${encodeURIComponent(serviceId)}/deployments/latest-active${scopeQuery({
        environmentId,
      })}`,
      { auth: "product", signal },
    )) ?? null,
  listVariables: (scope: EnvScope & { serviceId?: string } = {}, signal?: AbortSignal) => {
    const query = scopeQuery(scope, { service_id: scope.serviceId ?? "" });
    return apiFetch<VariableMeta[]>(`/variables${query}`, { signal });
  },
  createVariable: (
    input: CreateVariableInput,
    idempotencyKey = makeIdempotencyKey("variable-create"),
  ) =>
    apiFetch<VariableMeta>("/variables", {
      method: "POST",
      body: input,
      headers: { "Idempotency-Key": idempotencyKey },
    }),
  updateVariable: (
    id: string,
    input: UpdateVariableInput,
    idempotencyKey = makeIdempotencyKey("variable-update"),
  ) =>
    apiFetch<VariableMeta>(`/variables/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: input,
      headers: { "Idempotency-Key": idempotencyKey },
    }),
  deleteVariable: (
    id: string,
    input: DeleteVariableInput,
    idempotencyKey = makeIdempotencyKey("variable-delete"),
  ) =>
    apiFetch<void>(`/variables/${encodeURIComponent(id)}`, {
      method: "DELETE",
      body: input,
      headers: { "Idempotency-Key": idempotencyKey },
    }),
  listEnvironments: (projectId?: string, signal?: AbortSignal) =>
    apiFetch<Environment[]>(
      `/environments${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ""}`,
      { auth: "product", signal },
    ),
  getEnvironmentResources: (environmentId: string, signal?: AbortSignal) =>
    apiFetch<EnvironmentResourceSummary>(
      `/environments/${encodeURIComponent(environmentId)}/resources`,
      { signal },
    ),
  getEnvironmentChangeset: (environmentId: string, signal?: AbortSignal) =>
    apiFetch<EnvironmentChangesetView>(
      `/environments/${encodeURIComponent(environmentId)}/changeset`,
      { auth: "human", signal },
    ),
  putEnvironmentChangeset: (
    environmentId: string,
    expectedRevision: number,
    changes: EnvironmentChangesetInput[],
    note?: string,
    idempotencyKey = makeIdempotencyKey("changeset-put"),
  ) =>
    apiFetch<EnvironmentChangesetView>(
      `/environments/${encodeURIComponent(environmentId)}/changeset`,
      {
        auth: "human",
        method: "PUT",
        headers: {
          "Idempotency-Key": idempotencyKey,
          "If-Match": `"${expectedRevision}"`,
        },
        body: { expected_revision: expectedRevision, note, changes },
      },
    ),
  validateEnvironmentChangeset: (
    environmentId: string,
    expectedRevision: number,
  ) =>
    apiFetch<EnvironmentChangesetView>(
      `/environments/${encodeURIComponent(environmentId)}/changeset/validate`,
      {
        auth: "human",
        method: "POST",
        body: { expected_revision: expectedRevision },
      },
    ),
  applyEnvironmentChangeset: (
    environmentId: string,
    expectedRevision: number,
    idempotencyKey = makeIdempotencyKey("changeset-apply"),
  ) =>
    apiFetch<EnvironmentChangesetView>(
      `/environments/${encodeURIComponent(environmentId)}/changeset/apply`,
      {
        auth: "human",
        method: "POST",
        headers: {
          "Idempotency-Key": idempotencyKey,
          "If-Match": `"${expectedRevision}"`,
        },
        body: { expected_revision: expectedRevision },
      },
    ),
  applyAndDeployEnvironmentChangeset: (
    environmentId: string,
    expectedRevision: number,
    idempotencyKey = makeIdempotencyKey("changeset-apply-deploy"),
  ) =>
    apiFetch<ApplyAndDeployEnvironmentChangesetResult>(
      `/environments/${encodeURIComponent(environmentId)}/changeset/apply-and-deploy`,
      {
        auth: "human",
        method: "POST",
        headers: {
          "Idempotency-Key": idempotencyKey,
          "If-Match": `"${expectedRevision}"`,
        },
        body: { expected_revision: expectedRevision },
      },
    ),
  getEnvironmentOperationGroup: (
    environmentId: string,
    operationGroupId: string,
    signal?: AbortSignal,
  ) =>
    apiFetch<EnvironmentOperationGroup>(
      `/environments/${encodeURIComponent(environmentId)}/operation-groups/${encodeURIComponent(operationGroupId)}`,
      { auth: "human", signal },
    ),
  discardEnvironmentChangeset: (
    environmentId: string,
    expectedRevision: number,
    idempotencyKey = makeIdempotencyKey("changeset-discard"),
  ) =>
    apiFetch<EnvironmentChangesetView>(
      `/environments/${encodeURIComponent(environmentId)}/changeset/discard`,
      {
        auth: "human",
        method: "POST",
        headers: {
          "Idempotency-Key": idempotencyKey,
          "If-Match": `"${expectedRevision}"`,
        },
        body: { expected_revision: expectedRevision },
      },
    ),
  getOperation: (id: string, signal?: AbortSignal) =>
    apiFetch<OperationResource>(`/operations/${encodeURIComponent(id)}`, {
      auth: "product",
      signal,
    }),
  listGithubInstallations: (signal?: AbortSignal) =>
    apiFetch<GithubInstallation[]>("/github/installations", { signal }),
  listGithubRepos: (installationId: string, signal?: AbortSignal) =>
    apiFetch<GithubRepo[]>(
      `/github/installations/${encodeURIComponent(installationId)}/repos`,
      { signal },
    ),
  createGithubSetup: (workspaceId: string, signal?: AbortSignal) =>
    apiFetch<GithubSetupInitiation>(
      `/workspaces/${encodeURIComponent(workspaceId)}/github/setup`,
      { auth: "human", method: "POST", signal },
    ),
  dashboardSummary: (signal?: AbortSignal) =>
    apiFetch<Record<string, unknown>>("/dashboard/summary", { signal }),
};

function makeIdempotencyKey(scope: string): string {
  const random =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : Math.random().toString(36).slice(2);
  return `web-next:${scope}:${random}`;
}
