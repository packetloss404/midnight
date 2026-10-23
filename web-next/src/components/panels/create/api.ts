// Create and mutation surface shared by the New Project and Add Service flows.
//
// Endpoints wired here:
//   - POST /projects                       (REST, Idempotency-Key)   createProject
//   - POST /services                       (REST, Idempotency-Key)   createService
//   - GET  /templates                      (REST)                    useTemplates
//   - GET  /templates/{slug}               (REST)                    useTemplate
//   - POST /templates/{slug}/instances     (REST, Idempotency-Key)   deployTemplate
//   - serviceInstanceDeployV2              (GraphQL, Idempotency-Key) deployServiceImage
//
// The GraphQL `serviceCreate` resolver ignores the
// `source` input (it only persists slug/displayName), and REST `POST
// /services` has no source field either. So a Docker-image source is bound at
// deploy time via `serviceInstanceDeployV2`, and a GitHub source is bound via
// the github link (see ../github/api.ts). The service row itself is created
// through REST here for a deterministic id + separate display name.

import { useQuery, type UseQueryOptions } from "@tanstack/react-query";
import {
  apiFetch,
  DEFAULT_BOOTSTRAP_WORKSPACE_ID,
  type RequestAuthPolicy,
  type Environment,
  type EnvironmentChangesetView,
  type OperationResource,
  type Project,
  type Service,
} from "@/api/client";
import { graphqlRequest } from "@/api/graphql";
import { getStagedForEnv } from "@/lib/stagedChanges";

// ── shared helpers ────────────────────────────────────────────────

/** Fresh Idempotency-Key for a mutating POST. */
export function newIdempotencyKey(): string {
  try {
    if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
      return crypto.randomUUID();
    }
  } catch {
    /* fall through */
  }
  return `idem-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

/** Slugify a free-text name into a resource slug the REST API accepts. */
export function slugify(input: string): string {
  const s = input
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s || `svc-${Math.random().toString(36).slice(2, 8)}`;
}

/** Human image intake accepts only a complete lowercase SHA-256 digest. */
export function isImmutableImageReference(value: string): boolean {
  return (
    value === value.trim() &&
    /^[^\s@]+@sha256:[0-9a-f]{64}$/.test(value)
  );
}

export type PublicRouteRequest = Readonly<{ public: true }>;

function isPublicRouteRequest(value: unknown): value is PublicRouteRequest {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const keys = Object.keys(value);
  return (
    keys.length === 1 &&
    keys[0] === "public" &&
    (value as { public?: unknown }).public === true
  );
}

// ── projects / services (REST) ────────────────────────────────────

export async function createProject(input: {
  name: string;
  slug?: string;
  workspaceId?: string;
}, idempotencyKey = newIdempotencyKey()): Promise<Project> {
  const slug = input.slug && input.slug.trim() ? slugify(input.slug) : slugify(input.name);
  return apiFetch<Project>("/projects", {
    auth: "operator",
    method: "POST",
    body: {
      workspace_id: input.workspaceId?.trim() || DEFAULT_BOOTSTRAP_WORKSPACE_ID,
      slug,
      display_name: input.name.trim() || slug,
    },
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

/** Human project creation never falls back to an installed recovery token. */
export async function createHumanProject(input: {
  name: string;
  slug?: string;
  workspaceId: string;
}, idempotencyKey: string): Promise<Project> {
  const workspaceId = input.workspaceId.trim();
  if (!workspaceId) throw new Error("A workspace is required.");
  const slug = input.slug?.trim() ? slugify(input.slug) : slugify(input.name);
  return apiFetch<Project>("/projects", {
    auth: "human",
    method: "POST",
    body: {
      workspace_id: workspaceId,
      slug,
      display_name: input.name.trim() || slug,
    },
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export async function createService(input: {
  projectId: string;
  name: string;
  slug?: string;
}, idempotencyKey = newIdempotencyKey()): Promise<Service> {
  const slug = input.slug && input.slug.trim() ? slugify(input.slug) : slugify(input.name);
  return apiFetch<Service>("/services", {
    auth: "operator",
    method: "POST",
    body: {
      project_id: input.projectId,
      slug,
      display_name: input.name.trim() || slug,
    },
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

/**
 * Human service creation is deliberately distinct from product authority:
 * losing the human session between access admission and submit must never
 * fall back to an installed operator recovery credential.
 */
export async function createHumanService(input: {
  projectId: string;
  name: string;
  slug?: string;
}, idempotencyKey = newIdempotencyKey()): Promise<Service> {
  const slug = input.slug && input.slug.trim() ? slugify(input.slug) : slugify(input.name);
  return apiFetch<Service>("/services", {
    auth: "human",
    method: "POST",
    body: {
      project_id: input.projectId,
      slug,
      display_name: input.name.trim() || slug,
    },
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

// ── templates (REST) ──────────────────────────────────────────────

export type TemplateSummary = {
  slug: string;
  name: string;
  version: string;
  summary: string;
  provisionable: boolean;
  capability_reason?: string;
  tags?: string[];
};

export type TemplateEnvSpec = {
  name?: string;
  kind: string;
  value?: string;
  generated_kind?: string;
  length?: number;
  template?: string;
  secret?: boolean;
};

export type TemplateServiceSpec = {
  slug: string;
  image: string;
  env?: Record<string, TemplateEnvSpec>;
  volume?: { name: string; mount_path: string; size_bytes: number };
};

export type TemplateDetail = {
  slug: string;
  name: string;
  version: string;
  summary: string;
  consumer_docs?: string;
  tags?: string[];
  services: TemplateServiceSpec[];
};

export type CommunityValidationFinding = {
  severity: "error" | "warning";
  code: string;
  path: string;
  message: string;
};

export type CommunityManifest = {
  schema_version: number;
  template: TemplateDetail;
  metadata: {
    maintainer: string;
    license: string;
    security_review: "reviewed" | "unreviewed";
    risk_class: "low" | "moderate" | "high";
    update_policy: string;
    deprecation_policy: string;
    readme_path: string;
    sbom: { path: string; sha256: string };
  };
};

export type CommunityTemplateProvenance = {
  source: string;
  ref: string;
  manifest_path: string;
  manifest_sha256: string;
  readme_path: string;
  readme_sha256: string;
  sbom_path: string;
  sbom_sha256: string;
};

export type CommunityTemplateDiscovery = {
  trust_class: "community";
  manifest: CommunityManifest;
  provenance: CommunityTemplateProvenance;
  readme: string;
  findings: CommunityValidationFinding[];
  valid: boolean;
};

export type CommunityTemplateFork = {
  id: string;
  project_id: string;
  trust_class: "local";
  upstream_manifest: CommunityManifest;
  draft: CommunityManifest;
  provenance: CommunityTemplateProvenance;
  readme: string;
  findings: CommunityValidationFinding[];
  valid: boolean;
  resource_version: number;
  created_at: string;
  updated_at: string;
};

export type TemplateDeployResult = {
  operation_id: string;
  provision_run_id: string;
  template_slug: string;
  state: "queued" | "running" | "succeeded" | "failed" | "canceled";
  environment_id: string;
  deployment_id: string;
  status_url: string;
  created_services?: { id: string; slug: string }[];
  created_volumes?: { id: string; name?: string }[];
  note?: string;
};

export type TemplateProvisionRun = {
  id: string;
  template_slug: string;
  project_id: string;
  environment_id: string;
  service_id: string;
  volume_id: string;
  operation_id: string;
  deployment_id: string;
  state: "queued" | "running" | "succeeded" | "failed" | "canceled";
  connectivity_verified: boolean;
  persistence_verified: boolean;
  cleanup_completed: boolean;
  failure_code?: string;
  failure_reason?: string;
};

async function listTemplates(signal?: AbortSignal): Promise<TemplateSummary[]> {
  const res = await apiFetch<{ templates?: TemplateSummary[] }>("/templates", { signal });
  return res.templates ?? [];
}

async function getTemplate(slug: string, signal?: AbortSignal): Promise<TemplateDetail> {
  return apiFetch<TemplateDetail>(`/templates/${encodeURIComponent(slug)}`, { signal });
}

type QueryOpts<T> = Omit<UseQueryOptions<T, Error, T>, "queryKey" | "queryFn">;

export function useTemplates(opts?: QueryOpts<TemplateSummary[]>) {
  return useQuery<TemplateSummary[]>({
    queryKey: ["templates"],
    queryFn: ({ signal }) => listTemplates(signal),
    ...opts,
  });
}

export function useTemplate(slug: string | undefined, opts?: QueryOpts<TemplateDetail>) {
  return useQuery<TemplateDetail>({
    queryKey: ["templates", slug ?? ""],
    queryFn: ({ signal }) => getTemplate(slug as string, signal),
    enabled: Boolean(slug),
    ...opts,
  });
}

export async function deployTemplate(
  slug: string,
  input: { projectId: string; environmentId?: string; name?: string },
  idempotencyKey = newIdempotencyKey(),
): Promise<TemplateDeployResult> {
  return apiFetch<TemplateDeployResult>(
    `/templates/${encodeURIComponent(slug)}/instances`,
    {
      method: "POST",
      body: {
        project_id: input.projectId,
        environment_id: input.environmentId,
        name: input.name,
      },
      headers: { "Idempotency-Key": idempotencyKey },
    },
  );
}

export async function discoverCommunityTemplate(
  input: { source: string; ref: string },
  auth: RequestAuthPolicy = "product",
): Promise<CommunityTemplateDiscovery> {
  return apiFetch<CommunityTemplateDiscovery>("/templates/community/discover", {
    auth,
    method: "POST",
    body: { source: input.source.trim(), ref: input.ref.trim() },
  });
}

export async function createCommunityTemplateFork(
  input: {
    projectId: string;
    discovery: CommunityTemplateDiscovery;
  },
  idempotencyKey: string,
  auth: RequestAuthPolicy = "product",
): Promise<CommunityTemplateFork> {
  return apiFetch<CommunityTemplateFork>("/templates/community/forks", {
    auth,
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: {
      project_id: input.projectId,
      source: input.discovery.provenance.source,
      ref: input.discovery.provenance.ref,
      expected_manifest_sha256: input.discovery.provenance.manifest_sha256,
    },
  });
}

export async function updateCommunityTemplateFork(
  input: {
    id: string;
    projectId: string;
    resourceVersion: number;
    draft: CommunityManifest;
  },
  auth: RequestAuthPolicy = "product",
): Promise<CommunityTemplateFork> {
  return apiFetch<CommunityTemplateFork>(
    `/templates/community/forks/${encodeURIComponent(input.id)}`,
    {
      auth,
      method: "PATCH",
      headers: { "If-Match": `"${input.resourceVersion}"` },
      body: { project_id: input.projectId, draft: input.draft },
    },
  );
}

export async function stageCommunityTemplateFork(
  input: {
    fork: CommunityTemplateFork;
    projectId: string;
    environmentId: string;
    serviceIds: Record<string, string>;
  },
  idempotencyKey = newIdempotencyKey(),
): Promise<EnvironmentChangesetView> {
  if (getStagedForEnv(input.environmentId).length > 0) {
    throw new Error(
      "Save or discard the tab-local Changes edits before staging a community template.",
    );
  }
  const current = await apiFetch<EnvironmentChangesetView>(
    `/environments/${encodeURIComponent(input.environmentId)}/changeset`,
    { auth: "human" },
  );
  if (current.changeset?.state === "draft") {
    throw new Error(
      "This environment already has a saved draft. Apply or discard it before adding a template.",
    );
  }
  const revision = current.changeset?.revision ?? 0;
  return apiFetch<EnvironmentChangesetView>(
    `/templates/community/forks/${encodeURIComponent(input.fork.id)}/changeset`,
    {
      auth: "human",
      method: "POST",
      headers: {
        "Idempotency-Key": idempotencyKey,
        "If-Match": `"${revision}"`,
      },
      body: {
        project_id: input.projectId,
        environment_id: input.environmentId,
        expected_revision: revision,
        expected_fork_version: input.fork.resource_version,
        service_ids: input.serviceIds,
      },
    },
  );
}

export async function listCommunityTemplateForks(
  projectId: string,
  signal?: AbortSignal,
  auth: RequestAuthPolicy = "product",
): Promise<CommunityTemplateFork[]> {
  const response = await apiFetch<{ forks?: CommunityTemplateFork[] }>(
    `/templates/community/forks?project_id=${encodeURIComponent(projectId)}`,
    { signal, auth },
  );
  return response.forks ?? [];
}

export function useCommunityTemplateForks(
  projectId: string | undefined,
  auth: RequestAuthPolicy = "product",
) {
  return useQuery<CommunityTemplateFork[]>({
    queryKey: ["community-template-forks", projectId ?? "", auth],
    queryFn: ({ signal }) => listCommunityTemplateForks(projectId as string, signal, auth),
    enabled: Boolean(projectId),
  });
}

export function useTemplateProvisionRun(
  slug: string | undefined,
  runId: string | undefined,
) {
  return useQuery<TemplateProvisionRun>({
    queryKey: ["template-provision-run", slug ?? "", runId ?? ""],
    queryFn: ({ signal }) =>
      apiFetch<TemplateProvisionRun>(
        `/templates/${encodeURIComponent(slug as string)}/instances/${encodeURIComponent(runId as string)}`,
        { signal },
      ),
    enabled: Boolean(slug && runId),
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state === "queued" || state === "running" || !state ? 1_500 : false;
    },
  });
}

// ── environments (REST) ───────────────────────────────────────────

export type EnvironmentTier = "PROD" | "STAGING" | "PREVIEW" | "DEV";

export async function createEnvironment(input: {
  projectId: string;
  name: string;
  tier?: EnvironmentTier;
}, idempotencyKey = newIdempotencyKey()): Promise<Environment> {
  const projectId = input.projectId.trim();
  const tier = ({ PROD: "production", STAGING: "staging", PREVIEW: "preview", DEV: "development" } as const)[input.tier ?? "DEV"];
  const slug = slugify(input.name);
  const name = input.name.trim() || slug;
  const result = await apiFetch<Environment>("/environments", {
    auth: "human",
    method: "POST",
    body: { project_id: projectId, slug, name, tier },
    headers: { "Idempotency-Key": idempotencyKey },
  });
  if (
    !result ||
    typeof result.id !== "string" ||
    !result.id.startsWith("env_") ||
    result.project_id !== projectId ||
    result.slug !== slug ||
    result.name !== name ||
    result.tier !== tier ||
    result.is_protected !== false ||
    !Number.isSafeInteger(result.resource_version) ||
    result.resource_version < 1 ||
    typeof result.created_at !== "string" ||
    !Number.isFinite(Date.parse(result.created_at)) ||
    typeof result.updated_at !== "string" ||
    !Number.isFinite(Date.parse(result.updated_at))
  ) {
    throw new Error("Midnight returned an invalid environment response.");
  }
  return result;
}

export type EnvironmentDuplicateOmission = {
  resource: "domains" | "volumes" | "deployments";
  count: number;
  reason: string;
};

export type EnvironmentDuplicateResult = Environment & {
  copy_config?: boolean;
  copied_service_configs?: number;
  copied_variables?: number;
  copied_variable_versions?: number;
  omitted_resources?: EnvironmentDuplicateOmission[];
};

export async function duplicateEnvironment(input: {
  sourceEnvironmentId: string;
  projectId: string;
  name: string;
  slug?: string;
  copyConfig: boolean;
}): Promise<EnvironmentDuplicateResult> {
  const slug =
    input.slug && input.slug.trim() ? slugify(input.slug) : slugify(input.name);
  const headers = input.copyConfig
    ? { "Idempotency-Key": newIdempotencyKey() }
    : undefined;
  return apiFetch<EnvironmentDuplicateResult>(
    `/environments/${encodeURIComponent(input.sourceEnvironmentId)}/duplicate`,
    {
      auth: "operator",
      method: "POST",
      body: {
        project_id: input.projectId,
        slug,
        name: input.name.trim() || slug,
        copy_config: input.copyConfig,
      },
      headers,
    },
  );
}

// ── deploy a docker image into an env (GraphQL) ───────────────────
// Used by AddServiceForm when the operator picks an image source AND an
// environment is in scope: the service row is created first, then the image
// is deployed so the source actually lands somewhere.

const DEPLOY_IMAGE = `mutation Deploy($input: ServiceInstanceDeployInput!, $key: String) {
  serviceInstanceDeployV2(input: $input, idempotencyKey: $key) { id status operationId }
}`;

export async function deployServiceImage(input: {
  serviceId: string;
  environmentId: string;
  image: string;
}, idempotencyKey = newIdempotencyKey()): Promise<{ id: string; status: string; operationId?: string }> {
  const key = idempotencyKey;
  const data = await graphqlRequest<{
    serviceInstanceDeployV2: { id: string; status: string; operationId?: string };
  }>(DEPLOY_IMAGE, { input, key }, { idempotencyKey: key });
  return data.serviceInstanceDeployV2;
}

/**
 * The sole human-session deployment mutation admitted by the console.
 * The caller owns the key so a retry of the exact same body replays the same
 * durable request instead of minting a second deployment.
 */
export async function deployHumanServiceImage(
  input: {
    serviceId: string;
    environmentId: string;
    image: string;
    route?: PublicRouteRequest;
  },
  idempotencyKey: string,
): Promise<OperationResource> {
  if (
    !input.serviceId ||
    !input.environmentId ||
    !idempotencyKey ||
    !isImmutableImageReference(input.image)
  ) {
    throw new Error(
      "A service, environment, replay key, and immutable image digest are required.",
    );
  }
  if (input.route !== undefined && !isPublicRouteRequest(input.route)) {
    throw new Error(
      "A public route request must contain exactly { public: true }.",
    );
  }
  const result = await apiFetch<OperationResource>(
    `/services/${encodeURIComponent(input.serviceId)}/deployments`,
    {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: {
        environment_id: input.environmentId,
        image: input.image,
        ...(input.route ? { route: input.route } : {}),
      },
    },
  );
  if (
    !result ||
    typeof result.id !== "string" ||
    result.id.length === 0 ||
    typeof result.state !== "string" ||
    result.state.length === 0
  ) {
    throw new Error("Midnight started the deployment but sent back a response it could not read. Check Activity before retrying.");
  }
  return result;
}
