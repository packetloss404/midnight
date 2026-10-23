// Shared deploy-intent mutations wired to the REST deploy routes.
//
// Both the canvas tile actions and the command palette drive deploys from
// here so idempotency-key generation and cache invalidation live in ONE
// place.
//
// Two shapes live here on purpose:
//
//   - `redeploy` / `restart` act on an EXISTING deployment and therefore need
//     its id — callers typically pass the service's latest active deployment
//     id (see `useLatestActiveDeployment`).
//   - `deployNow` needs no deployment id at all. It is the first-deployment
//     action for a service that has a linked source or has run an image
//     before: the API derives the intent from durable service state
//     (`POST /services/{id}/deployments` with `{"deploy_now": true}`) and
//     produces the same operation, deployment record, and queue job an
//     explicit request would. Without it a freshly source-linked service has
//     no way to produce its first deployment from the console, because
//     linking deliberately starts nothing and every other action needs a
//     deployment that does not exist yet.

import { useCallback } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { useAuth } from "@/auth/AuthProvider";

function idempotencyKey(): string {
  try {
    if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
      return crypto.randomUUID();
    }
  } catch {
    /* fall through */
  }
  return `idem_${Date.now()}_${Math.random().toString(36).slice(2)}`;
}

type DeployResult = { id: string; status: string; operationId: string };
type DeploymentActionResponse = {
  operation: { id: string; state: string };
};

async function runAction(
  serviceId: string,
  deploymentId: string,
  action: "redeploy" | "restart",
  credential: "human" | "operator",
): Promise<DeployResult> {
  const result = await apiFetch<DeploymentActionResponse>(
    `/services/${encodeURIComponent(serviceId)}/deployments/${encodeURIComponent(deploymentId)}/actions/${action}`,
    {
      auth: credential,
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey() },
    },
  );
  return {
    id: result.operation.id,
    status: result.operation.state,
    operationId: result.operation.id,
  };
}

type OperationAccepted = { id: string; state: string };

/**
 * Start a deployment for a service that has a source or a previously used
 * image, without needing an existing deployment id. The API resolves what to
 * deploy from durable state and applies the same RBAC, idempotency, and
 * operation/deployment records as an explicit deploy request.
 */
export async function deployServiceNow(
  serviceId: string,
  environmentId: string,
  credential: "human" | "operator" = "operator",
  key: string = idempotencyKey(),
): Promise<DeployResult> {
  if (!serviceId) throw new Error("service id is required");
  if (!environmentId) {
    throw new Error("An environment is required before Midnight can deploy.");
  }
  const operation = await apiFetch<OperationAccepted>(
    `/services/${encodeURIComponent(serviceId)}/deployments`,
    {
      auth: credential,
      method: "POST",
      headers: { "Idempotency-Key": key },
      body: { environment_id: environmentId, deploy_now: true },
    },
  );
  return {
    id: operation.id,
    status: operation.state,
    operationId: operation.id,
  };
}

/**
 * Redeploy + restart mutations scoped to a service. On success we invalidate
 * every `["deployments", serviceId, …]` cache entry (list + latest-active) so
 * the node's StatusPill flips to its new in-flight tone immediately.
 */
export function useDeploymentActions(serviceId: string | undefined) {
  const qc = useQueryClient();
  const auth = useAuth();
  const credential = auth.state.status === "human" ? "human" : "operator";

  const invalidate = useCallback(() => {
    if (!serviceId) return;
    qc.invalidateQueries({ queryKey: ["deployments", serviceId] });
    qc.invalidateQueries({ queryKey: ["environment-resources"] });
  }, [qc, serviceId]);

  const redeploy = useMutation<DeployResult, Error, string>({
    mutationFn: (deploymentId) => {
      if (!serviceId) throw new Error("service id is required");
      return runAction(serviceId, deploymentId, "redeploy", credential);
    },
    onSuccess: invalidate,
  });

  const restart = useMutation<DeployResult, Error, string>({
    mutationFn: (deploymentId) => {
      if (!serviceId) throw new Error("service id is required");
      return runAction(serviceId, deploymentId, "restart", credential);
    },
    onSuccess: invalidate,
  });

  const deployNow = useMutation<DeployResult, Error, string>({
    mutationFn: (environmentId) => {
      if (!serviceId) throw new Error("service id is required");
      return deployServiceNow(serviceId, environmentId, credential);
    },
    onSuccess: invalidate,
  });

  return { redeploy, restart, deployNow };
}
