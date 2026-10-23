import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import { listLatestServiceDeployment } from "./portfolio-api";

describe("portfolio latest-deployment read", () => {
  beforeEach(() => vi.clearAllMocks());

  it("requests one bounded deployment row with product authority", async () => {
    const deployment = {
      id: "dep_1",
      service_id: "svc /one",
      operation_id: "op_1",
      operation_state: "SUCCEEDED",
      status: "active",
      active: true,
    };
    apiFetch.mockResolvedValue({ items: [deployment], next_cursor: "ignored" });
    const controller = new AbortController();

    await expect(
      listLatestServiceDeployment("svc /one", controller.signal),
    ).resolves.toEqual(deployment);
    expect(apiFetch).toHaveBeenCalledWith(
      "/services/svc%20%2Fone/deployments?limit=1",
      { auth: "product", signal: controller.signal },
    );
  });

  it("rejects a server response that exceeds the requested bound", async () => {
    apiFetch.mockResolvedValue({
      items: [
        { id: "dep_1", service_id: "svc_one" },
        { id: "dep_2", service_id: "svc_one" },
      ],
    });
    await expect(listLatestServiceDeployment("svc_one")).rejects.toThrow(
      "invalid latest-deployment page",
    );
  });

  it("rejects legacy over-fetch and cross-service evidence", async () => {
    apiFetch
      .mockResolvedValueOnce([
        { id: "dep_1", service_id: "svc_one" },
        { id: "dep_2", service_id: "svc_one" },
      ])
      .mockResolvedValueOnce([
        { id: "dep_3", service_id: "svc_foreign" },
      ]);

    await expect(listLatestServiceDeployment("svc_one")).rejects.toThrow(
      "invalid latest-deployment page",
    );
    await expect(listLatestServiceDeployment("svc_one")).rejects.toThrow(
      "another service",
    );
  });
});
