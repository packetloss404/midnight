// Panel-local read hooks for InstanceSettings: the single-VM "server"
// concerns. Backed by two REST reads:
//   - GET /dashboard/summary            → version + counts
//   - GET /dashboard/runtime-inventory  → hosts / agents / schema version
//
// These stay beside InstanceSettings because no other surface consumes them.

import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type DashboardSummary = {
  status?: string;
  api_version?: string;
  server_version?: string;
  project_count?: number;
  service_count?: number;
  operation_count?: number;
  audit_status?: string;
  audit_event_count?: number;
};

export type RuntimeHost = {
  name: string;
  profile?: string;
  docker?: string;
  cgroup?: string;
  doctor?: string;
  agent_id?: string;
  version?: string;
  last_heartbeat?: string;
};

export type RuntimeAgent = {
  agent_id: string;
  hostname?: string;
  version?: string;
  status?: string;
  runtime?: string;
  last_heartbeat?: string;
  maintenance_mode?: boolean;
  drain_mode?: boolean;
};

export type RuntimeInventory = {
  schema_version?: string;
  generated_at?: string;
  hosts?: RuntimeHost[];
  agents?: RuntimeAgent[];
  services?: unknown[];
  projects?: unknown[];
};

export type InstanceDomains = {
  configured: boolean;
  root_domain?: string;
  console_hostname?: string;
  apps_domain?: string;
  ingress_target?: string;
  active_apps_domain?: string;
  version?: number;
  updated_at?: string;
  restart_required?: boolean;
};

export type SaveInstanceDomainsInput = {
  rootDomain: string;
  ingressTarget: string;
  expectedVersion: number;
};

type QueryOpts<T> = Omit<UseQueryOptions<T, Error, T>, "queryKey" | "queryFn">;

export function useDashboardSummary(opts?: QueryOpts<DashboardSummary>) {
  return useQuery<DashboardSummary>({
    queryKey: ["dashboard", "summary"],
    queryFn: ({ signal }) => apiFetch<DashboardSummary>("/dashboard/summary", { signal }),
    ...opts,
  });
}

export function useRuntimeInventory(opts?: QueryOpts<RuntimeInventory>) {
  return useQuery<RuntimeInventory>({
    queryKey: ["dashboard", "runtime-inventory"],
    queryFn: ({ signal }) =>
      apiFetch<RuntimeInventory>("/dashboard/runtime-inventory", { signal }),
    ...opts,
  });
}

export function useInstanceDomains(opts?: QueryOpts<InstanceDomains>) {
  return useQuery<InstanceDomains>({
    queryKey: ["instance", "domains"],
    queryFn: ({ signal }) => apiFetch<InstanceDomains>("/instance/domains", { signal }),
    ...opts,
  });
}

export function useSaveInstanceDomains() {
  const queryClient = useQueryClient();
  return useMutation<InstanceDomains, Error, SaveInstanceDomainsInput>({
    mutationFn: ({ rootDomain, ingressTarget, expectedVersion }) =>
      apiFetch<InstanceDomains>("/instance/domains", {
        method: "PUT",
        headers: { "If-Match": `"${expectedVersion}"` },
        body: {
          root_domain: rootDomain,
          ingress_target: ingressTarget,
          expected_version: expectedVersion,
        },
      }),
    onSuccess: (value) => {
      queryClient.setQueryData(["instance", "domains"], value);
    },
  });
}
