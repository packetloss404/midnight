import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const settings = vi.hoisted(() => ({
	mutate: vi.fn(),
	reset: vi.fn(),
	refetch: vi.fn(),
	saveError: null as Error | null,
	requestedEnvironments: [] as Array<string | undefined>,
	requestedConfigs: [] as Array<{
		projectId: string;
		serviceId: string;
		environmentId: string;
		authority: "human" | "operator";
	}>,
	intentSequence: 0,
	accessCalls: [] as Array<{
		projectId: string;
		environmentId?: string;
		enabled: boolean;
	}>,
	accessPending: false,
	accessError: null as Error | null,
	accessExactEnvironment: true,
	accessProjectId: "prj_1",
	accessPermissions: ["service.read", "service.write"] as string[],
	deleteMutate: vi.fn(),
	deletePending: false,
	deleteError: null as Error | null,
	deleteReceipt: null as Record<string, unknown> | null,
	config: {
		project_id: "prj_1",
		service_id: "svc_web",
		environment_id: "env_prod",
		version: 4,
		explicit: true,
		root_directory: "",
		builder: "railpack",
		dockerfile_path: "Dockerfile",
		build_command: "npm run build",
		start_command: "npm start",
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
	},
}));

vi.mock("./automation-tokens/api", () => ({
	useProjectAccess: (
		projectId: string,
		environmentId: string | undefined,
		enabled: boolean,
	) => {
		settings.accessCalls.push({ projectId, environmentId, enabled });
		return {
			data: enabled
				? {
						principal_kind: "human",
						workspace_id: "wrk_1",
						project_id: settings.accessProjectId,
						environment_id: settings.accessExactEnvironment
							? environmentId
							: "env_other",
						environment_restricted: false,
						permissions: settings.accessPermissions,
					}
				: undefined,
			isPending: enabled && settings.accessPending,
			error: enabled ? settings.accessError : null,
		};
	},
}));

vi.mock("@/api/hooks", () => ({
	useService: () => ({
		data: {
			id: "svc_web",
			project_id: "prj_1",
			slug: "web",
			display_name: "Web",
			resource_version: 7,
		},
		isPending: false,
		error: null,
		refetch: vi.fn(),
	}),
	useDeleteService: () => ({
		mutate: settings.deleteMutate,
		isPending: settings.deletePending,
		isSuccess: settings.deleteReceipt !== null,
		data: settings.deleteReceipt,
		error: settings.deleteError,
	}),
	useEnvironments: () => ({
		data: [
			{ id: "env_prod", name: "Production" },
			{ id: "env_stage", name: "Staging" },
		],
		isPending: false,
	}),
}));

vi.mock("./settings/api", () => ({
	createServiceConfigIntentKey: () =>
		`settings-intent-${++settings.intentSequence}`,
	useServiceConfig: (scope: {
		projectId: string;
		serviceId: string;
		environmentId: string;
		authority: "human" | "operator";
	}) => {
		settings.requestedEnvironments.push(scope.environmentId);
		settings.requestedConfigs.push(scope);
		return {
			data: {
				...settings.config,
				project_id: scope.projectId,
				service_id: scope.serviceId,
				environment_id: scope.environmentId,
			},
			isPending: false,
			error: null,
			refetch: settings.refetch,
		};
	},
	usePutServiceConfig: () => ({
		mutate: settings.mutate,
		reset: settings.reset,
		isPending: false,
		isSuccess: false,
		data: undefined,
		error: settings.saveError,
	}),
}));

vi.mock("./DomainList", () => ({
	DomainList: () => (
		<div role="alert" data-testid="domains">
			Domain API unavailable
		</div>
	),
}));

vi.mock("./RecoveryDomainList", () => ({
	RecoveryDomainList: () => (
		<div role="alert" data-testid="recovery-domains">
			Recovery domain API unavailable
		</div>
	),
}));

vi.mock("./TCPProxyList", () => ({
	TCPProxyList: () => <div data-testid="tcp-proxies" />,
}));

vi.mock("./CronOccurrenceHistory", () => ({
	CronOccurrenceHistory: ({ enabled }: { enabled: boolean }) => (
		<div data-testid="cron-history" data-enabled={String(enabled)} />
	),
}));

import { ApiError } from "@/api/client";
import { ServiceSettings } from "./ServiceSettings";

beforeEach(() => {
	vi.clearAllMocks();
	settings.saveError = null;
	settings.requestedEnvironments = [];
	settings.requestedConfigs = [];
	settings.intentSequence = 0;
	settings.accessCalls = [];
	settings.accessPending = false;
	settings.accessError = null;
	settings.accessExactEnvironment = true;
	settings.accessProjectId = "prj_1";
	settings.accessPermissions = ["service.read", "service.write"];
	settings.deletePending = false;
	settings.deleteError = null;
	settings.deleteReceipt = null;
	settings.refetch.mockResolvedValue({
		data: { ...settings.config, version: 5 },
	});
});

describe("ServiceSettings durable config editor", () => {
	it("edits and saves an exact-version desired config without claiming a deploy", async () => {
		const user = userEvent.setup();
		render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		expect(screen.getByText("Saved settings v4.")).toBeInTheDocument();
		expect(
			screen.getByText(/Running containers are not changed/),
		).toBeInTheDocument();
		const replicasInput = screen.getByText("Replicas").parentElement?.querySelector("input");
		expect(replicasInput).toHaveAttribute("max", "50");

		const buildCommand = screen.getByLabelText(/Build command/);
		await user.clear(buildCommand);
		await user.type(buildCommand, "pnpm build");
		await user.click(
			screen.getByRole("button", { name: "Save desired config" }),
		);

		expect(settings.mutate).toHaveBeenCalledTimes(1);
		expect(settings.mutate.mock.calls[0]?.[0]).toMatchObject({
			projectId: "prj_1",
			serviceId: "svc_web",
			environmentId: "env_prod",
			authority: "human",
			idempotencyKey: "settings-intent-1",
			config: {
				expected_version: 4,
				build_command: "pnpm build",
				target_port: 3000,
				health_kind: "http",
			},
		});
	});

	it("reuses one idempotency key for an unchanged retry and rotates it after the intent changes", async () => {
		const user = userEvent.setup();
		render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		const save = screen.getByRole("button", {
			name: "Save desired config",
		});
		await user.click(save);
		await user.click(save);
		expect(settings.mutate.mock.calls[0]?.[0].idempotencyKey).toBe(
			"settings-intent-1",
		);
		expect(settings.mutate.mock.calls[1]?.[0].idempotencyKey).toBe(
			"settings-intent-1",
		);

		await user.type(screen.getByLabelText(/Build command/), " --fresh");
		await user.click(save);
		expect(settings.mutate.mock.calls[2]?.[0].idempotencyKey).toBe(
			"settings-intent-2",
		);
	});

	it("waits for exact access admission before sending a config GET", () => {
		settings.accessPending = true;
		render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		expect(
			screen.getByText(/Checking the current project role/),
		).toBeInTheDocument();
		expect(settings.requestedConfigs).toHaveLength(0);
		expect(settings.accessCalls.at(-1)).toEqual({
			projectId: "prj_1",
			environmentId: "env_prod",
			enabled: true,
		});
	});

	it("fails closed on access errors, exact-scope mismatches, and missing service-read", () => {
		settings.accessError = new Error("access unavailable");
		const { rerender } = render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);
		expect(
			screen.getByText(/could not verify service-read permission/),
		).toBeInTheDocument();
		expect(settings.requestedConfigs).toHaveLength(0);

		settings.accessError = null;
		settings.accessExactEnvironment = false;
		rerender(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);
		expect(
			screen.getByText(/could not verify the exact project and environment/),
		).toBeInTheDocument();
		expect(settings.requestedConfigs).toHaveLength(0);

		settings.accessExactEnvironment = true;
		settings.accessPermissions = ["service.write"];
		rerender(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);
		expect(
			screen.getByText(/does not grant service-read permission/),
		).toBeInTheDocument();
		expect(settings.requestedConfigs).toHaveLength(0);
	});

	it("loads for service-read but keeps the editor and save fail closed without service-write", () => {
		settings.accessPermissions = ["service.read"];
		render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		expect(screen.getByText(/Desired configuration is read-only/)).toBeInTheDocument();
		expect(screen.getByLabelText(/Build command/)).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Save desired config" }),
		).toBeDisabled();
		expect(settings.requestedConfigs.at(-1)).toMatchObject({
			projectId: "prj_1",
			serviceId: "svc_web",
			environmentId: "env_prod",
			authority: "human",
		});
		expect(settings.mutate).not.toHaveBeenCalled();
	});

	it("exposes only tenant domains in human networking mode", () => {
		render(
			<ServiceSettings
				authority="human"
				mode="networking"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		expect(
			screen.getByText(/Tenant-managed generated HTTP domains/),
		).toBeInTheDocument();
		expect(screen.getByTestId("domains")).toBeInTheDocument();
		expect(screen.queryByTestId("tcp-proxies")).not.toBeInTheDocument();
		expect(
			screen.getByText(/Tenant-scoped TCP proxy management is not installed/),
		).toBeInTheDocument();
		expect(settings.requestedConfigs).toHaveLength(0);
		expect(settings.accessCalls.at(-1)).toMatchObject({ enabled: false });
	});

	it("edits cron intent and automatically applies the server-required one-off constraints", async () => {
		const user = userEvent.setup();
		render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		await user.selectOptions(screen.getByLabelText(/Runtime kind/), "cron");
		expect(screen.getByLabelText(/Replicas/)).toBeDisabled();
		expect(screen.getByLabelText(/Health check/)).toBeDisabled();
		expect(screen.getByLabelText(/Restart policy/)).toBeDisabled();
		expect(screen.getByTestId("cron-history")).toHaveAttribute(
			"data-enabled",
			"false",
		);
		expect(
			screen.getByText(/cannot view past cron runs/i),
		).toBeInTheDocument();
		await user.type(screen.getByLabelText(/Schedule \(UTC\)/), "*/5 * * * *");
		await user.click(
			screen.getByRole("button", { name: "Save desired config" }),
		);

		expect(settings.mutate.mock.calls[0]?.[0]).toMatchObject({
			serviceId: "svc_web",
			environmentId: "env_prod",
			config: {
				runtime_kind: "cron",
				cron_schedule: "*/5 * * * *",
				replicas: 1,
				restart_policy: "no",
				restart_retries: 0,
				health_kind: "none",
				predeploy_command: "",
			},
		});
	});

	it("keeps TCP networking visible when the domain surface is unavailable", () => {
		render(
			<ServiceSettings
				authority="operator"
				mode="networking"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		expect(
			screen.getByText("Recovery domain API unavailable"),
		).toBeInTheDocument();
		expect(screen.getByTestId("tcp-proxies")).toBeInTheDocument();
	});

	it("offers an explicit latest-state reload after an optimistic version conflict", async () => {
		settings.saveError = new ApiError(409, {
			error: { message: "service config version conflict" },
		});
		const user = userEvent.setup();
		render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);

		expect(
			screen.getByText("The resource changed while you were working."),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", {
				name: "Reload latest & discard draft",
			}),
		);
		expect(settings.refetch).toHaveBeenCalledTimes(1);
		expect(settings.reset).toHaveBeenCalled();
	});

	it("resynchronizes its environment-scoped query when the drawer environment changes", async () => {
		const { rerender } = render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);
		rerender(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_stage"
				projectId="prj_1"
			/>,
		);
		await waitFor(() =>
			expect(settings.requestedEnvironments).toContain("env_stage"),
		);
	});
});

describe("ServiceSettings danger zone", () => {
	function renderDangerZone() {
		return render(
			<ServiceSettings
				authority="operator"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);
	}

	it("names what the delete removes and what it keeps", () => {
		renderDangerZone();
		expect(screen.getByText("Delete this service")).toBeInTheDocument();
		expect(
			screen.getByText("Its environment variables"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Its published domain and HTTP route"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Its volumes (recoverable for 7 days, then erased)",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Past deployments and their logs, as read-only history",
			),
		).toBeInTheDocument();
	});

	it("stays disabled until the exact slug is typed, then deletes with the current version", async () => {
		const user = userEvent.setup();
		renderDangerZone();

		const button = screen.getByRole("button", {
			name: "Delete web permanently",
		});
		expect(button).toBeDisabled();

		const confirm = screen.getByLabelText("Type web to confirm");
		await user.type(confirm, "we");
		expect(button).toBeDisabled();

		await user.type(confirm, "b");
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);

		expect(settings.deleteMutate).toHaveBeenCalledTimes(1);
		expect(settings.deleteMutate.mock.calls[0]?.[0]).toEqual({
			serviceId: "svc_web",
			expectedVersion: 7,
		});
	});

	it("reports a refused delete in the server's own terms", () => {
		settings.deleteError = new ApiError(409, {
			error: { code: "SERVICE_DELETE_RUNNING", message: "ignored" },
		});
		renderDangerZone();
		expect(screen.getByRole("alert")).toHaveTextContent(
			"This service is still running. Stop its deployment from the Deployments tab, then delete it.",
		);
	});

	it("explains a blocking dependent instead of a generic conflict", () => {
		settings.deleteError = new ApiError(409, {
			error: { code: "SERVICE_DELETE_BLOCKED", message: "ignored" },
		});
		renderDangerZone();
		expect(screen.getByRole("alert")).toHaveTextContent(
			/a volume that is still attached/,
		);
	});

	it("refuses to offer a delete the current role cannot perform", () => {
		settings.accessPermissions = ["service.read", "service.write"];
		render(
			<ServiceSettings
				authority="human"
				mode="runtime"
				serviceId="svc_web"
				environmentId="env_prod"
				projectId="prj_1"
			/>,
		);
		expect(
			screen.getByText(
				"Your role in this project cannot delete services. Ask a project owner.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Delete web permanently" }),
		).toBeDisabled();
	});

	it("summarises the receipt after a successful delete", () => {
		settings.deleteReceipt = {
			service_id: "svc_web",
			project_id: "prj_1",
			slug: "web",
			deleted_at: "2026-01-02T03:04:05Z",
			retained_deployments: 4,
			removed: {
				variables: 3,
				volumes: 1,
				published_domains: 1,
				routes: 1,
				tcp_proxies: 0,
				environment_configs: 2,
				source_links: 0,
				pending_changes: 0,
				monitors: 0,
			},
		};
		renderDangerZone();
		expect(screen.getByText("Service deleted")).toBeInTheDocument();
		expect(
			screen.getByText(/3 variables, 1 volume, 1 domain/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/4 past deployments remain readable as history/),
		).toBeInTheDocument();
	});
});
