import { beforeEach, describe, expect, it, vi } from "vitest";

const transport = vi.hoisted(() => ({
  apiFetch: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  apiFetch: transport.apiFetch,
}));

import {
  createTenantVariable,
  deleteTenantVariable,
  fetchTenantVariables,
  updateTenantVariable,
  type TenantVariableMeta,
  type TenantVariableReference,
} from "./tenant-api";

const target = {
  projectId: "prj_1",
  serviceId: "svc/web",
  environmentId: "env_prod",
};

function metadata(
  overrides: Partial<TenantVariableMeta> = {},
): TenantVariableMeta {
  return {
    id: "var_1",
    workspace_id: "wrk_1",
    project_id: target.projectId,
    environment_id: target.environmentId,
    service_id: target.serviceId,
    name: "API_TOKEN",
    scope: "secret",
    version: 1,
    last_rotated_at: "2026-07-29T12:00:00Z",
    length_bucket: "17-64",
    kek_fingerprint: "kek-test",
    value_kind: "literal",
    references: [],
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("tenant variable API", () => {
  it("lists exact service-owned metadata with explicit human auth", async () => {
    transport.apiFetch.mockResolvedValue([metadata()]);

    await expect(fetchTenantVariables(target)).resolves.toEqual([metadata()]);
    expect(transport.apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fweb/environments/env_prod/variables",
      { auth: "human", signal: undefined },
    );
  });

  it("lists and updates a safe legacy hyphenated metadata name", async () => {
    const legacy = metadata({ name: "LEGACY-NAME" });
    transport.apiFetch.mockResolvedValueOnce([legacy]);

    await expect(fetchTenantVariables(target)).resolves.toEqual([legacy]);

    transport.apiFetch.mockResolvedValueOnce(
      metadata({ name: "LEGACY-NAME", version: 5 }),
    );
    await expect(
      updateTenantVariable({
        ...target,
        variableId: "var_1",
        name: "LEGACY-NAME",
        value: "replacement",
        expectedVersion: 4,
        idempotencyKey: "legacy-update",
      }),
    ).resolves.toMatchObject({ name: "LEGACY-NAME", version: 5 });
  });

  it("fails closed on shared, cross-scope, duplicate, or plaintext-bearing metadata", async () => {
    const { value_kind: _valueKind, ...missingValueKind } = metadata();
    for (const response of [
      [metadata({ service_id: "" })],
      [metadata({ environment_id: "env_other" })],
      [metadata({ name: "BAD=NAME" })],
      [metadata({ name: " \t" })],
      [metadata(), metadata({ id: "var_2" })],
      [missingValueKind],
      [{ ...metadata(), value: "must-not-be-trusted" }],
    ]) {
      transport.apiFetch.mockResolvedValueOnce(response);
      await expect(fetchTenantVariables(target)).rejects.toThrow(
        "invalid variable metadata response",
      );
    }
  });

  it("rejects invalid create names and accepts the 255-byte boundary", async () => {
    for (const name of ["LEGACY-NAME", "A".repeat(256)]) {
      await expect(
        createTenantVariable({
          ...target,
          name,
          scope: "secret",
          value: "sensitive-value",
          idempotencyKey: "invalid-name",
        }),
      ).rejects.toThrow("valid variable name");
    }
    expect(transport.apiFetch).not.toHaveBeenCalled();

    const boundaryName = "A".repeat(255);
    transport.apiFetch.mockResolvedValueOnce(metadata({ name: boundaryName }));
    await expect(
      createTenantVariable({
        ...target,
        name: boundaryName,
        scope: "secret",
        value: "sensitive-value",
        idempotencyKey: "boundary-name",
      }),
    ).resolves.toMatchObject({ name: boundaryName });
  });

  it("rejects every unknown metadata or reference field", async () => {
    for (const response of [
      [{ ...metadata(), created_at: "2026-07-29T12:00:00Z" }],
      [{ ...metadata(), resolved_value: "must-not-be-trusted" }],
      [
        metadata({
          value_kind: "reference",
          references: [
            {
              kind: "service_var",
              service: "database",
              name: "DATABASE_URL",
              resolved_value: "must-not-be-trusted",
            } as TenantVariableReference,
          ],
        }),
      ],
    ]) {
      transport.apiFetch.mockResolvedValueOnce(response);
      await expect(fetchTenantVariables(target)).rejects.toThrow(
        "invalid variable metadata response",
      );
    }
  });

  it("creates with one caller-stable key and validates exact version-one metadata", async () => {
    transport.apiFetch.mockResolvedValue(metadata());

    await expect(
      createTenantVariable({
        ...target,
        name: "API_TOKEN",
        scope: "secret",
        value: "sensitive-value",
        idempotencyKey: "stable-create",
      }),
    ).resolves.toEqual(metadata());

    expect(transport.apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fweb/environments/env_prod/variables",
      {
        auth: "human",
        method: "POST",
        headers: { "Idempotency-Key": "stable-create" },
        body: {
          name: "API_TOKEN",
          scope: "secret",
          value: "sensitive-value",
        },
      },
    );

    transport.apiFetch.mockResolvedValueOnce(metadata({ version: 2 }));
    await expect(
      createTenantVariable({
        ...target,
        name: "API_TOKEN",
        scope: "secret",
        value: "sensitive-value",
        idempotencyKey: "stable-create-two",
      }),
    ).rejects.toThrow("invalid variable metadata response");
  });

  it("updates the exact item with CAS and validates the next version", async () => {
    transport.apiFetch.mockResolvedValue(
      metadata({ id: "var/1", scope: "sealed", version: 5 }),
    );

    await expect(
      updateTenantVariable({
        ...target,
        variableId: "var/1",
        name: "API_TOKEN",
        scope: "sealed",
        value: "replacement",
        expectedVersion: 4,
        idempotencyKey: "stable-update",
      }),
    ).resolves.toMatchObject({ version: 5, scope: "sealed" });

    expect(transport.apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fweb/environments/env_prod/variables/var%2F1",
      {
        auth: "human",
        method: "PATCH",
        headers: { "Idempotency-Key": "stable-update" },
        body: {
          scope: "sealed",
          value: "replacement",
          expected_version: 4,
        },
      },
    );

    transport.apiFetch.mockResolvedValueOnce(metadata({ version: 7 }));
    await expect(
      updateTenantVariable({
        ...target,
        variableId: "var_1",
        name: "API_TOKEN",
        value: "replacement",
        expectedVersion: 4,
        idempotencyKey: "stable-update-two",
      }),
    ).rejects.toThrow("invalid variable metadata response");
  });

  it("deletes with only the exact version and requires an empty response", async () => {
    transport.apiFetch.mockResolvedValue(undefined);

    await expect(
      deleteTenantVariable({
        ...target,
        variableId: "var_1",
        expectedVersion: 4,
        idempotencyKey: "stable-delete",
      }),
    ).resolves.toBeUndefined();

    expect(transport.apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fweb/environments/env_prod/variables/var_1",
      {
        auth: "human",
        method: "DELETE",
        headers: { "Idempotency-Key": "stable-delete" },
        body: { expected_version: 4 },
      },
    );

    transport.apiFetch.mockResolvedValueOnce({ deleted: true });
    await expect(
      deleteTenantVariable({
        ...target,
        variableId: "var_1",
        expectedVersion: 4,
        idempotencyKey: "stable-delete-two",
      }),
    ).rejects.toThrow("invalid variable metadata response");
  });

  it("rejects incomplete scope and invalid retry keys before transport", async () => {
    await expect(
      fetchTenantVariables({ ...target, environmentId: "" }),
    ).rejects.toThrow("valid project, service, and environment scope");
    await expect(
      createTenantVariable({
        ...target,
        name: "API_TOKEN",
        scope: "secret",
        value: "value",
        idempotencyKey: "contains whitespace",
      }),
    ).rejects.toThrow("Something went wrong preparing that request");
    expect(transport.apiFetch).not.toHaveBeenCalled();
  });
});
