import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import {
  createProjectAutomationToken,
  fetchProjectAccess,
  fetchProjectAutomationTokens,
  projectAutomationTokenKeys,
  revokeProjectAutomationToken,
} from "./api";

const token = {
  id: "sat_1",
  service_account_id: "svcacct_1",
  workspace_id: "wrk_1",
  project_id: "prj/one",
  name: "Production deploy",
  token_prefix: "midn_sa_abcd",
  profile: "ci-deploy-v1",
  created_at: "2026-07-28T12:00:00Z",
  updated_at: "2026-07-28T12:00:00Z",
  expires_at: "2026-08-27T12:00:00Z",
  resource_version: 1,
} as const;

beforeEach(() => {
  vi.clearAllMocks();
});

describe("project automation-token API", () => {
  it("uses only the human session for access and metadata reads", async () => {
    const signal = new AbortController().signal;
    apiFetch
      .mockResolvedValueOnce({
        principal_kind: "human",
        workspace_id: "wrk_1",
        project_id: "prj/one",
        environment_id: "env/prod",
        environment_restricted: false,
        permissions: ["project.manage"],
      })
      .mockResolvedValueOnce({ items: [token] });

    await fetchProjectAccess("prj/one", "env/prod", signal);
    await fetchProjectAutomationTokens("prj/one", signal);

    expect(apiFetch).toHaveBeenNthCalledWith(
      1,
      "/projects/prj%2Fone/access?environment_id=env%2Fprod",
      { auth: "human", signal },
    );
    expect(apiFetch).toHaveBeenNthCalledWith(
      2,
      "/projects/prj%2Fone/automation-tokens",
      { auth: "human", signal },
    );
    expect(projectAutomationTokenKeys.access("prj/one", "env/prod")).toEqual([
      "project-access",
      "prj/one",
      "env/prod",
    ]);
  });

  it("rejects mismatched project access instead of enabling controls", async () => {
    apiFetch.mockResolvedValueOnce({
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: "prj_other",
      environment_restricted: false,
      permissions: ["service.write", "deployment.trigger.cli"],
    });

    await expect(fetchProjectAccess("prj/one", undefined)).rejects.toThrow(
      "invalid project access response",
    );
  });

  it("rejects mismatched or malformed environment scope", async () => {
    apiFetch
      .mockResolvedValueOnce({
        principal_kind: "human",
        workspace_id: "wrk_1",
        project_id: "prj/one",
        environment_id: "env_other",
        environment_restricted: true,
        permissions: ["service.write", "deployment.trigger.cli"],
      })
      .mockResolvedValueOnce({
        principal_kind: "human",
        workspace_id: "wrk_1",
        project_id: "prj/one",
        environment_restricted: true,
        permissions: ["service.write", "deployment.trigger.cli"],
      });

    await expect(
      fetchProjectAccess("prj/one", "env/prod"),
    ).rejects.toThrow("invalid project access response");
    await expect(
      fetchProjectAccess("prj/one", undefined),
    ).rejects.toThrow("invalid project access response");
  });

  it("creates a bounded-expiry token with an explicit replay key", async () => {
    apiFetch.mockResolvedValueOnce({
      access_token: "midn_sa_once_only_secret",
      token,
    });

    const result = await createProjectAutomationToken(
      "prj/one",
      {
        name: "Production deploy",
        expiresAt: "2026-08-27T12:00:00.000Z",
      },
      "idem-create-1",
    );

    expect(apiFetch).toHaveBeenCalledWith(
      "/projects/prj%2Fone/automation-tokens",
      {
        auth: "human",
        method: "POST",
        headers: { "Idempotency-Key": "idem-create-1" },
        body: {
          name: "Production deploy",
          expires_at: "2026-08-27T12:00:00.000Z",
        },
      },
    );
    expect(result.access_token).toBe("midn_sa_once_only_secret");
  });

  it("rejects malformed secret responses instead of claiming success", async () => {
    apiFetch.mockResolvedValueOnce({
      access_token: "not-a-midnight-service-account-token",
      token,
    });

    await expect(
      createProjectAutomationToken(
        "prj/one",
        {
          name: "Production deploy",
          expiresAt: "2026-08-27T12:00:00.000Z",
        },
        "idem-create-2",
      ),
    ).rejects.toThrow("invalid project automation-token response");
  });

  it("revokes the encoded token resource with human auth and replay safety", async () => {
    apiFetch.mockResolvedValueOnce(undefined);

    await revokeProjectAutomationToken(
      "prj/one",
      "sat/one",
      7,
      "idem-revoke-1",
    );

    expect(apiFetch).toHaveBeenCalledWith(
      "/projects/prj%2Fone/automation-tokens/sat%2Fone",
      {
        auth: "human",
        method: "DELETE",
        headers: { "Idempotency-Key": "idem-revoke-1", "If-Match": '"7"' },
      },
    );
  });
});
