import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  setHumanSessionToken,
  setOperatorRecoveryToken,
} from "@/api/credentials";
import {
  canRedriveProjectDelivery,
  listProjectWebhookDeliveries,
  redriveWebhookDelivery,
  type WebhookDeliveryDetail,
} from "./api";

const detail = (id: string, projectIds: string[]): WebhookDeliveryDetail => ({
  id,
  provider: "github",
  event_type: "push",
  provider_delivery_id: `provider-${id}`,
  state: "dead_letter",
  route_attempt: 2,
  max_route_attempts: 2,
  manual_retries: 0,
  received_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
  exclusive_project: new Set(projectIds).size <= 1,
  targets: projectIds.map((project_id, index) => ({
    id: `${id}-${index}`,
    project_id,
    service_id: `svc_${index}`,
    repository: "midnight/app",
    branch: "main",
    commit_sha: "a".repeat(40),
    state: "dead_letter",
    attempt: 2,
    max_attempts: 2,
    manual_retries: 0,
    updated_at: "2026-08-01T00:00:00Z",
  })),
});

function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "content-type": "application/json" },
  });
}

describe("project webhook delivery scoping", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setHumanSessionToken(undefined);
    setOperatorRecoveryToken(undefined);
  });

  it("requests server-scoped collection and detail projections", async () => {
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(json({ items: [{ id: "wbd_1" }, { id: "wbd_2" }] }))
      .mockResolvedValueOnce(json(detail("wbd_1", ["prj_1"])))
      .mockResolvedValueOnce(json(detail("wbd_2", ["prj_1"])));

    await expect(listProjectWebhookDeliveries("prj_1", "human")).resolves.toEqual([
      detail("wbd_1", ["prj_1"]),
      detail("wbd_2", ["prj_1"]),
    ]);
    expect(globalThis.fetch).toHaveBeenNthCalledWith(
      1,
      "/api/v1alpha/webhook-deliveries?project_id=prj_1&limit=50",
      expect.any(Object),
    );
    expect(globalThis.fetch).toHaveBeenNthCalledWith(
      2,
      "/api/v1alpha/webhook-deliveries/wbd_1?project_id=prj_1",
      expect.any(Object),
    );
  });

  it("fails closed when any detail ownership proof fails", async () => {
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(json({ items: [{ id: "wbd_1" }, { id: "wbd_2" }] }))
      .mockResolvedValueOnce(json(detail("wbd_1", ["prj_1"])))
      .mockResolvedValueOnce(json({ error: { message: "unavailable" } }, 503));

    await expect(listProjectWebhookDeliveries("prj_1", "human")).rejects.toThrow("unavailable");
  });

  it("rejects a server projection containing a foreign target", async () => {
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(json({ items: [{ id: "wbd_1" }] }))
      .mockResolvedValueOnce(json(detail("wbd_1", ["prj_other"])));

    await expect(listProjectWebhookDeliveries("prj_1", "human")).rejects.toThrow(
      "ownership metadata",
    );
  });

  it("allows redrive only when every target is in the current project", () => {
    expect(canRedriveProjectDelivery(detail("wbd_1", ["prj_1", "prj_1"]), "prj_1")).toBe(true);
    expect(canRedriveProjectDelivery(detail("wbd_2", ["prj_1", "prj_2"]), "prj_1")).toBe(false);
    expect(canRedriveProjectDelivery(detail("wbd_3", []), "prj_1")).toBe(false);
  });

  it("sends the redrive contract's required empty body", async () => {
    setHumanSessionToken("midn_user_project_admin");
    setOperatorRecoveryToken("operator-recovery");
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(async () => json(detail("wbd_1", ["prj_1"])));

    await redriveWebhookDelivery("wbd_1", "prj_1", "stable-redrive-key", "human");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1alpha/webhook-deliveries/wbd_1/redrive?project_id=prj_1",
      expect.objectContaining({ method: "POST", body: undefined }),
    );
    expect((fetchMock.mock.calls[0][1] as RequestInit).headers).toMatchObject({
      "Idempotency-Key": "stable-redrive-key",
      Authorization: "Bearer midn_user_project_admin",
    });

    setHumanSessionToken(undefined);
    await redriveWebhookDelivery("wbd_1", "prj_1", "second-redrive-key", "human");
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).not.toHaveProperty(
      "Authorization",
    );
  });
});
