// Exact human-session service/environment variable transport.
//
// This module is intentionally separate from the recovery-only `/variables`
// client. Tenant requests never fall back to the installation recovery token,
// and every metadata response is checked against the selected project,
// service, and environment before the UI trusts it.

import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export const TENANT_VARIABLE_SCOPES = [
  "plain",
  "secret",
  "sealed",
] as const;

export const TENANT_VARIABLE_NAME_MAX_BYTES = 255;
const TENANT_VARIABLE_CREATE_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

const TENANT_VARIABLE_META_FIELDS = new Set([
  "id",
  "scope",
  "workspace_id",
  "project_id",
  "environment_id",
  "service_id",
  "name",
  "version",
  "last_rotated_at",
  "length_bucket",
  "kek_fingerprint",
  "value_kind",
  "references",
]);

const TENANT_VARIABLE_REFERENCE_FIELDS = new Set([
  "kind",
  "service",
  "name",
]);

export type TenantVariableScope = (typeof TENANT_VARIABLE_SCOPES)[number];

export type TenantVariableReference = {
  kind: "service_var" | "shared" | "local" | "builtin";
  service?: string;
  name: string;
};

export type TenantVariableMeta = {
  id: string;
  scope: TenantVariableScope;
  workspace_id: string;
  project_id: string;
  environment_id: string;
  service_id: string;
  name: string;
  version: number;
  last_rotated_at: string;
  length_bucket:
    | "empty"
    | "1-16"
    | "17-64"
    | "65-256"
    | "257-1024"
    | "1024+";
  kek_fingerprint: string;
  value_kind: "literal" | "reference";
  references?: TenantVariableReference[];
};

export type TenantVariableTarget = {
  projectId: string;
  serviceId: string;
  environmentId: string;
};

export type FetchTenantVariablesInput = TenantVariableTarget & {
  signal?: AbortSignal;
};

export type CreateTenantVariableInput = TenantVariableTarget & {
  name: string;
  scope: TenantVariableScope;
  value: string;
  idempotencyKey: string;
};

export type UpdateTenantVariableInput = TenantVariableTarget & {
  variableId: string;
  name: string;
  scope?: TenantVariableScope;
  value: string;
  expectedVersion: number;
  idempotencyKey: string;
};

export type DeleteTenantVariableInput = TenantVariableTarget & {
  variableId: string;
  expectedVersion: number;
  idempotencyKey: string;
};

export const tenantVariableKeys = {
  list: (target: TenantVariableTarget) =>
    [
      "tenant-service-environment-variables",
      "human",
      target.projectId,
      target.serviceId,
      target.environmentId,
    ] as const,
};

function collectionPath(target: TenantVariableTarget): string {
  return `/services/${encodeURIComponent(target.serviceId)}/environments/${encodeURIComponent(target.environmentId)}/variables`;
}

function itemPath(
  target: TenantVariableTarget,
  variableId: string,
): string {
  return `${collectionPath(target)}/${encodeURIComponent(variableId)}`;
}

export async function fetchTenantVariables(
  input: FetchTenantVariablesInput,
): Promise<TenantVariableMeta[]> {
  assertTarget(input);
  const result = await apiFetch<unknown>(collectionPath(input), {
    auth: "human",
    signal: input.signal,
  });
  if (!Array.isArray(result)) {
    throw invalidResponse();
  }
  const variables = result.map((value) => validateVariableMeta(value, input));
  const ids = new Set<string>();
  const names = new Set<string>();
  for (const variable of variables) {
    if (ids.has(variable.id) || names.has(variable.name)) {
      throw invalidResponse();
    }
    ids.add(variable.id);
    names.add(variable.name);
  }
  return variables.sort((a, b) => a.name.localeCompare(b.name));
}

export async function createTenantVariable(
  input: CreateTenantVariableInput,
): Promise<TenantVariableMeta> {
  assertTarget(input);
  assertCreateVariableName(input.name);
  assertScope(input.scope);
  assertIdempotencyKey(input.idempotencyKey);
  const result = await apiFetch<unknown>(collectionPath(input), {
    auth: "human",
    method: "POST",
    headers: { "Idempotency-Key": input.idempotencyKey },
    body: {
      name: input.name,
      scope: input.scope,
      value: input.value,
    },
  });
  const created = validateVariableMeta(result, input);
  if (
    created.name !== input.name ||
    created.scope !== input.scope ||
    created.version !== 1
  ) {
    throw invalidResponse();
  }
  return created;
}

export async function updateTenantVariable(
  input: UpdateTenantVariableInput,
): Promise<TenantVariableMeta> {
  assertTarget(input);
  assertVariableID(input.variableId);
  assertSafeMetadataVariableName(input.name);
  if (input.scope !== undefined) {
    assertScope(input.scope);
  }
  assertExpectedVersion(input.expectedVersion);
  assertIdempotencyKey(input.idempotencyKey);
  const body: {
    value: string;
    expected_version: number;
    scope?: TenantVariableScope;
  } = {
    value: input.value,
    expected_version: input.expectedVersion,
  };
  if (input.scope !== undefined) {
    body.scope = input.scope;
  }
  const result = await apiFetch<unknown>(
    itemPath(input, input.variableId),
    {
      auth: "human",
      method: "PATCH",
      headers: { "Idempotency-Key": input.idempotencyKey },
      body,
    },
  );
  const updated = validateVariableMeta(result, input);
  if (
    updated.id !== input.variableId ||
    updated.name !== input.name ||
    (input.scope !== undefined && updated.scope !== input.scope) ||
    updated.version !== input.expectedVersion + 1
  ) {
    throw invalidResponse();
  }
  return updated;
}

export async function deleteTenantVariable(
  input: DeleteTenantVariableInput,
): Promise<void> {
  assertTarget(input);
  assertVariableID(input.variableId);
  assertExpectedVersion(input.expectedVersion);
  assertIdempotencyKey(input.idempotencyKey);
  const result = await apiFetch<unknown>(
    itemPath(input, input.variableId),
    {
      auth: "human",
      method: "DELETE",
      headers: { "Idempotency-Key": input.idempotencyKey },
      body: { expected_version: input.expectedVersion },
    },
  );
  if (result !== undefined) {
    throw invalidResponse();
  }
}

export function createTenantVariableIntentKey(): string {
  try {
    return `web-tenant-variable-${crypto.randomUUID()}`;
  } catch {
    return `web-tenant-variable-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

export function useTenantVariables(
  target: TenantVariableTarget,
  enabled: boolean,
) {
  return useQuery<TenantVariableMeta[], Error>({
    queryKey: tenantVariableKeys.list(target),
    queryFn: ({ signal }) => fetchTenantVariables({ ...target, signal }),
    enabled:
      enabled &&
      Boolean(target.projectId && target.serviceId && target.environmentId),
  });
}

function assertTarget(target: TenantVariableTarget): void {
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

function assertVariableID(variableId: string): void {
  if (!variableId.trim()) {
    throw new Error("A valid variable identity is required.");
  }
}

function assertCreateVariableName(name: string): void {
  if (!isValidTenantVariableCreateName(name)) {
    throw new Error("A valid variable name is required.");
  }
}

function assertSafeMetadataVariableName(name: string): void {
  if (!isSafeTenantVariableMetadataName(name)) {
    throw new Error("A valid variable name is required.");
  }
}

export function isValidTenantVariableCreateName(name: string): boolean {
  return (
    name.length > 0 &&
    utf8ByteLength(name) <= TENANT_VARIABLE_NAME_MAX_BYTES &&
    TENANT_VARIABLE_CREATE_NAME.test(name)
  );
}

function assertScope(
  scope: TenantVariableScope,
): asserts scope is TenantVariableScope {
  if (!(TENANT_VARIABLE_SCOPES as readonly string[]).includes(scope)) {
    throw new Error("A valid variable scope is required.");
  }
}

function assertExpectedVersion(version: number): void {
  if (!Number.isInteger(version) || version < 1) {
    throw new Error("A positive variable version is required.");
  }
}

function assertIdempotencyKey(key: string): void {
  if (!key || key.length > 255 || /\s/.test(key)) {
    console.error(
      "midnight: refusing variable write without a valid idempotency key",
    );
    throw new Error("Something went wrong preparing that request. Please try again.", {
      cause: "invalid variable idempotency key",
    });
  }
}

function validateVariableMeta(
  value: unknown,
  target: TenantVariableTarget,
): TenantVariableMeta {
  if (
    !isRecord(value) ||
    !hasOnlyFields(value, TENANT_VARIABLE_META_FIELDS)
  ) {
    throw invalidResponse();
  }
  const variable = value as Partial<TenantVariableMeta>;
  if (
    typeof variable.id !== "string" ||
    !variable.id ||
    variable.project_id !== target.projectId ||
    variable.environment_id !== target.environmentId ||
    variable.service_id !== target.serviceId ||
    typeof variable.workspace_id !== "string" ||
    !variable.workspace_id ||
    typeof variable.name !== "string" ||
    !isSafeTenantVariableMetadataName(variable.name) ||
    !isScope(variable.scope) ||
    !Number.isInteger(variable.version) ||
    (variable.version ?? 0) < 1 ||
    !isTimestamp(variable.last_rotated_at) ||
    !isLengthBucket(variable.length_bucket) ||
    typeof variable.kek_fingerprint !== "string" ||
    !variable.kek_fingerprint ||
    (variable.value_kind !== "literal" &&
      variable.value_kind !== "reference") ||
    !validReferences(variable.references)
  ) {
    throw invalidResponse();
  }
  return variable as TenantVariableMeta;
}

function validReferences(
  value: TenantVariableReference[] | undefined,
): boolean {
  if (value === undefined) return true;
  if (!Array.isArray(value)) return false;
  return value.every(
    (reference) =>
      isRecord(reference) &&
      hasOnlyFields(reference, TENANT_VARIABLE_REFERENCE_FIELDS) &&
      (reference.kind === "service_var" ||
        reference.kind === "shared" ||
        reference.kind === "local" ||
        reference.kind === "builtin") &&
      typeof reference.name === "string" &&
      reference.name.length > 0 &&
      (reference.service === undefined ||
        (typeof reference.service === "string" &&
          reference.service.length > 0)),
  );
}

function isScope(value: unknown): value is TenantVariableScope {
  return (
    typeof value === "string" &&
    (TENANT_VARIABLE_SCOPES as readonly string[]).includes(value)
  );
}

function isLengthBucket(
  value: unknown,
): value is TenantVariableMeta["length_bucket"] {
  return (
    typeof value === "string" &&
    ["empty", "1-16", "17-64", "65-256", "257-1024", "1024+"].includes(
      value,
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

function isSafeTenantVariableMetadataName(name: string): boolean {
  return name.trim().length > 0 && !/[=\u0000\r\n]/.test(name);
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasOnlyFields(
  value: Record<string, unknown>,
  allowedFields: ReadonlySet<string>,
): boolean {
  return Object.keys(value).every((field) => allowedFields.has(field));
}

function invalidResponse(): Error {
  return new Error("Midnight returned an invalid variable metadata response.");
}
