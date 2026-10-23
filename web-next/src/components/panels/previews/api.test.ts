import { beforeEach, describe, expect, it, vi } from "vitest";
import { listPreviewEnvironments } from "./api";

const valid = {
  service_id: "svc_one",
  project_id: "prj_one",
  items: [],
  capability: {
    state: "setup_required",
    automatic_lifecycle: false,
    received_event: "pull_request",
    current_disposition: "accepted_but_not_routed",
    requested_actions: [
      "opened:create",
      "reopened:update",
      "synchronize:update",
      "closed:delete",
    ],
    blockers: [
      {
        code: "pull_request_router_missing",
        detail: "Signed deliveries are not routed.",
      },
    ],
  },
};

const enabled = {
  service_id: "svc_one",
  project_id: "prj_one",
  items: [{
    id: "prv_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    pull_request: 42,
    environment_id: "env_preview",
    deployment_id: "dep_preview",
    state: "ready",
    url: "https://svc--preview.apps.example.com",
    head_sha: "a".repeat(40),
    from_fork: true,
    expires_at: "2026-09-06T12:00:00Z",
    updated_at: "2026-08-30T12:00:00Z",
  }],
  capability: {
    state: "enabled",
    automatic_lifecycle: true,
    received_event: "pull_request",
    current_disposition: "routed",
    requested_actions: ["opened:create", "reopened:update", "synchronize:update", "closed:delete"],
    blockers: [],
  },
};

function json(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

describe("preview capability response binding", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("accepts the exact closed capability for the requested service", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json(valid));

    await expect(listPreviewEnvironments("svc_one")).resolves.toEqual(valid);
  });

  it("accepts durable active preview rows when lifecycle is enabled", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json(enabled));

    await expect(listPreviewEnvironments("svc_one")).resolves.toEqual(enabled);
  });

  it.each([
    ["foreign service", { ...valid, service_id: "svc_other" }],
    ["invalid preview", { ...valid, items: [{ id: "preview_fake" }] }],
    [
      "unexpected activation",
      {
        ...valid,
        capability: { ...valid.capability, automatic_lifecycle: true },
      },
    ],
    [
      "incomplete duplicate action set",
      {
        ...valid,
        capability: {
          ...valid.capability,
          requested_actions: [
            "opened:create",
            "opened:create",
            "synchronize:update",
            "closed:delete",
          ],
        },
      },
    ],
  ])("rejects %s evidence instead of presenting it as setup-required", async (_name, payload) => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json(payload));

    await expect(listPreviewEnvironments("svc_one")).rejects.toThrow(
      /preview environments it could not read/i,
    );
  });
});
