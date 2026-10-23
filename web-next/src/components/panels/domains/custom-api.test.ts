import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import {
  createCustomDomain,
  deleteCustomDomain,
  fetchCustomDomains,
  verifyCustomDomain,
  type CustomDomainRecord,
} from "./custom-api";

const target = {
  projectId: "prj/one",
  serviceId: "svc/one",
  environmentId: "env/prod",
};

const domain: CustomDomainRecord = {
  id: "dom_custom",
  workspace_id: "wrk_1",
  project_id: target.projectId,
  service_id: target.serviceId,
  environment_id: target.environmentId,
  kind: "custom",
  hostname: "api.example.test",
  path: "/",
  target_port: 8080,
  public: false,
  tls: false,
  version: 1,
  ownership_state: "pending",
  route_apply_state: "private",
  route_reachable: false,
  routing_dns: {
    record_type: "CNAME",
    record_name: "api.example.test",
    record_value: "apps.midnight.test",
    state: "operator_required",
    note: "Create this CNAME at your DNS provider.",
  },
  dns_challenge: {
    record_name: "_midnight-challenge.api.example.test",
    record_value: "midnight-verification=token",
    state: "pending",
    attempts: 0,
  },
  created_at: "2026-07-29T12:00:00Z",
  updated_at: "2026-07-29T12:00:00Z",
};

beforeEach(() => vi.clearAllMocks());

describe("tenant custom-domain API", () => {
  it("reads the nested collection and validates routing guidance", async () => {
    const signal = new AbortController().signal;
    apiFetch.mockResolvedValueOnce([domain]);

    await expect(fetchCustomDomains(target, signal)).resolves.toEqual([domain]);
    expect(apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fone/environments/env%2Fprod/custom-domains",
      { auth: "human", signal },
    );
  });

  it("creates, verifies, and deletes with exact encoded routes and versions", async () => {
    apiFetch.mockResolvedValueOnce(domain);
    await expect(createCustomDomain(target, "api.example.test", true)).resolves.toEqual(domain);
    expect(apiFetch.mock.calls[0][0]).toBe(
      "/services/svc%2Fone/environments/env%2Fprod/custom-domains",
    );
    expect(apiFetch.mock.calls[0][1].body).toEqual({ hostname: "api.example.test", tls: true });

    apiFetch.mockResolvedValueOnce({ ...domain, ownership_state: "verified", public: true });
    await verifyCustomDomain(target, "dom/custom", 1);
    expect(apiFetch.mock.calls[1][0]).toContain("/custom-domains/dom%2Fcustom/verify");
    expect(apiFetch.mock.calls[1][1].body).toEqual({ expected_version: 1 });

    apiFetch.mockResolvedValueOnce(undefined);
    await deleteCustomDomain(target, "dom/custom", 2);
    expect(apiFetch.mock.calls[2][0]).toContain("/custom-domains/dom%2Fcustom");
    expect(apiFetch.mock.calls[2][1].body).toEqual({ expected_version: 2 });
    expect(apiFetch.mock.calls[2][1].headers).toEqual(
      expect.objectContaining({ "If-Match": '"2"' }),
    );
  });

  it.each([
    ["missing routing guidance", { ...domain, routing_dns: undefined }],
    ["extra response field", { ...domain, certificate: "claimed" }],
    ["invalid TLS type", { ...domain, tls: "true" }],
    ["mismatched service", { ...domain, service_id: "svc_other" }],
    [
      "invalid challenge attempts",
      { ...domain, dns_challenge: { ...domain.dns_challenge, attempts: -1 } },
    ],
    [
      "invalid optional routing target",
      { ...domain, routing_dns: { ...domain.routing_dns, record_value: 42 } },
    ],
  ])("rejects %s", async (_label, response) => {
    apiFetch.mockResolvedValueOnce([response]);
    await expect(fetchCustomDomains(target)).rejects.toThrow(
      "invalid custom-domain response",
    );
  });

  it("rejects invalid mutation input before making a request", async () => {
    await expect(
      createCustomDomain(target, "HTTPS://API.EXAMPLE.TEST", true),
    ).rejects.toThrow("lowercase hostname");
    await expect(verifyCustomDomain(target, "", 0)).rejects.toThrow(
      "positive expected version",
    );
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
