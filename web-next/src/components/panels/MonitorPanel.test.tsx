import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const monitorAPI = vi.hoisted(() => ({
	retry: vi.fn(),
	emptyMutation: {
		mutate: vi.fn(),
		isPending: false,
		error: null,
	},
}));

vi.mock("./monitors/api", () => ({
	useMonitors: () => ({ data: [], isPending: false, error: null }),
	useMonitorChannels: () => ({ data: [], isPending: false, error: null }),
	useCreateMonitor: () => monitorAPI.emptyMutation,
	useDeleteMonitor: () => monitorAPI.emptyMutation,
	useCreateMonitorChannel: () => monitorAPI.emptyMutation,
	useSubscribeMonitor: () => monitorAPI.emptyMutation,
	useMonitorIncidents: () => ({
		isPending: false,
		error: null,
		data: [
			{
				id: "minc_1",
				monitor_id: "mon_1",
				monitor_name: "CPU high",
				scope: {
					project_id: "prj_1",
					environment_id: "env_1",
					service_id: "svc_1",
				},
				state: "open",
				opened_at: "2026-07-28T03:00:00Z",
				delivery_count: 1,
				dead_letter_count: 1,
			},
		],
	}),
	useMonitorDeliveries: () => ({
		isPending: false,
		error: null,
		data: [
			{
				id: "mdl_1",
				incident_id: "minc_1",
				monitor_id: "mon_1",
				monitor_name: "CPU high",
				project_id: "prj_1",
				environment_id: "env_1",
				service_id: "svc_1",
				channel_id: "mch_1",
				channel_name: "on-call",
				channel_type: "webhook",
				event_type: "monitor.firing",
				state: "dead",
				attempts: 8,
				manual_retries: 0,
				available_at: "2026-07-28T03:00:00Z",
				response_status: 503,
				error_summary: "remote endpoint returned a non-success status",
				created_at: "2026-07-28T03:00:00Z",
				updated_at: "2026-07-28T03:02:00Z",
			},
		],
	}),
	useRetryMonitorDelivery: () => ({
		mutate: monitorAPI.retry,
		isPending: false,
		error: null,
	}),
}));

import { MonitorPanel } from "./MonitorPanel";

beforeEach(() => {
	vi.clearAllMocks();
});

describe("MonitorPanel incident delivery history", () => {
	it("shows metadata-only dead-letter state and invokes scoped retry hook", async () => {
		const user = userEvent.setup();
		render(
			<MonitorPanel
				projectId="prj_1"
				environmentId="env_1"
				serviceId="svc_1"
			/>,
		);

		expect(screen.getByText("CPU high → on-call")).toBeInTheDocument();
		expect(screen.getByText(/attempt 8\/8/)).toBeInTheDocument();
		expect(
			screen.getByText("HTTP 503", { exact: false }),
		).toBeInTheDocument();
		expect(document.body.textContent).not.toContain(
			"https://hooks.example",
		);
		expect(document.body.textContent).not.toContain("var_signing");

		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect(monitorAPI.retry).toHaveBeenCalledWith("mdl_1");
	});
});
