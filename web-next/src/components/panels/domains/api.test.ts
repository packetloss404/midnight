import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  __resetStagedChangesForTests,
  getStagedForService,
  stageChange,
} from "@/lib/stagedChanges";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import {
  createGeneratedDomain,
  deleteGeneratedDomain,
  fetchGeneratedDomains,
  normalizeDomainState,
  type GeneratedDomainRecord,
} from "./api";

const target = {
  projectId: "prj/one",
  serviceId: "svc/one",
  environmentId: "env/prod",
};

const domain: GeneratedDomainRecord = {
  id: "dom_1",
  workspace_id: "wrk_1",
  project_id: target.projectId,
  service_id: target.serviceId,
  environment_id: target.environmentId,
  kind: "generated",
  hostname: "svc-one.example.test",
  path: "/",
  target_port: 8080,
  public: true,
  tls: false,
  version: 3,
  route_apply_state: "ready",
  route_reachable: true,
  created_at: "2026-07-29T12:00:00Z",
  updated_at: "2026-07-29T12:01:00Z",
};

beforeEach(() => {
  vi.clearAllMocks();
  __resetStagedChangesForTests();
});

describe("tenant generated-domain API", () => {
  it("reads the exact nested service and environment collection with human auth", async () => {
    const signal = new AbortController().signal;
    apiFetch.mockResolvedValueOnce([
      domain,
      {
        ...domain,
        id: "dom_2",
        hostname: "alpha.example.test",
        version: 1,
        route_apply_state: "pending",
        route_reachable: false,
      },
    ]);

    const result = await fetchGeneratedDomains(target, signal);

    expect(apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fone/environments/env%2Fprod/domains",
      { auth: "human", signal },
    );
    expect(result.map((record) => record.hostname)).toEqual([
      "alpha.example.test",
      "svc-one.example.test",
    ]);
  });

  it("stages generated domain intent in the shared tab-local buffer", async () => {
    await createGeneratedDomain(target, "idem-generate-1");

    expect(getStagedForService(target.serviceId, target.environmentId)).toEqual([
      {
        kind: "networking",
        serviceId: target.serviceId,
        environmentId: target.environmentId,
        name: "generated public domain",
        op: "add",
        networking: { kind: "generated", public: true },
      },
    ]);
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("stages removal with the displayed domain version", async () => {
    await deleteGeneratedDomain(
      target,
      "dom/one",
      7,
      "idem-delete-1",
    );

    expect(getStagedForService(target.serviceId, target.environmentId)).toEqual([
      expect.objectContaining({
        kind: "networking",
        op: "remove",
        targetId: "dom/one",
        expectedVersion: 7,
      }),
    ]);
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("coexists with other resource kinds until one Operations save", async () => {
    stageChange({
      kind: "storage",
      serviceId: target.serviceId,
      environmentId: target.environmentId,
      name: "data",
      op: "add",
      storage: { size_bytes: 1024, mount_path: "/data" },
    });

    await createGeneratedDomain(target, "idem-generate-1");

    expect(getStagedForService(target.serviceId, target.environmentId)).toHaveLength(2);
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it.each([
    ["an extra response field", { ...domain, certificate: "claimed" }],
    ["a mismatched project", { ...domain, project_id: "prj_other" }],
    ["an invalid hostname", { ...domain, hostname: "bad..host" }],
    [
      "reachability without ready evidence",
      { ...domain, route_apply_state: "file_applied" },
    ],
    ["caller-controlled TLS", { ...domain, tls: true }],
  ])("rejects %s", async (_label, response) => {
    apiFetch.mockResolvedValueOnce([response]);

    await expect(fetchGeneratedDomains(target)).rejects.toThrow(
      "invalid generated-domain response",
    );
  });

  it("rejects duplicate identities instead of rendering ambiguous versions", async () => {
    apiFetch.mockResolvedValueOnce([domain, { ...domain }]);

    await expect(fetchGeneratedDomains(target)).rejects.toThrow(
      "invalid generated-domain response",
    );
  });

  it("only labels a route reachable when both readiness and probe evidence exist", () => {
    expect(normalizeDomainState(domain)).toMatchObject({
      label: "Trusted ingress ready",
      inFlight: false,
    });
    expect(
      normalizeDomainState({
        ...domain,
        route_reachable: false,
      }),
    ).toMatchObject({
      label: "Checking routing",
      inFlight: true,
    });
  });

  it("describes projected controller failures as environment route-group state", () => {
    expect(
      normalizeDomainState({
        ...domain,
        route_apply_state: "blocked_backend",
        route_reachable: false,
      }),
    ).toMatchObject({
      label: "Route group waiting",
      description: expect.stringContaining("sibling route"),
    });
  });
});
