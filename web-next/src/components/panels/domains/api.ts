import {
  useMutation,
  useQuery,
  type UseQueryResult,
} from "@tanstack/react-query";
import { apiFetch } from "@/api/client";
import { stageChange } from "@/lib/stagedChanges";

const GENERATED_DOMAIN_FIELDS = new Set([
  "id",
  "workspace_id",
  "project_id",
  "service_id",
  "environment_id",
  "kind",
  "hostname",
  "path",
  "target_port",
  "public",
  "tls",
  "version",
  "route_apply_state",
  "route_reachable",
  "created_at",
  "updated_at",
]);

const ROUTE_APPLY_STATES = [
  "pending",
  "private",
  "blocked_tls",
  "blocked_backend",
  "file_applied",
  "ready",
  "failed",
] as const;

export type RouteApplyState = (typeof ROUTE_APPLY_STATES)[number];

export type GeneratedDomainRecord = {
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
  route_apply_state: RouteApplyState;
  route_reachable: boolean;
  created_at: string;
  updated_at: string;
};

export type GeneratedDomainTarget = {
  projectId: string;
  serviceId: string;
  environmentId: string;
};

export type DomainStateView = {
  pillStatus: string;
  label: string;
  description: string;
  inFlight: boolean;
};

export const domainKeys = {
  all: ["tenant-generated-domains", "human"] as const,
  list: (target: GeneratedDomainTarget) =>
    [
      "tenant-generated-domains",
      "human",
      target.projectId,
      target.serviceId,
      target.environmentId,
    ] as const,
};

export function normalizeDomainState(
  record: GeneratedDomainRecord,
): DomainStateView {
  if (record.route_apply_state === "ready" && record.route_reachable) {
    return {
      pillStatus: "active",
      label: "Trusted ingress ready",
      description:
        "Traefik acknowledged the current route group and the trusted Host-header ingress probe succeeded. Public DNS and browser reachability are not proven.",
      inFlight: false,
    };
  }
  if (record.route_apply_state === "failed") {
    return {
      pillStatus: "error",
      label: "Route group failed",
      description:
        "The environment route group could not reconcile. A sibling route may be responsible; check the deployment and networking controller.",
      inFlight: false,
    };
  }
  if (record.route_apply_state === "blocked_backend") {
    return {
      pillStatus: "pending",
      label: "Route group waiting",
      description:
        "The environment route group does not yet have its complete ready backend set. A sibling route may be responsible.",
      inFlight: true,
    };
  }
  if (record.route_apply_state === "file_applied") {
    return {
      pillStatus: "verifying",
      label: "Verifying route group",
      description:
        "Routing is written. Midnight is waiting for the proxy to confirm it and for a successful health check.",
      inFlight: true,
    };
  }
  if (record.route_apply_state === "ready") {
    return {
      pillStatus: "verifying",
      label: "Checking routing",
      description:
        "Routing is ready, but this domain has not yet passed its health check.",
      inFlight: true,
    };
  }
  if (
    record.route_apply_state === "private" ||
    record.route_apply_state === "blocked_tls"
  ) {
    return {
      pillStatus: "error",
      label: "Route group blocked",
      description:
        "Routing for this environment is not in a public, plain-HTTP state. Another domain that is private, HTTPS, or blocked may be the cause.",
      inFlight: false,
    };
  }
  return {
    pillStatus: "pending",
    label: "Pending",
    description:
      "Saved. Waiting for Midnight to set up routing.",
    inFlight: true,
  };
}

export async function fetchGeneratedDomains(
  target: GeneratedDomainTarget,
  signal?: AbortSignal,
): Promise<GeneratedDomainRecord[]> {
  assertTarget(target);
  const result = await apiFetch<unknown>(collectionPath(target), {
    auth: "human",
    signal,
  });
  if (!Array.isArray(result)) throw invalidResponse();
  const records = result.map((value) => validateGeneratedDomain(value, target));
  const ids = new Set<string>();
  for (const record of records) {
    if (ids.has(record.id)) throw invalidResponse();
    ids.add(record.id);
  }
  return records.sort((a, b) => a.hostname.localeCompare(b.hostname));
}

export async function createGeneratedDomain(
  target: GeneratedDomainTarget,
  idempotencyKey = createGeneratedDomainIntentKey("create"),
): Promise<void> {
  assertTarget(target);
  assertIdempotencyKey(idempotencyKey);
  stageChange({
    kind: "networking",
    serviceId: target.serviceId,
    environmentId: target.environmentId,
    name: "generated public domain",
    op: "add",
    networking: { kind: "generated", public: true },
  });
}

export async function deleteGeneratedDomain(
  target: GeneratedDomainTarget,
  domainId: string,
  expectedVersion: number,
  idempotencyKey = createGeneratedDomainIntentKey("delete"),
): Promise<void> {
  assertTarget(target);
  if (!domainId.trim()) throw new Error("A valid domain identity is required.");
  if (!Number.isInteger(expectedVersion) || expectedVersion < 1) {
    throw new Error("A positive domain version is required.");
  }
  assertIdempotencyKey(idempotencyKey);
  stageChange({
    kind: "networking",
    serviceId: target.serviceId,
    environmentId: target.environmentId,
    name: "generated public domain",
    op: "remove",
    targetId: domainId,
    expectedVersion,
    networking: { kind: "generated", public: true },
  });
}

export function createGeneratedDomainIntentKey(operation: "create" | "delete") {
  try {
    return `web-generated-domain-${operation}-${crypto.randomUUID()}`;
  } catch {
    return `web-generated-domain-${operation}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

export function useServiceDomains(
  target: GeneratedDomainTarget,
  enabled: boolean,
): UseQueryResult<GeneratedDomainRecord[], Error> {
  return useQuery<GeneratedDomainRecord[], Error>({
    queryKey: domainKeys.list(target),
    queryFn: ({ signal }) => fetchGeneratedDomains(target, signal),
    enabled:
      enabled &&
      Boolean(target.projectId && target.serviceId && target.environmentId),
    refetchInterval: (query) =>
      query.state.data?.some(
        (record) => normalizeDomainState(record).inFlight,
      )
        ? 8000
        : false,
  });
}

export function useCreateDomain(target: GeneratedDomainTarget) {
  return useMutation<void, Error>({
    mutationFn: () => createGeneratedDomain(target),
  });
}

export function useDeleteDomain(target: GeneratedDomainTarget) {
  return useMutation<
    void,
    Error,
    { domainId: string; expectedVersion: number }
  >({
    mutationFn: ({ domainId, expectedVersion }) =>
      deleteGeneratedDomain(target, domainId, expectedVersion),
  });
}

function collectionPath(target: GeneratedDomainTarget): string {
  return `/services/${encodeURIComponent(target.serviceId)}/environments/${encodeURIComponent(target.environmentId)}/domains`;
}

function validateGeneratedDomain(
  value: unknown,
  target: GeneratedDomainTarget,
): GeneratedDomainRecord {
  if (!isRecord(value) || !hasOnlyFields(value, GENERATED_DOMAIN_FIELDS)) {
    throw invalidResponse();
  }
  const record = value as Partial<GeneratedDomainRecord>;
  if (
    typeof record.id !== "string" ||
    !record.id ||
    typeof record.workspace_id !== "string" ||
    !record.workspace_id ||
    record.project_id !== target.projectId ||
    record.service_id !== target.serviceId ||
    record.environment_id !== target.environmentId ||
    record.kind !== "generated" ||
    typeof record.hostname !== "string" ||
    !validHostname(record.hostname) ||
    record.path !== "/" ||
    !Number.isInteger(record.target_port) ||
    (record.target_port ?? 0) < 1 ||
    (record.target_port ?? 0) > 65535 ||
    record.public !== true ||
    record.tls !== false ||
    !Number.isInteger(record.version) ||
    (record.version ?? 0) < 1 ||
    !isRouteApplyState(record.route_apply_state) ||
    typeof record.route_reachable !== "boolean" ||
    (record.route_reachable && record.route_apply_state !== "ready") ||
    !isTimestamp(record.created_at) ||
    !isTimestamp(record.updated_at)
  ) {
    throw invalidResponse();
  }
  return record as GeneratedDomainRecord;
}

function assertTarget(target: GeneratedDomainTarget): void {
  if (
    !target.projectId.trim() ||
    !target.serviceId.trim() ||
    !target.environmentId.trim()
  ) {
    throw new Error(
      "A valid project, service, and environment scope is required.",
    );
  }
}

function assertIdempotencyKey(value: string): void {
  if (!value || value.length > 255 || /\s/.test(value)) {
    console.error(
      "midnight: refusing domain write without a valid idempotency key",
    );
    throw new Error("Something went wrong preparing that request. Please try again.", {
      cause: "invalid domain idempotency key",
    });
  }
}

function isRouteApplyState(value: unknown): value is RouteApplyState {
  return (ROUTE_APPLY_STATES as readonly unknown[]).includes(value);
}

function validHostname(value: string): boolean {
  if (
    value !== value.trim().toLowerCase() ||
    value.length < 3 ||
    value.length > 253
  ) {
    return false;
  }
  const labels = value.split(".");
  return (
    labels.length >= 2 &&
    labels.every(
      (label) =>
        label.length >= 1 &&
        label.length <= 63 &&
        /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label),
    )
  );
}

function isTimestamp(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    Number.isFinite(Date.parse(value))
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasOnlyFields(
  value: Record<string, unknown>,
  allowed: ReadonlySet<string>,
): boolean {
  return Object.keys(value).every((field) => allowed.has(field));
}

function invalidResponse(): Error {
  return new Error("Midnight returned an invalid generated-domain response.");
}
