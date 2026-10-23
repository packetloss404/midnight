import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type RecoveryDomainRecord = {
  id: string;
  project_id: string;
  environment_id: string;
  service_id: string;
  kind: "generated" | "custom";
  hostname: string;
  tls_desired: boolean;
  tls_state: "disabled" | "unavailable" | "ready";
  apply_state:
    | "pending"
    | "private"
    | "blocked_tls"
    | "blocked_backend"
    | "file_applied"
    | "ready"
    | "failed";
  reachable: boolean;
  error_code?: string;
  error_detail?: string;
  dns_challenge?: {
    record_name: string;
    record_value: string;
    state: "pending" | "verified" | "failed";
    last_error?: string;
  };
  certificate?: {
    issuer: string;
    state: "pending" | "issuing" | "ready" | "failed";
    last_error?: string;
  };
};

export type RecoveryServiceDomains = {
  customDomains: RecoveryDomainRecord[];
  generatedDomains: RecoveryDomainRecord[];
};

export const recoveryDomainKeys = {
  all: ["recovery", "domains", "operator"] as const,
  list: (projectId: string, serviceId: string, environmentId?: string) =>
    [
      "recovery",
      "domains",
      "operator",
      projectId,
      serviceId,
      environmentId ?? null,
    ] as const,
};

export type RecoveryDomainStateView = {
  pillStatus: string;
  label: string;
  description: string;
  serving: boolean;
  inFlight: boolean;
};

export function normalizeRecoveryDomainState(
  record: RecoveryDomainRecord,
): RecoveryDomainStateView {
  if (record.apply_state === "ready" && record.reachable) {
    return {
      pillStatus: "active",
      label: "Trusted ingress ready",
      description:
        "Traefik acknowledged this route and the trusted Host-header ingress probe succeeded. Public DNS and browser reachability remain separate.",
      serving: true,
      inFlight: false,
    };
  }
  if (record.apply_state === "private") {
    return {
      pillStatus: "private",
      label: "Private",
      description: "Domain intent exists, but public routing is disabled.",
      serving: false,
      inFlight: false,
    };
  }
  if (record.apply_state === "failed") {
    return {
      pillStatus: "error",
      label: "Route group failed",
      description:
        record.error_detail ||
        "The environment route group could not reconcile. A sibling route may be responsible.",
      serving: false,
      inFlight: false,
    };
  }
  if (record.apply_state === "blocked_tls") {
    return {
      pillStatus: "pending",
      label: "Route group TLS pending",
      description:
        "Routing for this environment is waiting on a certificate or on proof of domain ownership. Another domain may be the cause.",
      serving: false,
      inFlight: true,
    };
  }
  if (record.apply_state === "blocked_backend") {
    return {
      pillStatus: "pending",
      label: "Route group waiting",
      description:
        "The environment route group does not yet have its complete ready backend set. A sibling route may be responsible.",
      serving: false,
      inFlight: true,
    };
  }
  if (record.apply_state === "file_applied") {
    return {
      pillStatus: "verifying",
      label: "Verifying route group",
      description:
        record.error_detail ||
        "Routing is written. Midnight is waiting for Traefik to confirm it and for a successful health check.",
      serving: false,
      inFlight: true,
    };
  }
  return {
    pillStatus: "pending",
    label: "Pending",
    description: "Saved. Waiting for Midnight to set up routing.",
    serving: false,
    inFlight: true,
  };
}

export function useRecoveryServiceDomains(
  projectId: string,
  serviceId: string,
  environmentId?: string,
): UseQueryResult<RecoveryServiceDomains, Error> {
  return useQuery<RecoveryServiceDomains, Error>({
    queryKey: recoveryDomainKeys.list(projectId, serviceId, environmentId),
    queryFn: ({ signal }) =>
      fetchRecoveryDomains(projectId, serviceId, environmentId!, signal),
    enabled: Boolean(projectId && serviceId && environmentId),
    refetchInterval: (query) => {
      const records = [
        ...(query.state.data?.customDomains ?? []),
        ...(query.state.data?.generatedDomains ?? []),
      ];
      return records.some(
        (record) => normalizeRecoveryDomainState(record).inFlight,
      )
        ? 8000
        : false;
    },
  });
}

export type CreateRecoveryDomainInput = {
  projectId: string;
  environmentId: string;
  serviceId: string;
  kind: "generated" | "custom";
  hostname?: string;
  tls?: boolean;
};

function recoveryMutationKey(operation: "create" | "delete") {
  return `web-recovery-domain-${operation}-${crypto.randomUUID()}`;
}

export async function fetchRecoveryDomains(
  projectId: string,
  serviceId: string,
  environmentId: string,
  signal?: AbortSignal,
): Promise<RecoveryServiceDomains> {
  const query = new URLSearchParams({
    project_id: projectId,
    environment_id: environmentId,
    service_id: serviceId,
  });
  const records = await apiFetch<RecoveryDomainRecord[]>(`/domains?${query}`, {
    auth: "operator",
    signal,
  });
  return {
    customDomains: records.filter((record) => record.kind === "custom"),
    generatedDomains: records.filter((record) => record.kind === "generated"),
  };
}

export function createRecoveryDomain(
  input: CreateRecoveryDomainInput,
  idempotencyKey = recoveryMutationKey("create"),
): Promise<RecoveryDomainRecord> {
  return apiFetch<RecoveryDomainRecord>("/domains", {
    auth: "operator",
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: {
      project_id: input.projectId,
      environment_id: input.environmentId,
      service_id: input.serviceId,
      kind: input.kind,
      hostname: input.hostname,
      path_prefix: "/",
      target_port: 8080,
      public: true,
      tls: input.tls ?? false,
    },
  });
}

export function deleteRecoveryDomain(
  domainId: string,
  idempotencyKey = recoveryMutationKey("delete"),
): Promise<void> {
  return apiFetch<void>(`/domains/${encodeURIComponent(domainId)}`, {
    auth: "operator",
    method: "DELETE",
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export function useCreateRecoveryDomain() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateRecoveryDomainInput) =>
      createRecoveryDomain(input),
    onSuccess: (_data, input) =>
      client.invalidateQueries({
        queryKey: recoveryDomainKeys.list(
          input.projectId,
          input.serviceId,
          input.environmentId,
        ),
      }),
  });
}

export function useDeleteRecoveryDomain(
  projectId: string,
  serviceId: string,
  environmentId: string,
) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (domainId: string) => deleteRecoveryDomain(domainId),
    onSuccess: () =>
      client.invalidateQueries({
        queryKey: recoveryDomainKeys.list(
          projectId,
          serviceId,
          environmentId,
        ),
      }),
  });
}
