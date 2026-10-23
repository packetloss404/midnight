import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import {
	fetchMonitorDeliveries,
	fetchMonitorIncidents,
	retryMonitorDelivery,
} from "./api";

beforeEach(() => {
	vi.clearAllMocks();
	apiFetch.mockResolvedValue([]);
});

describe("monitor incident and delivery metadata API", () => {
	it("queries incidents and deliveries with an exact project/environment/service scope", async () => {
		const controller = new AbortController();
		await fetchMonitorIncidents(
			"prj 1",
			"env/1",
			"svc_1",
			controller.signal,
		);
		await fetchMonitorDeliveries(
			"prj 1",
			"env/1",
			"svc_1",
			controller.signal,
		);

		expect(apiFetch).toHaveBeenNthCalledWith(
			1,
			"/monitor-incidents?project_id=prj+1&limit=50&environment_id=env%2F1&service_id=svc_1",
			{ signal: controller.signal },
		);
		expect(apiFetch).toHaveBeenNthCalledWith(
			2,
			"/monitor-deliveries?project_id=prj+1&limit=50&environment_id=env%2F1&service_id=svc_1",
			{ signal: controller.signal },
		);
	});

	it("retries by opaque delivery id and project only", async () => {
		await retryMonitorDelivery("prj_1", "mdl/dead", "retry-once");

		expect(apiFetch).toHaveBeenCalledWith(
			"/monitor-deliveries/mdl%2Fdead/retry",
			{
				method: "POST",
				body: { project_id: "prj_1" },
				headers: { "Idempotency-Key": "retry-once" },
			},
		);
		const serialized = JSON.stringify(apiFetch.mock.calls[0]);
		expect(serialized).not.toMatch(/payload|endpoint|signing|secret/i);
	});
});
