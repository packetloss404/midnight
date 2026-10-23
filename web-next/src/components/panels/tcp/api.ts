import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type TCPProxyRecord = {
	id: string;
	project_id: string;
	environment_id: string;
	service_id: string;
	hostname: string;
	external_port: number;
	public_address: string;
	backend_port: number;
	allowed_source_cidrs: string[];
	desired_enabled: boolean;
	apply_state: "pending" | "active" | "withdrawn" | "failed";
	listener_bound: boolean;
	backend_reachable: boolean;
	dns_provisioned: boolean;
	public_reachability_verified: boolean;
	error_code?: string;
	error_detail?: string;
	observed_at?: string;
};

export type CreateTCPProxyInput = {
	projectId: string;
	environmentId: string;
	serviceId: string;
	hostname?: string;
	externalPort?: number;
	backendPort: number;
	allowedSourceCIDRs: string[];
};

export const tcpProxyKeys = {
	list: (projectId: string, environmentId: string, serviceId: string) =>
		["panel", "tcp-proxies", projectId, environmentId, serviceId] as const,
};

export function useTCPProxies(
	projectId?: string,
	environmentId?: string,
	serviceId?: string,
) {
	return useQuery<TCPProxyRecord[], Error>({
		queryKey: tcpProxyKeys.list(
			projectId ?? "",
			environmentId ?? "",
			serviceId ?? "",
		),
		queryFn: ({ signal }) => {
			const query = new URLSearchParams({
				project_id: projectId!,
				environment_id: environmentId!,
				service_id: serviceId!,
			});
			return apiFetch<TCPProxyRecord[]>(`/tcp-proxies?${query}`, {
				signal,
			});
		},
		enabled: Boolean(projectId && environmentId && serviceId),
		refetchInterval: (query) =>
			query.state.data?.some(
				(record) =>
					record.apply_state === "pending" ||
					(record.desired_enabled &&
						record.apply_state === "failed"),
			)
				? 5000
				: false,
	});
}

export function useCreateTCPProxy() {
	const client = useQueryClient();
	return useMutation<TCPProxyRecord, Error, CreateTCPProxyInput>({
		mutationFn: (input) =>
			apiFetch<TCPProxyRecord>("/tcp-proxies", {
				method: "POST",
				headers: {
					"Idempotency-Key": `web-tcp-proxy-${crypto.randomUUID()}`,
				},
				body: {
					project_id: input.projectId,
					environment_id: input.environmentId,
					service_id: input.serviceId,
					hostname: input.hostname || undefined,
					external_port: input.externalPort || undefined,
					backend_port: input.backendPort,
					allowed_source_cidrs: input.allowedSourceCIDRs,
				},
			}),
		onSuccess: (_record, input) =>
			client.invalidateQueries({
				queryKey: tcpProxyKeys.list(
					input.projectId,
					input.environmentId,
					input.serviceId,
				),
			}),
	});
}

export function useWithdrawTCPProxy(
	projectId: string,
	environmentId: string,
	serviceId: string,
) {
	const client = useQueryClient();
	return useMutation<TCPProxyRecord, Error, string>({
		mutationFn: (id) =>
			apiFetch<TCPProxyRecord>(
				`/tcp-proxies/${encodeURIComponent(id)}`,
				{
					method: "DELETE",
					headers: {
						"Idempotency-Key": `web-tcp-proxy-withdraw-${crypto.randomUUID()}`,
					},
				},
			),
		onSuccess: () =>
			client.invalidateQueries({
				queryKey: tcpProxyKeys.list(
					projectId,
					environmentId,
					serviceId,
				),
			}),
	});
}
