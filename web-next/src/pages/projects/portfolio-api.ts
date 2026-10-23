import { useMemo } from "react";
import { useQueries, type UseQueryOptions } from "@tanstack/react-query";
import {
  apiFetch,
  type DeploymentResource,
  type DeploymentResourcePage,
  type Service,
} from "@/api/client";

export const MAX_PORTFOLIO_SERVICE_PROBES = 12;

export type PortfolioDeploymentEvidence = {
  deployments: DeploymentResource[];
  sampledServiceCount: number;
  totalServiceCount: number;
  truncated: boolean;
  isPending: boolean;
  error: Error | null;
  refetchAll: () => void;
};

export async function listLatestServiceDeployment(
  serviceId: string,
  signal?: AbortSignal,
): Promise<DeploymentResource | undefined> {
  const response = await apiFetch<DeploymentResource[] | DeploymentResourcePage>(
    `/services/${encodeURIComponent(serviceId)}/deployments?limit=1`,
    { auth: "product", signal },
  );
  const items = Array.isArray(response) ? response : response?.items;
  if (!Array.isArray(items) || items.length > 1) {
    throw new Error("Midnight returned an invalid latest-deployment page.");
  }
  const deployment = items[0];
  if (deployment && deployment.service_id !== serviceId) {
    throw new Error("Midnight returned a deployment that belongs to another service.");
  }
  return deployment;
}

export function usePortfolioDeploymentEvidence(
  services: Service[],
): PortfolioDeploymentEvidence {
  const sampled = services.slice(0, MAX_PORTFOLIO_SERVICE_PROBES);
  const queries: UseQueryOptions<
    DeploymentResource | undefined,
    Error,
    DeploymentResource | undefined,
    readonly unknown[]
  >[] = sampled.map((service) => ({
    queryKey: ["portfolio", "latest-deployment", service.id],
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      listLatestServiceDeployment(service.id, signal),
    enabled: Boolean(service.id),
    refetchInterval: (query: {
      state: { data?: DeploymentResource };
    }) => {
      const state = query.state.data?.operation_state?.toLowerCase();
      return state === "queued" || state === "running" ? 4_000 : false;
    },
  }));
  const results = useQueries({ queries });

  return useMemo(() => ({
    deployments: results.flatMap((result) =>
      result.data ? [result.data] : [],
    ),
    sampledServiceCount: sampled.length,
    totalServiceCount: services.length,
    truncated: services.length > sampled.length,
    isPending: results.length > 0 && results.some((result) => result.isPending),
    error: (results.find((result) => result.error)?.error as Error) ?? null,
    refetchAll: () => results.forEach((result) => void result.refetch()),
  }), [results, sampled.length, services.length]);
}
