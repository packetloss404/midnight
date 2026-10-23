import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import { updateEnvironmentMetadata } from "./api";

const input = {
	projectId: "prj_one",
	environmentId: "env_staging",
	name: "Staging",
	slug: "staging",
	tier: "staging" as const,
	expectedVersion: 4,
	expectedIsProtected: true,
	idempotencyKey: "environment-metadata-stable",
};

const response = {
	id: "env_staging",
	project_id: "prj_one",
	name: "Staging",
	slug: "staging",
	tier: "staging",
	is_protected: true,
	resource_version: 5,
	created_at: "2026-07-29T12:00:00Z",
	updated_at: "2026-07-29T13:00:00Z",
};

beforeEach(() => {
	vi.clearAllMocks();
	apiFetch.mockResolvedValue(response);
});

describe("human environment metadata update REST surface", () => {
	it("sends only ordinary metadata with explicit human authority and caller-owned retry identity", async () => {
		await expect(
			updateEnvironmentMetadata({
				...input,
				projectId: " prj_one ",
				environmentId: " env_staging ",
				name: " Staging ",
				slug: "staging ",
			}),
		).resolves.toEqual(response);

		expect(apiFetch).toHaveBeenCalledWith("/environments/env_staging", {
			auth: "human",
			method: "PATCH",
			headers: { "Idempotency-Key": "environment-metadata-stable" },
			body: {
				name: "Staging",
				slug: "staging",
				tier: "staging",
				expected_version: 4,
			},
		});
		const body = apiFetch.mock.calls[0]?.[1]?.body;
		expect(body).not.toHaveProperty("is_protected");
		expect(body).not.toHaveProperty("copy_config");
		expect(body).not.toHaveProperty("project_id");
	});

	it("rejects invalid input before issuing a request", async () => {
		for (const invalid of [
			{ ...input, slug: "Staging" },
			{ ...input, name: " " },
			{ ...input, expectedVersion: 0 },
			{ ...input, idempotencyKey: "bad key" },
		]) {
			await expect(updateEnvironmentMetadata(invalid)).rejects.toThrow();
		}

		expect(apiFetch).not.toHaveBeenCalled();
	});

	it("rejects response scope, protection, metadata, and version mismatches", async () => {
		for (const invalid of [
			{ ...response, id: "env_other" },
			{ ...response, project_id: "prj_other" },
			{ ...response, slug: "other" },
			{ ...response, is_protected: false },
			{ ...response, resource_version: 4 },
			{ ...response, updated_at: "not-a-date" },
		]) {
			apiFetch.mockResolvedValueOnce(invalid);
			await expect(updateEnvironmentMetadata(input)).rejects.toThrow(
				"invalid environment metadata response",
			);
		}
	});
});
