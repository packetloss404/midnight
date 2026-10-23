import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import {
  createRecoveryDomain,
  deleteRecoveryDomain,
  fetchRecoveryDomains,
  normalizeRecoveryDomainState,
  type RecoveryDomainRecord,
} from "./recovery-api";

const generated: RecoveryDomainRecord = {
  id: "dom_generated",
  project_id: "prj_1",
  environment_id: "env_prod",
  service_id: "svc_web",
  kind: "generated",
  hostname: "generated.apps.example.test",
  tls_desired: false,
  tls_state: "disabled",
  apply_state: "pending",
  reachable: false,
};

const custom: RecoveryDomainRecord = {
  ...generated,
  id: "dom_custom",
  kind: "custom",
  hostname: "app.example.test",
};

beforeEach(() => {
  vi.clearAllMocks();
});

describe("operator recovery domain API", () => {
  it("uses the flat collection with only operator authority", async () => {
    const signal = new AbortController().signal;
    apiFetch.mockResolvedValueOnce([generated, custom]);

    await expect(
      fetchRecoveryDomains("prj_1", "svc_web", "env_prod", signal),
    ).resolves.toEqual({
      generatedDomains: [generated],
      customDomains: [custom],
    });

    expect(apiFetch).toHaveBeenCalledWith(
      "/domains?project_id=prj_1&environment_id=env_prod&service_id=svc_web",
      { auth: "operator", signal },
    );
  });

  it("creates custom recovery intent without falling back to human auth", async () => {
    apiFetch.mockResolvedValueOnce(custom);

    await createRecoveryDomain(
      {
        projectId: "prj_1",
        serviceId: "svc_web",
        environmentId: "env_prod",
        kind: "custom",
        hostname: "app.example.test",
        tls: true,
      },
      "recovery-create-1",
    );

    expect(apiFetch).toHaveBeenCalledWith("/domains", {
      auth: "operator",
      method: "POST",
      headers: { "Idempotency-Key": "recovery-create-1" },
      body: {
        project_id: "prj_1",
        environment_id: "env_prod",
        service_id: "svc_web",
        kind: "custom",
        hostname: "app.example.test",
        path_prefix: "/",
        target_port: 8080,
        public: true,
        tls: true,
      },
    });
  });

  it("deletes through the flat recovery item route with operator authority", async () => {
    apiFetch.mockResolvedValueOnce(undefined);

    await deleteRecoveryDomain("dom/custom", "recovery-delete-1");

    expect(apiFetch).toHaveBeenCalledWith("/domains/dom%2Fcustom", {
      auth: "operator",
      method: "DELETE",
      headers: { "Idempotency-Key": "recovery-delete-1" },
    });
  });

  it("describes projected blocked states as environment route-group state", () => {
    expect(
      normalizeRecoveryDomainState({
        ...custom,
        apply_state: "blocked_tls",
      }),
    ).toMatchObject({
      label: "Route group TLS pending",
      description: expect.stringContaining("Another domain may be the cause"),
    });
    expect(
      normalizeRecoveryDomainState({
        ...generated,
        apply_state: "blocked_backend",
      }),
    ).toMatchObject({
      label: "Route group waiting",
      description: expect.stringContaining("sibling route"),
    });
  });
});
