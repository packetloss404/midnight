// TanStack Query hooks for the Midnight v1alpha REST surface.
//
// Query keys are tuple-shaped and ENVIRONMENT-SCOPED where the underlying
// resource is: the `?env=` selection threads into the key so switching
// environments refetches cleanly and two envs never share a cache slot.
//
// Shared hooks wrap concrete REST or SSE calls. Feature-specific surfaces
// keep their data adapters beside the panel when their contracts are not
// reused elsewhere.

import {
  useEffect,
  useRef,
  useState,
} from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query";
import {
  api,
  type CreateProjectInput,
  type CreateServiceInput,
  type DeploymentResource,
  type Environment,
  type EnvironmentChangesetView,
  type EnvironmentResourceSummary,
  type GithubInstallation,
  type GithubRepo,
  type IdentityWorkspace,
  type OperationEvent,
  type OperationResource,
  type Project,
  type Service,
  type ServiceDeletionReceipt,
  type VariableMeta,
} from "./client";
import { connectSSE } from "./sse";

type QueryOpts<T> = Omit<UseQueryOptions<T, Error, T>, "queryKey" | "queryFn">;

export const queryKeys = {
  workspaces: () => ["identity", "workspaces"] as const,
  projects: () => ["projects"] as const,
  project: (id: string) => ["projects", id] as const,
  services: (projectId?: string, env?: string) =>
    ["services", projectId ?? null, env ?? null] as const,
  service: (id: string) => ["service", id] as const,
  environments: (projectId?: string) =>
    ["environments", projectId ?? null] as const,
  environmentResources: (environmentId?: string) =>
    ["environment-resources", environmentId ?? null] as const,
  environmentChangeset: (environmentId?: string) =>
    ["environment-changeset", environmentId ?? null] as const,
  deployments: (serviceId: string, env?: string) =>
    ["deployments", serviceId, env ?? null] as const,
  latestActiveDeployment: (serviceId: string, env?: string) =>
    ["deployments", serviceId, "latest-active", env ?? null] as const,
  operation: (id: string) => ["operation", id] as const,
  operationLogs: (id: string) => ["operation", id, "logs"] as const,
  variables: (serviceId?: string, env?: string) =>
    ["variables", serviceId ?? null, env ?? null] as const,
  domains: (serviceId: string, env?: string) =>
    ["domains", serviceId, env ?? null] as const,
  githubInstallations: () => ["github", "installations"] as const,
  githubRepos: (installationId?: string) =>
    installationId
      ? (["github", "installations", installationId, "repos"] as const)
      : (["github", "repos"] as const),
  dashboardSummary: () => ["dashboard", "summary"] as const,
};

export function useIdentityWorkspaces(opts?: QueryOpts<IdentityWorkspace[]>) {
  return useQuery<IdentityWorkspace[]>({
    queryKey: queryKeys.workspaces(),
    queryFn: ({ signal }) => api.listIdentityWorkspaces(signal),
    ...opts,
  });
}

// ── Projects ─────────────────────────────────────────────────────

export function useProjects(opts?: QueryOpts<Project[]>) {
  return useQuery<Project[]>({
    queryKey: queryKeys.projects(),
    queryFn: ({ signal }) => api.listProjects(signal),
    ...opts,
  });
}

export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateProjectInput) => api.createProject(input),
    onSuccess: (project) => {
      qc.invalidateQueries({ queryKey: queryKeys.projects() });
      qc.setQueryData(queryKeys.project(project.id), project);
    },
  });
}

export function useProject(id: string | undefined, opts?: QueryOpts<Project>) {
  return useQuery<Project>({
    queryKey: queryKeys.project(id ?? ""),
    queryFn: ({ signal }) => api.getProject(id as string, signal),
    enabled: Boolean(id),
    ...opts,
  });
}

// ── Environments ─────────────────────────────────────────────────

export function useEnvironments(
  projectId: string | undefined,
  opts?: QueryOpts<Environment[]>,
) {
  return useQuery<Environment[]>({
    queryKey: queryKeys.environments(projectId),
    queryFn: ({ signal }) => api.listEnvironments(projectId, signal),
    enabled: Boolean(projectId),
    ...opts,
  });
}

export function useEnvironmentResources(
  environmentId: string | undefined,
  opts?: QueryOpts<EnvironmentResourceSummary>,
) {
  return useQuery<EnvironmentResourceSummary>({
    queryKey: queryKeys.environmentResources(environmentId),
    queryFn: ({ signal }) =>
      api.getEnvironmentResources(environmentId as string, signal),
    enabled: Boolean(environmentId),
    refetchInterval: (query) => {
      const resources = query.state.data?.resources ?? [];
      const inFlight = resources.some(({ latest_deployment: deployment }) => {
        const state = (
          deployment?.operation?.state ??
          deployment?.status ??
          ""
        ).toLowerCase();
        return (
          state === "queued" ||
          state === "running" ||
          state === "building" ||
          state === "deploying"
        );
      });
      return inFlight ? 4_000 : false;
    },
    ...opts,
  });
}

export function useEnvironmentChangeset(
  environmentId: string | undefined,
  opts?: QueryOpts<EnvironmentChangesetView>,
) {
  return useQuery<EnvironmentChangesetView>({
    queryKey: queryKeys.environmentChangeset(environmentId),
    queryFn: ({ signal }) =>
      api.getEnvironmentChangeset(environmentId as string, signal),
    enabled: Boolean(environmentId),
    ...opts,
  });
}

// ── Services (env-scoped) ────────────────────────────────────────

export function useServices(
  projectId: string | undefined,
  environmentId?: string,
  opts?: QueryOpts<Service[]>,
) {
  return useQuery<Service[]>({
    queryKey: queryKeys.services(projectId, environmentId),
    queryFn: ({ signal }) =>
      api.listServices({ projectId, environmentId }, signal),
    enabled: Boolean(projectId),
    ...opts,
  });
}

export function useCreateService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateServiceInput) => api.createService(input),
    onSuccess: (service) => {
      qc.invalidateQueries({ queryKey: queryKeys.services(service.project_id) });
      qc.invalidateQueries({ queryKey: queryKeys.services() });
      qc.invalidateQueries({ queryKey: ["environment-resources"] });
      qc.setQueryData(queryKeys.service(service.id), service);
    },
  });
}

export function useService(id: string | undefined, opts?: QueryOpts<Service>) {
  return useQuery<Service>({
    queryKey: queryKeys.service(id ?? ""),
    queryFn: ({ signal }) => api.getService(id as string, signal),
    enabled: Boolean(id),
    ...opts,
  });
}

/**
 * Delete a service.
 *
 * The mutation carries the service's current `resource_version` so a stale
 * console cannot delete something that changed underneath it. On success every
 * cached view that could still name the service is invalidated, and the
 * service's own cache entry is dropped rather than left showing a resource
 * that no longer exists.
 */
export function useDeleteService(projectId: string | undefined) {
  const qc = useQueryClient();
  return useMutation<
    ServiceDeletionReceipt,
    Error,
    { serviceId: string; expectedVersion: number }
  >({
    mutationFn: ({ serviceId, expectedVersion }) =>
      api.deleteService(serviceId, expectedVersion),
    onSuccess: (_receipt, { serviceId }) => {
      qc.removeQueries({ queryKey: queryKeys.service(serviceId) });
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["environment-resources"] });
      qc.invalidateQueries({ queryKey: queryKeys.dashboardSummary() });
      if (projectId) {
        qc.invalidateQueries({ queryKey: queryKeys.project(projectId) });
      }
    },
  });
}

// ── Deployments ──────────────────────────────────────────────────

export function useDeployments(
  serviceId: string | undefined,
  environmentId?: string,
  opts?: QueryOpts<DeploymentResource[]>,
) {
  return useQuery<DeploymentResource[]>({
    queryKey: queryKeys.deployments(serviceId ?? "", environmentId),
    queryFn: ({ signal }) =>
      api.listDeployments(serviceId as string, environmentId, signal),
    enabled: Boolean(serviceId),
    ...opts,
  });
}

/**
 * Latest active (live-traffic) deployment for a service. 404 = no active
 * deployment yet, which we surface as `undefined` rather than an error so
 * status surfaces can render a neutral "no deploy" state.
 */
export function useLatestActiveDeployment(
  serviceId: string | undefined,
  environmentId?: string,
  opts?: QueryOpts<DeploymentResource | null>,
) {
  return useQuery<DeploymentResource | null>({
    queryKey: queryKeys.latestActiveDeployment(serviceId ?? "", environmentId),
    queryFn: async ({ signal }) => {
      try {
        return await api.latestActiveDeployment(
          serviceId as string,
          environmentId,
          signal,
        );
      } catch (err) {
        if (err && typeof err === "object" && "status" in err && (err as { status: number }).status === 404) {
          return null;
        }
        throw err;
      }
    },
    enabled: Boolean(serviceId),
    retry: false,
    ...opts,
  });
}

// ── Operations + events (SSE) ────────────────────────────────────

export function useOperation(
  id: string | undefined,
  opts?: QueryOpts<OperationResource>,
) {
  return useQuery<OperationResource>({
    queryKey: queryKeys.operation(id ?? ""),
    queryFn: ({ signal }) => api.getOperation(id as string, signal),
    enabled: Boolean(id),
    ...opts,
  });
}

/**
 * Live deploy-progress events for an operation via SSE. Accumulates
 * `event: operation` frames into an ordered, de-duplicated (by seq) list.
 * The stream owns its own connection and closes on unmount / id change.
 */
export function useOperationEvents(operationId: string | undefined): {
  events: OperationEvent[];
  connected: boolean;
  error: string | null;
} {
  const [events, setEvents] = useState<OperationEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const seen = useRef<Set<number>>(new Set());

  useEffect(() => {
    if (!operationId) {
      seen.current = new Set();
      setEvents([]);
      setError(null);
      setConnected(false);
      return;
    }
    seen.current = new Set();
    setEvents([]);
    setError(null);
    setConnected(true);

    const handle = connectSSE(
      `/operations/${encodeURIComponent(operationId)}/events?stream=sse`,
      {
        auth: "product",
        onMessage: (msg) => {
          if (msg.event !== "operation") return;
          try {
            const ev = JSON.parse(msg.data) as OperationEvent;
            if (seen.current.has(ev.seq)) return;
            seen.current.add(ev.seq);
            setEvents((prev) =>
              [...prev, ev].sort((a, b) => a.seq - b.seq),
            );
          } catch {
            /* ignore malformed frame */
          }
        },
        onError: (e) => {
          setError(e.message);
          setConnected(false);
        },
        onClose: () => setConnected(false),
      },
    );
    return () => {
      handle.close();
      setConnected(false);
    };
  }, [operationId]);

  return { events, connected, error };
}

// ── Variables (env-scoped, metadata only) ────────────────────────

export function useVariables(
  serviceId: string | undefined,
  environmentId?: string,
  opts?: QueryOpts<VariableMeta[]>,
) {
  return useQuery<VariableMeta[]>({
    queryKey: queryKeys.variables(serviceId, environmentId),
    queryFn: async ({ signal }) => {
      const all = await api.listVariables({ serviceId, environmentId }, signal);
      if (!serviceId) return all;
      return all.filter((v) => !v.service_id || v.service_id === serviceId);
    },
    enabled: Boolean(serviceId),
    ...opts,
  });
}

// ── GitHub installations / repos ─────────────────────────────────

export function useGithubInstallations(opts?: QueryOpts<GithubInstallation[]>) {
  return useQuery<GithubInstallation[]>({
    queryKey: queryKeys.githubInstallations(),
    queryFn: ({ signal }) => api.listGithubInstallations(signal),
    ...opts,
  });
}

export function useGithubRepos(
  installationId: string | undefined,
  opts?: QueryOpts<GithubRepo[]>,
) {
  return useQuery<GithubRepo[]>({
    queryKey: queryKeys.githubRepos(installationId ?? ""),
    queryFn: ({ signal }) => api.listGithubRepos(installationId as string, signal),
    enabled: Boolean(installationId),
    ...opts,
  });
}

// Custom domains and service metrics use panel-local adapters because those
// contracts are not shared outside their owning surfaces.
