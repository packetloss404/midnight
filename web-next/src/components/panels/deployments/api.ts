// Panel-local data layer for deployment ACTIONS (owned by the deployments
// feature). Reads stay on the shared `useDeployments` / `useOperation` hooks;
// only the mutating surface lives here because it spans two transports:
//
//   - Redeploy / Rollback / Restart / Stop / Remove → authoritative REST
//     deployment-action intake.
//   - Cancel                          → REST `POST /operations/{id}/cancel`.
//
// Every mutation invalidates the deployment/operation query keys so the list,
// the drawer, and the detail view reconcile from the server after the FSM
// advances. Nothing here is optimistic in a way that could fabricate a
// terminal state — a deploy action enqueues a NEW operation, so we invalidate
// and let the SSE event stream + refetch drive the UI.

import { useCallback, useMemo } from "react";
import {
  useMutation,
  useQueries,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query";
import { api, apiFetch, type DeploymentResource, type Service } from "@/api/client";
import { queryKeys } from "@/api/hooks";
import { useAuth } from "@/auth/AuthProvider";

// ── Idempotency ──────────────────────────────────────────────────────────

/** Fresh idempotency key per user-initiated action. */
export function newIdempotencyKey(prefix: string): string {
  const rand =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : Math.random().toString(36).slice(2) + Date.now().toString(36);
  return `${prefix}-${rand}`;
}

// ── Action capability derivation ─────────────────────────────────────────

export type DeploymentActionState = {
  pending: boolean;
  canRedeploy: boolean;
  canRollback: boolean;
  canRestart: boolean;
  canStop: boolean;
  canRemove: boolean;
  canCancel: boolean;
};

function normState(d: DeploymentResource): string {
  return (d.operation_state ?? "").toLowerCase();
}

/**
 * Runtime and replay actions use separate durable jobs.  The UI only exposes
 * actions whose preconditions are visible on the canonical deployment record;
 * the server rechecks those preconditions under its transaction lock.
 */
export function deploymentActionState(d: DeploymentResource): DeploymentActionState {
  const state = normState(d);
  const inFlight = state === "queued" || state === "running";
  const desired = (d.runtime_desired_state ?? "").toLowerCase();
  const active = d.active;
  // Legacy operation-event projections have no durable runtime state. Keep
  // controls hidden for those records rather than guessing that a deployment
  // is safe to replay or mutate.
  const runtimeKnown = desired === "running" || desired === "stopped" || desired === "removed";
  return {
    pending: inFlight,
    canRedeploy: runtimeKnown && !inFlight && desired !== "removed",
    canRollback: runtimeKnown && !inFlight && !active && desired !== "removed",
    canRestart: runtimeKnown && !inFlight && active && desired !== "removed",
    canStop: runtimeKnown && !inFlight && active && desired === "running",
    canRemove: runtimeKnown && !inFlight && !active && desired !== "removed",
    canCancel: inFlight,
  };
}

// ── REST deploy actions ──────────────────────────────────────────────────

export type DeploymentActionKind = "redeploy" | "rollback" | "stop" | "restart" | "remove";
export type DeploymentMutationCredential = "human" | "operator";

type DeployMutationField = { id: string; status: string; operationId: string };
type DeploymentActionResponse = {
  operation: { id: string; state: string };
};

/**
 * Run a deploy action against a deployment id. Resolves to the freshly
 * enqueued deployment stub (`{ id, status }`, where `id` is the new
 * operation id) so callers can follow its log stream immediately.
 */
export async function runDeploymentAction(
  kind: DeploymentActionKind,
  serviceId: string,
  deploymentId: string,
  credential: DeploymentMutationCredential,
  signal?: AbortSignal,
): Promise<DeployMutationField> {
  const key = newIdempotencyKey(kind);
  const data = await apiFetch<DeploymentActionResponse>(
    `/services/${encodeURIComponent(serviceId)}/deployments/${encodeURIComponent(deploymentId)}/actions/${kind}`,
    {
      auth: credential,
      method: "POST",
      headers: { "Idempotency-Key": key },
      signal,
    },
  );
  return {
    id: data.operation.id,
    status: data.operation.state,
    operationId: data.operation.id,
  };
}

// ── REST cancel ──────────────────────────────────────────────────────────

/** Cancel an in-flight operation. 202 Accepted (or idempotent replay). */
export async function cancelOperation(
  operationId: string,
  credential: DeploymentMutationCredential,
  signal?: AbortSignal,
): Promise<void> {
  await apiFetch<void>(`/operations/${encodeURIComponent(operationId)}/cancel`, {
    auth: credential,
    method: "POST",
    headers: { "Idempotency-Key": newIdempotencyKey("cancel") },
    signal,
  });
}

// ── Mutation hooks (with cache invalidation) ─────────────────────────────

/**
 * Actions for a single deployment. On success we invalidate the deployment
 * list + latest-active + this operation's record for the owning service, so
 * every surface reconciles from the server once the FSM has advanced.
 */
export function useDeploymentActions(deployment: DeploymentResource, environmentId?: string) {
  const auth = useAuth();
  const credential: DeploymentMutationCredential =
    auth.state.status === "human" ? "human" : "operator";
  const qc = useQueryClient();
  const serviceId = deployment.service_id;

  const invalidate = useCallback(() => {
    qc.invalidateQueries({ queryKey: queryKeys.deployments(serviceId, environmentId) });
    qc.invalidateQueries({ queryKey: queryKeys.latestActiveDeployment(serviceId, environmentId) });
    qc.invalidateQueries({ queryKey: queryKeys.environmentResources(environmentId) });
    qc.invalidateQueries({ queryKey: queryKeys.operation(deployment.operation_id) });
  }, [qc, serviceId, environmentId, deployment.operation_id]);

  const action = useMutation<DeployMutationField, Error, DeploymentActionKind>({
    mutationFn: (kind) =>
      runDeploymentAction(kind, serviceId, deployment.id, credential),
    onSuccess: invalidate,
  });

  const cancel = useMutation<void, Error, void>({
    mutationFn: () => cancelOperation(deployment.operation_id, credential),
    onSuccess: invalidate,
  });

  return { action, cancel };
}

// ── Cross-service project feed ───────────────────────────────────────────

export type DeploymentFeedItem = {
  service: Service;
  deployment: DeploymentResource;
};

export type ProjectDeploymentsFeed = {
  items: DeploymentFeedItem[];
  isPending: boolean;
  isFetching: boolean;
  error: Error | null;
  /** True while at least one listed deployment is queued/running. */
  hasInFlight: boolean;
  refetchAll: () => void;
};

const INFLIGHT_STATES = new Set(["queued", "running"]);

/**
 * Fan a per-service deployment list across every service in the project and
 * merge into one newest-first activity feed. No project-wide REST endpoint
 * exists yet, so we compose `api.listDeployments` per service with
 * `useQueries` (sharing the same env-scoped cache keys as `useDeployments`,
 * so the drawer and this feed never double-fetch). While anything is
 * in-flight the queries poll so the feed advances in near-real-time; the
 * selected deployment's own log stream is live via `useOperationEvents`.
 */
export function useProjectDeploymentsFeed(
  services: Service[],
  environmentId?: string,
): ProjectDeploymentsFeed {
  const queries: UseQueryOptions<DeploymentResource[], Error, DeploymentResource[], readonly unknown[]>[] =
    services.map((s) => ({
      queryKey: queryKeys.deployments(s.id, environmentId),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.listDeployments(s.id, environmentId, signal),
      enabled: Boolean(s.id),
      // Poll while any deployment for this service is still in-flight.
      refetchInterval: (query: { state: { data?: DeploymentResource[] } }) => {
        const data = query.state.data;
        const active = data?.some((d) => INFLIGHT_STATES.has((d.operation_state ?? "").toLowerCase()));
        return active ? 4000 : false;
      },
    }));
  const results = useQueries({ queries });

  const byService = useMemo(() => new Map(services.map((s) => [s.id, s])), [services]);

  return useMemo(() => {
    const items: DeploymentFeedItem[] = [];
    let hasInFlight = false;
    results.forEach((res, i) => {
      const service = services[i];
      if (!service) return;
      for (const deployment of res.data ?? []) {
        items.push({ service: byService.get(service.id) ?? service, deployment });
        if (INFLIGHT_STATES.has((deployment.operation_state ?? "").toLowerCase())) hasInFlight = true;
      }
    });
    items.sort((a, b) => {
      const at = new Date(a.deployment.last_event_at ?? 0).getTime();
      const bt = new Date(b.deployment.last_event_at ?? 0).getTime();
      return (Number.isNaN(bt) ? 0 : bt) - (Number.isNaN(at) ? 0 : at);
    });
    const firstError = results.find((r) => r.error)?.error ?? null;
    return {
      items,
      isPending: results.length > 0 && results.some((r) => r.isPending),
      isFetching: results.some((r) => r.isFetching),
      error: (firstError as Error) ?? null,
      hasInFlight,
      refetchAll: () => results.forEach((r) => void r.refetch()),
    };
  }, [results, services, byService]);
}
