import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type Monitor = {
	id: string;
	name: string;
	scope: {
		project_id: string;
		environment_id: string;
		service_id: string;
	};
	signal: "cpu_percent" | "memory_percent" | "restart_count";
	operator: "gt" | "gte";
	threshold: number;
	window_seconds: number;
	evaluation_interval_seconds: number;
	consecutive_failures: number;
	consecutive_recoveries: number;
	enabled: boolean;
	state: "unknown" | "healthy" | "firing";
	failure_streak: number;
	recovery_streak: number;
	next_evaluation_at: string;
	last_evaluated_at?: string;
	last_value?: number;
};

export type MonitorChannel = {
	id: string;
	project_id: string;
	display_name: string;
	type: "webhook";
	endpoint_variable_id: string;
	signing_variable_id: string;
	signing_key_id: string;
};

export type CreateMonitorInput = {
	project_id: string;
	environment_id: string;
	service_id: string;
	name: string;
	signal: Monitor["signal"];
	operator: Monitor["operator"];
	threshold: number;
	window_seconds: number;
	evaluation_interval_seconds: number;
	consecutive_failures: number;
	consecutive_recoveries: number;
};

export type CreateMonitorChannelInput = {
	project_id: string;
	display_name: string;
	endpoint_variable_id: string;
	signing_variable_id: string;
	signing_key_id?: string;
};

export type MonitorIncident = {
	id: string;
	monitor_id: string;
	monitor_name: string;
	scope: {
		project_id: string;
		environment_id: string;
		service_id: string;
	};
	state: "open" | "resolved";
	opened_at: string;
	resolved_at?: string;
	open_value?: number;
	close_value?: number;
	delivery_count: number;
	dead_letter_count: number;
};

export type MonitorDeliveryStatus = {
	id: string;
	incident_id: string;
	monitor_id: string;
	monitor_name: string;
	project_id: string;
	environment_id: string;
	service_id: string;
	channel_id: string;
	channel_name: string;
	channel_type: "webhook";
	event_type: "monitor.firing" | "monitor.resolved";
	state: "pending" | "leased" | "delivered" | "dead";
	attempts: number;
	manual_retries: number;
	available_at: string;
	response_status?: number;
	error_summary?: string;
	delivered_at?: string;
	created_at: string;
	updated_at: string;
};

const keys = {
	monitors: (projectId: string) => ["monitors", projectId] as const,
	channels: (projectId: string) => ["monitor-channels", projectId] as const,
	incidents: (
		projectId: string,
		environmentId?: string,
		serviceId?: string,
	) =>
		[
			"monitor-incidents",
			projectId,
			environmentId ?? null,
			serviceId ?? null,
		] as const,
	deliveries: (
		projectId: string,
		environmentId?: string,
		serviceId?: string,
	) =>
		[
			"monitor-deliveries",
			projectId,
			environmentId ?? null,
			serviceId ?? null,
		] as const,
};

function mutationKey(prefix: string): string {
	try {
		return `${prefix}-${crypto.randomUUID()}`;
	} catch {
		return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
	}
}

export function useMonitors(projectId: string) {
	return useQuery<Monitor[], Error>({
		queryKey: keys.monitors(projectId),
		queryFn: ({ signal }) =>
			apiFetch<Monitor[]>(
				`/monitors?project_id=${encodeURIComponent(projectId)}`,
				{ signal },
			),
		enabled: Boolean(projectId),
		refetchInterval: projectId ? 30_000 : false,
	});
}

export function useMonitorChannels(projectId: string) {
	return useQuery<MonitorChannel[], Error>({
		queryKey: keys.channels(projectId),
		queryFn: ({ signal }) =>
			apiFetch<MonitorChannel[]>(
				`/monitor-channels?project_id=${encodeURIComponent(projectId)}`,
				{ signal },
			),
		enabled: Boolean(projectId),
	});
}

function statusQuery(
	projectId: string,
	environmentId?: string,
	serviceId?: string,
): string {
	const query = new URLSearchParams({ project_id: projectId, limit: "50" });
	if (environmentId) query.set("environment_id", environmentId);
	if (serviceId) query.set("service_id", serviceId);
	return query.toString();
}

export function fetchMonitorIncidents(
	projectId: string,
	environmentId?: string,
	serviceId?: string,
	signal?: AbortSignal,
): Promise<MonitorIncident[]> {
	return apiFetch<MonitorIncident[]>(
		`/monitor-incidents?${statusQuery(projectId, environmentId, serviceId)}`,
		{ signal },
	);
}

export function fetchMonitorDeliveries(
	projectId: string,
	environmentId?: string,
	serviceId?: string,
	signal?: AbortSignal,
): Promise<MonitorDeliveryStatus[]> {
	return apiFetch<MonitorDeliveryStatus[]>(
		`/monitor-deliveries?${statusQuery(projectId, environmentId, serviceId)}`,
		{ signal },
	);
}

export function retryMonitorDelivery(
	projectId: string,
	deliveryId: string,
	idempotencyKey = mutationKey("monitor-delivery-retry"),
): Promise<MonitorDeliveryStatus> {
	return apiFetch<MonitorDeliveryStatus>(
		`/monitor-deliveries/${encodeURIComponent(deliveryId)}/retry`,
		{
			method: "POST",
			body: { project_id: projectId },
			headers: { "Idempotency-Key": idempotencyKey },
		},
	);
}

export function useMonitorIncidents(
	projectId: string,
	environmentId?: string,
	serviceId?: string,
) {
	return useQuery<MonitorIncident[], Error>({
		queryKey: keys.incidents(projectId, environmentId, serviceId),
		queryFn: ({ signal }) =>
			fetchMonitorIncidents(projectId, environmentId, serviceId, signal),
		enabled: Boolean(projectId),
		refetchInterval: projectId ? 30_000 : false,
	});
}

export function useMonitorDeliveries(
	projectId: string,
	environmentId?: string,
	serviceId?: string,
) {
	return useQuery<MonitorDeliveryStatus[], Error>({
		queryKey: keys.deliveries(projectId, environmentId, serviceId),
		queryFn: ({ signal }) =>
			fetchMonitorDeliveries(projectId, environmentId, serviceId, signal),
		enabled: Boolean(projectId),
		refetchInterval: projectId ? 15_000 : false,
	});
}

export function useCreateMonitor(projectId: string) {
	const client = useQueryClient();
	return useMutation<Monitor, Error, CreateMonitorInput>({
		mutationFn: (body) =>
			apiFetch<Monitor>("/monitors", {
				method: "POST",
				body,
				headers: { "Idempotency-Key": mutationKey("monitor-create") },
			}),
		onSuccess: () =>
			client.invalidateQueries({ queryKey: keys.monitors(projectId) }),
	});
}

export function useDeleteMonitor(projectId: string) {
	const client = useQueryClient();
	return useMutation<void, Error, string>({
		mutationFn: (monitorId) =>
			apiFetch<void>(
				`/monitors/${encodeURIComponent(monitorId)}?project_id=${encodeURIComponent(projectId)}`,
				{
					method: "DELETE",
					headers: {
						"Idempotency-Key": mutationKey("monitor-delete"),
					},
				},
			),
		onSuccess: () =>
			client.invalidateQueries({ queryKey: keys.monitors(projectId) }),
	});
}

export function useCreateMonitorChannel(projectId: string) {
	const client = useQueryClient();
	return useMutation<MonitorChannel, Error, CreateMonitorChannelInput>({
		mutationFn: (body) =>
			apiFetch<MonitorChannel>("/monitor-channels", {
				method: "POST",
				body,
				headers: {
					"Idempotency-Key": mutationKey("monitor-channel-create"),
				},
			}),
		onSuccess: () =>
			client.invalidateQueries({ queryKey: keys.channels(projectId) }),
	});
}

export function useSubscribeMonitor(projectId: string) {
	return useMutation<
		{ monitor_id: string; channel_id: string },
		Error,
		{ monitorId: string; channelId: string }
	>({
		mutationFn: ({ monitorId, channelId }) =>
			apiFetch(
				`/monitors/${encodeURIComponent(monitorId)}/subscriptions`,
				{
					method: "POST",
					body: { project_id: projectId, channel_id: channelId },
					headers: {
						"Idempotency-Key": mutationKey("monitor-subscribe"),
					},
				},
			),
	});
}

export function useRetryMonitorDelivery(
	projectId: string,
	environmentId?: string,
	serviceId?: string,
) {
	const client = useQueryClient();
	return useMutation<MonitorDeliveryStatus, Error, string>({
		mutationFn: (deliveryId) => retryMonitorDelivery(projectId, deliveryId),
		onSuccess: () => {
			void client.invalidateQueries({
				queryKey: keys.deliveries(projectId, environmentId, serviceId),
			});
			void client.invalidateQueries({
				queryKey: keys.incidents(projectId, environmentId, serviceId),
			});
		},
	});
}
