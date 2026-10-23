import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";
import type { GeneratedDomainTarget, RouteApplyState } from "./api";

export type CustomDomainRecord = {
  id: string;
  workspace_id: string;
  project_id: string;
  service_id: string;
  environment_id: string;
  kind: "custom";
  hostname: string;
  path: "/";
  target_port: number;
  public: boolean;
  tls: boolean;
  version: number;
  ownership_state: "pending" | "verified" | "failed";
  route_apply_state: RouteApplyState;
  route_reachable: boolean;
  routing_dns: {
    record_type: string;
    record_name: string;
    record_value?: string;
    state: "operator_required" | "unavailable" | "configured" | "invalid";
    note: string;
  };
  dns_challenge: {
    record_name: string;
    record_value: string;
    state: "pending" | "verified" | "failed";
    attempts: number;
    checked_at?: string;
    verified_at?: string;
    last_error?: string;
  };
  created_at: string;
  updated_at: string;
};

const fields = new Set([
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
  "ownership_state",
  "route_apply_state",
  "route_reachable",
  "dns_challenge",
  "routing_dns",
  "created_at",
  "updated_at",
]);
const routingDNSFields = new Set([
  "record_type",
  "record_name",
  "record_value",
  "state",
  "note",
]);
const challengeFields = new Set([
  "record_name",
  "record_value",
  "state",
  "attempts",
  "checked_at",
  "verified_at",
  "last_error",
]);
const ownershipStates = new Set(["pending", "verified", "failed"]);
const routeApplyStates = new Set([
  "pending",
  "private",
  "blocked_tls",
  "blocked_backend",
  "file_applied",
  "ready",
  "failed",
]);
const routingDNSStates = new Set([
  "operator_required",
  "unavailable",
  "configured",
  "invalid",
]);

export const customDomainKeys = {
  list: (target: GeneratedDomainTarget) =>
    [
      "tenant-custom-domains",
      "human",
      target.projectId,
      target.serviceId,
      target.environmentId,
    ] as const,
};

export async function fetchCustomDomains(
  target: GeneratedDomainTarget,
  signal?: AbortSignal,
): Promise<CustomDomainRecord[]> {
  const value = await apiFetch<unknown>(collectionPath(target), {
    auth: "human",
    signal,
  });
  if (!Array.isArray(value)) throw invalid();
  return value
    .map((entry) => validate(entry, target))
    .sort((a, b) => a.hostname.localeCompare(b.hostname));
}

export async function createCustomDomain(
  target: GeneratedDomainTarget,
  hostname: string,
  tls = false,
): Promise<CustomDomainRecord> {
  if (!validHostname(hostname)) {
    throw new Error(
      "Enter a lowercase hostname without a protocol, port, path, or wildcard.",
    );
  }
  const value = await apiFetch<unknown>(collectionPath(target), {
    auth: "human",
    method: "POST",
    headers: { "Idempotency-Key": key("create") },
    body: { hostname, tls },
  });
  return validate(value, target);
}

export async function verifyCustomDomain(
  target: GeneratedDomainTarget,
  domainId: string,
  expectedVersion: number,
): Promise<CustomDomainRecord> {
  requireMutationTarget(domainId, expectedVersion);
  const value = await apiFetch<unknown>(
    `${collectionPath(target)}/${encodeURIComponent(domainId)}/verify`,
    {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": key("verify") },
      body: { expected_version: expectedVersion },
    },
  );
  return validate(value, target);
}

export async function deleteCustomDomain(
  target: GeneratedDomainTarget,
  domainId: string,
  expectedVersion: number,
): Promise<void> {
  requireMutationTarget(domainId, expectedVersion);
  const value = await apiFetch<unknown>(
    `${collectionPath(target)}/${encodeURIComponent(domainId)}`,
    {
      auth: "human",
      method: "DELETE",
      headers: {
        "Idempotency-Key": key("delete"),
        "If-Match": `"${expectedVersion}"`,
      },
      body: { expected_version: expectedVersion },
    },
  );
  if (value !== undefined) throw invalid();
}

export function useCustomDomains(target: GeneratedDomainTarget, enabled: boolean) {
  return useQuery<CustomDomainRecord[], Error>({
    queryKey: customDomainKeys.list(target),
    queryFn: ({ signal }) => fetchCustomDomains(target, signal),
    enabled,
    refetchInterval: (query) =>
      query.state.data?.some(
        (domain) =>
          domain.ownership_state !== "verified" ||
          (domain.route_apply_state !== "ready" && domain.public),
      )
        ? 8_000
        : false,
  });
}

export function useCreateCustomDomain(target: GeneratedDomainTarget) {
  const client = useQueryClient();
  return useMutation<
    CustomDomainRecord,
    Error,
    { hostname: string; tls: boolean }
  >({
    mutationFn: ({ hostname, tls }) =>
      createCustomDomain(target, hostname, tls),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: customDomainKeys.list(target) }),
  });
}

export function useVerifyCustomDomain(target: GeneratedDomainTarget) {
  const client = useQueryClient();
  return useMutation<
    CustomDomainRecord,
    Error,
    { domainId: string; expectedVersion: number }
  >({
    mutationFn: ({ domainId, expectedVersion }) =>
      verifyCustomDomain(target, domainId, expectedVersion),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: customDomainKeys.list(target) }),
  });
}

export function useDeleteCustomDomain(target: GeneratedDomainTarget) {
  const client = useQueryClient();
  return useMutation<
    void,
    Error,
    { domainId: string; expectedVersion: number }
  >({
    mutationFn: ({ domainId, expectedVersion }) =>
      deleteCustomDomain(target, domainId, expectedVersion),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: customDomainKeys.list(target) }),
  });
}

function collectionPath(target: GeneratedDomainTarget): string {
  return `/services/${encodeURIComponent(target.serviceId)}/environments/${encodeURIComponent(target.environmentId)}/custom-domains`;
}

function key(operation: string): string {
  try {
    return `web-custom-domain-${operation}-${crypto.randomUUID()}`;
  } catch {
    return `web-custom-domain-${operation}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

function validate(
  value: unknown,
  target: GeneratedDomainTarget,
): CustomDomainRecord {
  if (!isRecord(value) || !hasOnly(value, fields)) throw invalid();
  const routingDNS = value.routing_dns;
  const challenge = value.dns_challenge;
  if (
    value.project_id !== target.projectId ||
    value.service_id !== target.serviceId ||
    value.environment_id !== target.environmentId ||
    value.kind !== "custom" ||
    !isNonEmptyString(value.id) ||
    !isNonEmptyString(value.workspace_id) ||
    !validHostname(value.hostname) ||
    value.path !== "/" ||
    !isIntegerInRange(value.target_port, 1, 65_535) ||
    typeof value.public !== "boolean" ||
    typeof value.tls !== "boolean" ||
    !isIntegerInRange(value.version, 1) ||
    !isAllowedString(value.ownership_state, ownershipStates) ||
    !isAllowedString(value.route_apply_state, routeApplyStates) ||
    typeof value.route_reachable !== "boolean" ||
    !isRecord(routingDNS) ||
    !hasOnly(routingDNS, routingDNSFields) ||
    !isNonEmptyString(routingDNS.record_type) ||
    !isNonEmptyString(routingDNS.record_name) ||
    !isOptionalString(routingDNS.record_value) ||
    !isAllowedString(routingDNS.state, routingDNSStates) ||
    typeof routingDNS.note !== "string" ||
    !isRecord(challenge) ||
    !hasOnly(challenge, challengeFields) ||
    !isNonEmptyString(challenge.record_name) ||
    !isNonEmptyString(challenge.record_value) ||
    !isAllowedString(challenge.state, ownershipStates) ||
    !isIntegerInRange(challenge.attempts, 0) ||
    !isOptionalDate(challenge.checked_at) ||
    !isOptionalDate(challenge.verified_at) ||
    !isOptionalString(challenge.last_error) ||
    !isDate(value.created_at) ||
    !isDate(value.updated_at)
  ) {
    throw invalid();
  }
  return value as CustomDomainRecord;
}

function requireMutationTarget(domainId: string, expectedVersion: number): void {
  if (!domainId.trim() || !isIntegerInRange(expectedVersion, 1)) {
    throw new Error("A custom domain and positive expected version are required.");
  }
}

function validHostname(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const labels = value.toLowerCase().split(".");
  return (
    value === value.trim().toLowerCase() &&
    labels.length >= 2 &&
    value.length <= 253 &&
    labels.every((label) =>
      /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label),
    )
  );
}

function isDate(value: unknown): value is string {
  return typeof value === "string" && Number.isFinite(Date.parse(value));
}

function isOptionalDate(value: unknown): value is string | undefined {
  return value === undefined || isDate(value);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

function isOptionalString(value: unknown): value is string | undefined {
  return value === undefined || typeof value === "string";
}

function isIntegerInRange(
  value: unknown,
  minimum: number,
  maximum = Number.MAX_SAFE_INTEGER,
): value is number {
  return (
    typeof value === "number" &&
    Number.isSafeInteger(value) &&
    value >= minimum &&
    value <= maximum
  );
}

function isAllowedString(
  value: unknown,
  allowed: ReadonlySet<string>,
): value is string {
  return typeof value === "string" && allowed.has(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasOnly(
  value: Record<string, unknown>,
  allowed: ReadonlySet<string>,
): boolean {
  return Object.keys(value).every((field) => allowed.has(field));
}

function invalid(): Error {
  return new Error("Midnight returned an invalid custom-domain response.");
}
