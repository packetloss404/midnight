import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch, type RequestAuthPolicy } from "@/api/client";
import { stageChange } from "@/lib/stagedChanges";

export type ServiceEnvironmentConfig = {
	project_id: string;
	service_id: string;
	environment_id: string;
	version: number;
	explicit: boolean;
	root_directory: string;
	builder: "auto" | "dockerfile" | "railpack";
	dockerfile_path: string;
	build_command: string;
	start_command: string;
	predeploy_command: string;
	target_port: number;
	health_kind: "none" | "http" | "tcp";
	health_path: string;
	health_port: number;
	health_timeout_seconds: number;
	health_interval_seconds: number;
	restart_policy: "no" | "always" | "unless-stopped" | "on-failure";
	restart_retries: number;
	replicas: number;
	overlap_enabled: boolean;
	drain_grace_seconds: number;
	runtime_kind: "service" | "background" | "cron";
	cron_schedule: string;
	cron_timeout_seconds: number;
	cron_max_attempts: number;
	cron_retry_delay_seconds: number;
	created_at?: string;
	updated_at?: string;
	staged?: boolean;
};

export type PutServiceEnvironmentConfig = Omit<
	ServiceEnvironmentConfig,
	| "project_id"
	| "service_id"
	| "environment_id"
	| "version"
	| "explicit"
	| "created_at"
	| "updated_at"
	| "staged"
> & {
	expected_version: number;
};

export type ServiceConfigAuthority = Extract<
	RequestAuthPolicy,
	"human" | "operator"
>;

export type ServiceConfigScope = {
	projectId: string;
	serviceId: string;
	environmentId: string;
	authority: ServiceConfigAuthority;
};

export type FetchServiceConfigInput = ServiceConfigScope & {
	signal?: AbortSignal;
};

export type PutServiceConfigInput = ServiceConfigScope & {
	config: PutServiceEnvironmentConfig;
	idempotencyKey: string;
};

export const settingsKeys = {
	config: (
		projectId: string,
		serviceId: string,
		environmentId: string,
		authority: ServiceConfigAuthority,
	) =>
		[
			"service-environment-config",
			authority,
			projectId,
			serviceId,
			environmentId,
		] as const,
};

function configPath(serviceId: string, environmentId: string): string {
	return `/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(environmentId)}/config`;
}

export async function fetchServiceConfig(
	input: FetchServiceConfigInput,
): Promise<ServiceEnvironmentConfig> {
	assertScope(input);
	const result = await apiFetch<unknown>(
		configPath(input.serviceId, input.environmentId),
		{ auth: input.authority, signal: input.signal },
	);
	return validateServiceConfigResponse(result, input);
}

export async function putServiceConfig(
	input: PutServiceConfigInput,
): Promise<ServiceEnvironmentConfig> {
	assertScope(input);
	if (
		!input.idempotencyKey ||
		input.idempotencyKey.length > 255 ||
		/\s/.test(input.idempotencyKey)
	) {
		console.error(
			"midnight: refusing service-config write without a valid idempotency key",
		);
		throw new Error("Something went wrong preparing that request. Please try again.", {
			cause: "invalid service-config idempotency key",
		});
	}
	if (input.authority === "human") {
		const { expected_version, ...runtime } = input.config;
		stageChange({
			kind: "runtime",
			serviceId: input.serviceId,
			environmentId: input.environmentId,
			name: "service configuration",
			op: expected_version === 0 ? "add" : "edit",
			targetId: expected_version === 0 ? undefined : input.serviceId,
			expectedVersion: expected_version === 0 ? undefined : expected_version,
			runtime,
		});
		return {
			project_id: input.projectId,
			service_id: input.serviceId,
			environment_id: input.environmentId,
			version: expected_version,
			explicit: expected_version > 0,
			...runtime,
			staged: true,
		};
	}
	const result = await apiFetch<unknown>(
		configPath(input.serviceId, input.environmentId),
		{
			auth: input.authority,
			method: "PUT",
			headers: {
				"Idempotency-Key": input.idempotencyKey,
				"If-Match": `"${input.config.expected_version}"`,
			},
			body: input.config,
		},
	);
	const saved = validateServiceConfigResponse(result, input);
	if (
		!saved.explicit ||
		saved.version !== input.config.expected_version + 1
	) {
		throw new Error(
			"Midnight returned an invalid service configuration response.",
		);
	}
	return saved;
}

export function createServiceConfigIntentKey(): string {
	try {
		return `web-service-config-${crypto.randomUUID()}`;
	} catch {
		return `web-service-config-${Date.now()}-${Math.random().toString(16).slice(2)}`;
	}
}

export function useServiceConfig(
	scope: ServiceConfigScope & { enabled?: boolean },
) {
	return useQuery<ServiceEnvironmentConfig, Error>({
		queryKey: settingsKeys.config(
			scope.projectId,
			scope.serviceId,
			scope.environmentId,
			scope.authority,
		),
		queryFn: ({ signal }) => fetchServiceConfig({ ...scope, signal }),
		enabled:
			(scope.enabled ?? true) &&
			Boolean(scope.projectId && scope.serviceId && scope.environmentId),
	});
}

export function usePutServiceConfig() {
	const queryClient = useQueryClient();
	return useMutation<ServiceEnvironmentConfig, Error, PutServiceConfigInput>({
		mutationFn: putServiceConfig,
		onSuccess: (saved, input) => {
			if (saved.staged) {
				return;
			}
			queryClient.setQueryData(
				settingsKeys.config(
					saved.project_id,
					saved.service_id,
					saved.environment_id,
					input.authority,
				),
				saved,
			);
		},
	});
}

function assertScope(scope: ServiceConfigScope): void {
	if (
		!scope.projectId.trim() ||
		!scope.serviceId.trim() ||
		!scope.environmentId.trim()
	) {
		throw new Error(
			"A valid project, service, and environment scope is required.",
		);
	}
}

function validateServiceConfigResponse(
	value: unknown,
	scope: ServiceConfigScope,
): ServiceEnvironmentConfig {
	if (!isRecord(value)) return invalidResponse();
	const config = value as Partial<ServiceEnvironmentConfig>;
	const versionIsValid =
		isIntegerInRange(config.version, 0) &&
		((config.explicit === false && config.version === 0) ||
			(config.explicit === true && config.version > 0));
	if (
		config.project_id !== scope.projectId ||
		config.service_id !== scope.serviceId ||
		config.environment_id !== scope.environmentId ||
		!versionIsValid ||
		!isOneOf(config.builder, ["auto", "dockerfile", "railpack"]) ||
		!isOneOf(config.health_kind, ["none", "http", "tcp"]) ||
		!isOneOf(config.restart_policy, [
			"no",
			"always",
			"unless-stopped",
			"on-failure",
		]) ||
		!isOneOf(config.runtime_kind, ["service", "background", "cron"]) ||
		!hasStrings(config, [
			"root_directory",
			"dockerfile_path",
			"build_command",
			"start_command",
			"predeploy_command",
			"health_path",
			"cron_schedule",
		]) ||
		!isIntegerInRange(config.target_port, 1, 65_535) ||
		!isIntegerInRange(config.health_port, 0, 65_535) ||
		!isIntegerInRange(config.health_timeout_seconds, 1) ||
		!isIntegerInRange(config.health_interval_seconds, 1) ||
		!isIntegerInRange(config.restart_retries, 0) ||
		!isIntegerInRange(config.replicas, 1, 50) ||
		typeof config.overlap_enabled !== "boolean" ||
		!isIntegerInRange(config.drain_grace_seconds, 1) ||
		!isIntegerInRange(config.cron_timeout_seconds, 1) ||
		!isIntegerInRange(config.cron_max_attempts, 1) ||
		!isIntegerInRange(config.cron_retry_delay_seconds, 1) ||
		!isOptionalString(config.created_at) ||
		!isOptionalString(config.updated_at)
	) {
		return invalidResponse();
	}
	return config as ServiceEnvironmentConfig;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasStrings(
	value: Partial<ServiceEnvironmentConfig>,
	fields: Array<keyof ServiceEnvironmentConfig>,
): boolean {
	return fields.every((field) => typeof value[field] === "string");
}

function isOneOf<T extends string>(
	value: unknown,
	options: readonly T[],
): value is T {
	return typeof value === "string" && options.includes(value as T);
}

function isIntegerInRange(
	value: unknown,
	minimum: number,
	maximum = Number.MAX_SAFE_INTEGER,
): value is number {
	return (
		typeof value === "number" &&
		Number.isSafeInteger(value) &&
		value >= minimum &&
		value <= maximum
	);
}

function isOptionalString(value: unknown): boolean {
	return value === undefined || typeof value === "string";
}

function invalidResponse(): never {
	throw new Error(
		"Midnight returned an invalid service configuration response.",
	);
}
