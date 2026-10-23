import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	__resetStagedChangesForTests,
	getStagedForService,
} from "@/lib/stagedChanges";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import {
	fetchServiceConfig,
	putServiceConfig,
	type PutServiceEnvironmentConfig,
	type ServiceEnvironmentConfig,
} from "./api";

const response: ServiceEnvironmentConfig = {
	project_id: "prj/one",
	service_id: "svc/web",
	environment_id: "env prod",
	version: 2,
	explicit: true,
	root_directory: "apps/web",
	builder: "railpack",
	dockerfile_path: "Dockerfile",
	build_command: "npm run build",
	start_command: "node server.js",
	predeploy_command: "",
	target_port: 3000,
	health_kind: "http",
	health_path: "/health",
	health_port: 3000,
	health_timeout_seconds: 120,
	health_interval_seconds: 2,
	restart_policy: "unless-stopped",
	restart_retries: 0,
	replicas: 1,
	overlap_enabled: true,
	drain_grace_seconds: 20,
	runtime_kind: "service",
	cron_schedule: "",
	cron_timeout_seconds: 600,
	cron_max_attempts: 1,
	cron_retry_delay_seconds: 60,
};

const putConfig: PutServiceEnvironmentConfig = {
	expected_version: 2,
	root_directory: response.root_directory,
	builder: response.builder,
	dockerfile_path: response.dockerfile_path,
	build_command: response.build_command,
	start_command: response.start_command,
	predeploy_command: response.predeploy_command,
	target_port: response.target_port,
	health_kind: response.health_kind,
	health_path: response.health_path,
	health_port: response.health_port,
	health_timeout_seconds: response.health_timeout_seconds,
	health_interval_seconds: response.health_interval_seconds,
	restart_policy: response.restart_policy,
	restart_retries: response.restart_retries,
	replicas: response.replicas,
	overlap_enabled: response.overlap_enabled,
	drain_grace_seconds: response.drain_grace_seconds,
	runtime_kind: response.runtime_kind,
	cron_schedule: response.cron_schedule,
	cron_timeout_seconds: response.cron_timeout_seconds,
	cron_max_attempts: response.cron_max_attempts,
	cron_retry_delay_seconds: response.cron_retry_delay_seconds,
};

beforeEach(() => {
	vi.clearAllMocks();
	__resetStagedChangesForTests();
	apiFetch.mockResolvedValue(response);
});

describe("service config REST surface", () => {
	it("gets the exact scope with only the selected human credential", async () => {
		const controller = new AbortController();

		await fetchServiceConfig({
			projectId: "prj/one",
			serviceId: "svc/web",
			environmentId: "env prod",
			authority: "human",
			signal: controller.signal,
		});

		expect(apiFetch).toHaveBeenCalledWith(
			"/services/svc%2Fweb/environments/env%20prod/config",
			{ auth: "human", signal: controller.signal },
		);
	});

	it("stages a complete runtime CAS document for human review", async () => {
		const staged = await putServiceConfig({
			projectId: "prj/one",
			serviceId: "svc/web",
			environmentId: "env prod",
			authority: "human",
			config: putConfig,
			idempotencyKey: "settings-save-v2",
		});

		expect(staged).toMatchObject({
			project_id: "prj/one",
			service_id: "svc/web",
			environment_id: "env prod",
			staged: true,
		});
		expect(getStagedForService("svc/web", "env prod")).toEqual([
			expect.objectContaining({
				kind: "runtime",
				op: "edit",
				targetId: "svc/web",
				expectedVersion: 2,
				runtime: expect.objectContaining({ target_port: 3000 }),
			}),
		]);
		expect(apiFetch).not.toHaveBeenCalled();
	});

	it("keeps recovery access explicit instead of using a credential fallback", async () => {
		await fetchServiceConfig({
			projectId: "prj/one",
			serviceId: "svc/web",
			environmentId: "env prod",
			authority: "operator",
		});

		expect(apiFetch).toHaveBeenCalledWith(
			"/services/svc%2Fweb/environments/env%20prod/config",
			{ auth: "operator", signal: undefined },
		);
	});

	it("rejects incomplete, cross-scope, and incoherent version responses", async () => {
		const scope = {
			projectId: "prj/one",
			serviceId: "svc/web",
			environmentId: "env prod",
			authority: "human" as const,
		};
		apiFetch.mockResolvedValueOnce({
			...response,
			project_id: "prj_other",
		});
		await expect(fetchServiceConfig(scope)).rejects.toThrow(
			"invalid service configuration response",
		);

		apiFetch.mockResolvedValueOnce({
			...response,
			build_command: undefined,
		});
		await expect(fetchServiceConfig(scope)).rejects.toThrow(
			"invalid service configuration response",
		);

		apiFetch.mockResolvedValueOnce({
			...response,
			explicit: false,
		});
		await expect(fetchServiceConfig(scope)).rejects.toThrow(
			"invalid service configuration response",
		);
	});

	it("restages the same runtime slot instead of stacking duplicate edits", async () => {
		await putServiceConfig({
			projectId: "prj/one",
			serviceId: "svc/web",
			environmentId: "env prod",
			authority: "human",
			config: putConfig,
			idempotencyKey: "settings-save-v2",
		});
		await putServiceConfig({
				projectId: "prj/one",
				serviceId: "svc/web",
				environmentId: "env prod",
				authority: "human",
				config: putConfig,
				idempotencyKey: "settings-save-v2",
			});
		expect(getStagedForService("svc/web", "env prod")).toHaveLength(1);
		expect(apiFetch).not.toHaveBeenCalled();
	});

	it("keeps operator recovery as a direct exact-version PUT", async () => {
		apiFetch.mockResolvedValueOnce({ ...response, version: 3 });

		await putServiceConfig({
			projectId: "prj/one",
			serviceId: "svc/web",
			environmentId: "env prod",
			authority: "operator",
			config: putConfig,
			idempotencyKey: "settings-save-v2",
		});

		expect(apiFetch).toHaveBeenCalledWith(
			"/services/svc%2Fweb/environments/env%20prod/config",
			{
				auth: "operator",
				method: "PUT",
				headers: { "Idempotency-Key": "settings-save-v2" },
				body: putConfig,
			},
		);
	});

	it("refuses an invalid idempotency key before sending a mutation", async () => {
		await expect(
			putServiceConfig({
				projectId: "prj/one",
				serviceId: "svc/web",
				environmentId: "env prod",
				authority: "human",
				config: putConfig,
				idempotencyKey: "bad key",
			}),
		).rejects.toThrow("Something went wrong preparing that request");
		expect(apiFetch).not.toHaveBeenCalled();
	});
});
