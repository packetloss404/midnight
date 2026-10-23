import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DeploymentResource } from "@/api/client";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ api: {}, apiFetch }));

import {
  cancelOperation,
  deploymentActionState,
  runDeploymentAction,
} from "./api";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("deploymentActionState", () => {
  it("hides actions for legacy records without durable runtime state", () => {
    const deployment = {
      id: "dep_1",
      deployment_id: "dep_1",
      service_id: "svc_1",
      operation_id: "op_1",
      operation_state: "SUCCEEDED",
      status: "ACTIVE",
      active: true,
      image: "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    } satisfies DeploymentResource;

    expect(deploymentActionState(deployment)).toMatchObject({
      canRedeploy: false,
      canRollback: false,
      canRestart: false,
      canCancel: false,
    });
  });

  it("keeps real in-flight operation cancellation available", () => {
    const deployment = {
      id: "dep_2",
      service_id: "svc_1",
      operation_id: "op_2",
      operation_state: "RUNNING",
      status: "DEPLOYING",
      active: false,
    } satisfies DeploymentResource;

    expect(deploymentActionState(deployment).canCancel).toBe(true);
  });

  it("exposes only durable actions that match runtime preconditions", () => {
    const active = {
      id: "dep_active",
      service_id: "svc_1",
      operation_id: "op_active",
      operation_state: "SUCCEEDED",
      status: "ACTIVE",
      active: true,
      runtime_desired_state: "running",
    } satisfies DeploymentResource;
    expect(deploymentActionState(active)).toMatchObject({
      canRedeploy: true,
      canRestart: true,
      canStop: true,
      canRollback: false,
      canRemove: false,
    });

    const prior = { ...active, id: "dep_prior", active: false, runtime_desired_state: "stopped" } satisfies DeploymentResource;
    expect(deploymentActionState(prior)).toMatchObject({
      canRollback: true,
      canRemove: true,
      canRestart: false,
      canStop: false,
    });
  });
});

describe("deployment action REST surface", () => {
  it("uses the tenant-coordinated service/deployment route", async () => {
    apiFetch.mockResolvedValueOnce({
      operation: { id: "op_restart", state: "QUEUED" },
    });

    await expect(
      runDeploymentAction("restart", "svc/one", "dep one", "human"),
    ).resolves.toEqual({
      id: "op_restart",
      status: "QUEUED",
      operationId: "op_restart",
    });

    expect(apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fone/deployments/dep%20one/actions/restart",
      {
        auth: "human",
        method: "POST",
        headers: { "Idempotency-Key": expect.any(String) },
        signal: undefined,
      },
    );
  });

  it("uses an empty idempotent request for operation cancellation", async () => {
    apiFetch.mockResolvedValueOnce(undefined);

    await expect(cancelOperation("op /one", "human")).resolves.toBeUndefined();

    expect(apiFetch).toHaveBeenCalledWith("/operations/op%20%2Fone/cancel", {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": expect.stringMatching(/^cancel-/) },
      signal: undefined,
    });
  });
});
