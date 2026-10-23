import { useMemo } from "react";
import { useAuth, type AuthState } from "@/auth/AuthProvider";
import {
  useProjectAccess,
  type ProjectAccess,
} from "@/components/panels/automation-tokens/api";
import type { CapabilityState } from "@/lib/capabilities";

const DEPLOY_PERMISSION = "deployment.trigger.cli";
const SERVICE_WRITE_PERMISSION = "service.write";

export type DeploymentMutationAuthority = {
  state: CapabilityState;
  canReplay: boolean;
  canRuntime: boolean;
  canCancel: boolean;
  detail: string;
};

export type DeploymentMutationAuthorityInput = {
  authState: AuthState;
  operatorFeaturesAvailable: boolean;
  projectId?: string;
  environmentId?: string;
  access?: ProjectAccess;
  accessPending?: boolean;
  accessError?: unknown;
};

/**
 * Combine server-returned RBAC with the human deployment-action transport.
 * Replay, runtime actions, and cancellation use tenant-coordinated REST
 * routes. Cancellation shares deployment.trigger.cli with deploy admission;
 * the server still reauthorizes the exact operation lineage atomically.
 */
export function deriveDeploymentMutationAuthority({
  authState,
  projectId,
  environmentId,
  access,
  accessPending,
  accessError,
}: DeploymentMutationAuthorityInput): DeploymentMutationAuthority {
  if (authState.status === "operator" || authState.status === "developer") {
    return {
      state: "available",
      canReplay: true,
      canRuntime: true,
      canCancel: true,
      detail:
        "The current privileged control-plane principal can use installed deployment mutation routes.",
    };
  }

  if (authState.status !== "human") {
    return unavailable(
      "Deployment mutation authority is unavailable until authentication completes.",
    );
  }
  if (!projectId) {
    return unavailable(
      "This deployment does not report a project, so Midnight cannot verify project-scoped mutation permissions.",
    );
  }
  if (accessPending) {
    return {
      ...unavailable("Checking server-reported project permissions…"),
      state: "degraded",
    };
  }
  if (accessError || !access || access.project_id !== projectId) {
    return unavailable(
      "Midnight could not verify project-scoped deployment permissions. No mutation request will be sent.",
    );
  }
  if (environmentId && access.environment_id !== environmentId) {
    return unavailable(
      "Midnight could not verify the exact environment scope for deployment actions. No mutation request will be sent.",
    );
  }

  const permissions = new Set(access.permissions);
  const roleCanReplay = permissions.has(DEPLOY_PERMISSION);
  const roleCanRuntime = permissions.has(SERVICE_WRITE_PERMISSION);

  if (!roleCanReplay && !roleCanRuntime) {
    return unavailable(
      "The server-reported project permissions do not allow deployment mutations.",
    );
  }
  return {
    state: "available",
    canReplay: roleCanReplay,
    canRuntime: roleCanRuntime,
    canCancel: roleCanReplay,
    detail:
      "Server-reported project permissions permit tenant-coordinated deployment actions, including safe-point cancellation for deployment-trigger roles.",
  };
}

export function useDeploymentMutationAuthority(
  projectId: string | undefined,
  environmentId?: string,
): DeploymentMutationAuthority {
  const auth = useAuth();
  const human = auth.state.status === "human";
  const access = useProjectAccess(
    projectId ?? "",
    environmentId,
    human && Boolean(projectId),
  );

  return useMemo(
    () =>
      deriveDeploymentMutationAuthority({
        authState: auth.state,
        operatorFeaturesAvailable: auth.operatorFeaturesAvailable,
        projectId,
        environmentId,
        access: access.data,
        accessPending: access.isPending,
        accessError: access.error,
      }),
    [
      access.data,
      access.error,
      access.isPending,
      auth.operatorFeaturesAvailable,
      auth.state,
      environmentId,
      projectId,
    ],
  );
}

function unavailable(detail: string): DeploymentMutationAuthority {
  return {
    state: "unavailable",
    canReplay: false,
    canRuntime: false,
    canCancel: false,
    detail,
  };
}
