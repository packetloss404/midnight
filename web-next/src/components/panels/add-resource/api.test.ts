import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createOperatorVolume,
  deployOperatorTemplate,
  listResourceServices,
  resolveResourceCapabilities,
} from "./api";
import {
  setHumanSessionToken,
  setOperatorRecoveryToken,
} from "@/api/credentials";

const access = (environmentId?: string) => ({
  principal_kind: "human" as const,
  workspace_id: "wrk_1",
  project_id: "prj_1",
  environment_id: environmentId,
  environment_restricted: Boolean(environmentId),
  project_role: "editor" as const,
  permissions: [
    "project.read",
    "service.write",
    "volume.write",
    "deployment.trigger.cli",
    "deployment.trigger.git",
  ],
});

describe("add-resource capabilities", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setHumanSessionToken(undefined);
    setOperatorRecoveryToken(undefined);
  });
  it("admits only server-granted human flows", () => {
    const result = resolveResourceCapabilities({
      authority: "human",
      projectId: "prj_1",
      environmentId: "env_1",
      projectAccess: access(),
      environmentAccess: access("env_1"),
    });
    expect(result.empty.available).toBe(true);
    expect(result.image.available).toBe(true);
    expect(result.github.available).toBe(true);
    expect(result.template.available).toBe(false);
    expect(result.database.available).toBe(false);
    expect(result.volume.available).toBe(false);
  });

  it("allows installation features for an instance admin only with exact resource access", () => {
    const result = resolveResourceCapabilities({
      authority: "human",
      installationAdminAuthorized: true,
      projectId: "prj_1",
      environmentId: "env_1",
      projectAccess: access(),
      environmentAccess: access("env_1"),
    });
    expect(result.template.available).toBe(true);
    expect(result.database.available).toBe(true);
    expect(result.volume.available).toBe(true);

    const wrongScope = resolveResourceCapabilities({
      authority: "human",
      installationAdminAuthorized: true,
      projectId: "prj_1",
      environmentId: "env_1",
      projectAccess: { ...access(), project_id: "prj_other" },
      environmentAccess: { ...access("env_1"), project_id: "prj_other" },
    });
    expect(wrongScope.template.available).toBe(false);
    expect(wrongScope.volume.available).toBe(false);

    const readOnlyAdmin = resolveResourceCapabilities({
      authority: "human",
      installationAdminAuthorized: true,
      projectId: "prj_1",
      projectAccess: { ...access(), permissions: ["project.read"] },
    });
    expect(readOnlyAdmin.template.available).toBe(false);
    expect(readOnlyAdmin.database.available).toBe(false);

    const noVolumeWrite = resolveResourceCapabilities({
      authority: "human",
      installationAdminAuthorized: true,
      projectId: "prj_1",
      environmentId: "env_1",
      projectAccess: access(),
      environmentAccess: {
        ...access("env_1"),
        permissions: ["project.read", "service.write"],
      },
    });
    expect(noVolumeWrite.volume.available).toBe(false);
  });

  it("requires exact project and environment bindings", () => {
    const result = resolveResourceCapabilities({
      authority: "human",
      projectId: "prj_1",
      environmentId: "env_1",
      projectAccess: { ...access(), project_id: "prj_other" },
      environmentAccess: { ...access("env_1"), environment_id: "env_other" },
    });
    expect(result.empty.available).toBe(false);
    expect(result.image.available).toBe(false);
    expect(result.github.available).toBe(false);
  });

  it("passes stable idempotency keys to executable provisioning APIs", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ id: "vol_1" }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ operation_id: "op_1" }), {
          status: 202,
          headers: { "content-type": "application/json" },
        }),
      );

    await createOperatorVolume(
      {
        projectId: "prj_1",
        environmentId: "env_1",
        service: { id: "svc_1" },
        name: "data",
        mountPath: "/data",
        sizeBytes: 1024,
      },
      "volume-stable",
    );
    await deployOperatorTemplate(
      "postgres",
      { projectId: "prj_1", environmentId: "env_1", name: "db" },
      "template-stable",
    );

    expect((fetchMock.mock.calls[0][1] as RequestInit).headers).toMatchObject({
      "Idempotency-Key": "volume-stable",
    });
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toMatchObject({
      "Idempotency-Key": "template-stable",
    });
  });

  it("never falls back to the recovery credential for human resource reads or writes", async () => {
    setHumanSessionToken("midn_user_admin");
    setOperatorRecoveryToken("midn_admin_recovery");
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(jsonResponse([{ id: "svc_1" }]))
      .mockResolvedValueOnce(jsonResponse({ id: "vol_1" }))
      .mockResolvedValueOnce(jsonResponse({ operation_id: "op_1" }, 202));

    await listResourceServices("prj_1", "env_1", "human");
    await createOperatorVolume(
      {
        projectId: "prj_1",
        environmentId: "env_1",
        service: { id: "svc_1" },
        name: "data",
        mountPath: "/data",
        sizeBytes: 1024,
      },
      "volume-human",
      "human",
    );
    setHumanSessionToken(undefined);
    await deployOperatorTemplate(
      "postgres",
      { projectId: "prj_1", environmentId: "env_1" },
      "template-human",
      "human",
    );

    expect((fetchMock.mock.calls[0][1] as RequestInit).headers).toMatchObject({
      Authorization: "Bearer midn_user_admin",
    });
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toMatchObject({
      Authorization: "Bearer midn_user_admin",
    });
    expect((fetchMock.mock.calls[2][1] as RequestInit).headers).not.toHaveProperty(
      "Authorization",
    );
    setOperatorRecoveryToken(undefined);
  });
});

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "content-type": "application/json" },
  });
}
