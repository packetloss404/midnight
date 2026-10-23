import { describe, expect, it } from "vitest";
import type { AuthState } from "@/auth/AuthProvider";
import type { ProjectAccess } from "@/components/panels/automation-tokens/api";
import { deriveDeploymentMutationAuthority } from "./authority";

const humanState = {
  status: "human",
  me: {},
} as AuthState;

const editorAccess: ProjectAccess = {
  principal_kind: "human",
  workspace_id: "wrk_1",
  project_id: "prj_1",
  environment_id: "env_1",
  project_role: "editor",
  environment_restricted: false,
  permissions: [
    "project.read",
    "service.write",
    "deployment.trigger.cli",
  ],
};

describe("deployment mutation authority", () => {
  it("uses server-returned RBAC without requiring recovery authority", () => {
    expect(
      deriveDeploymentMutationAuthority({
        authState: humanState,
        operatorFeaturesAvailable: false,
        projectId: "prj_1",
        environmentId: "env_1",
        access: editorAccess,
      }),
    ).toMatchObject({
      state: "available",
      canReplay: true,
      canRuntime: true,
      canCancel: true,
      detail: expect.stringContaining(
        "tenant-coordinated deployment actions",
      ),
    });
  });

  it("requires exact server-returned project access", () => {
    expect(
      deriveDeploymentMutationAuthority({
        authState: humanState,
        operatorFeaturesAvailable: true,
        projectId: "prj_1",
        environmentId: "env_1",
        access: { ...editorAccess, project_id: "prj_other" },
      }),
    ).toMatchObject({
      state: "unavailable",
      canReplay: false,
      canRuntime: false,
    });
  });

  it("keeps replay and runtime permissions distinct", () => {
    const replayOnly = deriveDeploymentMutationAuthority({
      authState: humanState,
      operatorFeaturesAvailable: true,
      projectId: "prj_1",
      environmentId: "env_1",
      access: {
        ...editorAccess,
        permissions: ["deployment.trigger.cli"],
      },
    });

    expect(replayOnly).toMatchObject({
      state: "available",
      canReplay: true,
      canRuntime: false,
      canCancel: true,
    });

    const runtimeOnly = deriveDeploymentMutationAuthority({
      authState: humanState,
      operatorFeaturesAvailable: true,
      projectId: "prj_1",
      environmentId: "env_1",
      access: {
        ...editorAccess,
        permissions: ["service.write"],
      },
    });
    expect(runtimeOnly).toMatchObject({
      state: "available",
      canReplay: false,
      canRuntime: true,
      canCancel: false,
    });
  });

  it("rejects a mismatched environment echo without relaxing actions", () => {
    expect(
      deriveDeploymentMutationAuthority({
        authState: humanState,
        operatorFeaturesAvailable: true,
        projectId: "prj_1",
        environmentId: "env_1",
        access: { ...editorAccess, environment_id: "env_other" },
      }),
    ).toMatchObject({
      state: "unavailable",
      canReplay: false,
      canRuntime: false,
      canCancel: false,
      detail: expect.stringContaining("exact environment scope"),
    });
  });

  it("recognizes the already-verified operator principal", () => {
    expect(
      deriveDeploymentMutationAuthority({
        authState: { status: "operator" },
        operatorFeaturesAvailable: true,
        projectId: "prj_1",
      }),
    ).toMatchObject({
      state: "available",
      canReplay: true,
      canRuntime: true,
      canCancel: true,
    });
  });
});
