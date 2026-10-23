import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());

vi.mock("./client", () => ({ apiFetch }));

import { deployServiceNow } from "./deployActions";

describe("deployServiceNow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiFetch.mockResolvedValue({ id: "op_1", state: "queued" });
  });

  it("asks the durable deploy intake to derive the intent from service state", async () => {
    const result = await deployServiceNow(
      "svc/1",
      "env_prod",
      "operator",
      "idem-1",
    );

    expect(apiFetch).toHaveBeenCalledWith("/services/svc%2F1/deployments", {
      auth: "operator",
      method: "POST",
      headers: { "Idempotency-Key": "idem-1" },
      body: { environment_id: "env_prod", deploy_now: true },
    });
    expect(result).toEqual({
      id: "op_1",
      status: "queued",
      operationId: "op_1",
    });
  });

  it("refuses to send a deploy request with no environment scope", async () => {
    await expect(deployServiceNow("svc_1", "")).rejects.toThrow(
      /environment is required/i,
    );
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("refuses to send a deploy request with no service", async () => {
    await expect(deployServiceNow("", "env_prod")).rejects.toThrow(
      /service id is required/i,
    );
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
