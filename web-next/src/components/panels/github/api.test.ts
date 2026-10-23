import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/client")>();
  return { ...original, apiFetch };
});

import { ApiError } from "@/api/client";
import {
  deleteHumanServiceGithubLink,
  getHumanServiceGithubLink,
  listProjectGithubBranches,
  listProjectGithubRepositories,
  putHumanServiceGithubLink,
} from "./api";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("tenant-scoped GitHub source API", () => {
  it("lists only repositories granted to the exact project", async () => {
    const signal = new AbortController().signal;
    apiFetch.mockResolvedValueOnce({
      items: [
        {
          installation_id: 41,
          repository_id: 82,
          repository_full_name: "acme/web",
          account_login: "acme",
          private: true,
          default_branch: "main",
        },
      ],
    });

    await expect(
      listProjectGithubRepositories("prj/one", signal),
    ).resolves.toEqual([
      {
        installation_id: 41,
        repository_id: 82,
        repository_full_name: "acme/web",
        account_login: "acme",
        private: true,
        default_branch: "main",
      },
    ]);
    expect(apiFetch).toHaveBeenCalledWith(
      "/projects/prj%2Fone/github/repositories",
      { auth: "human", signal },
    );
    expect(apiFetch.mock.calls.flat().join(" ")).not.toContain(
      "/github/installations",
    );
  });

  it("lists branches only through the project and immutable repository id", async () => {
    const signal = new AbortController().signal;
    apiFetch.mockResolvedValueOnce({ items: [{ name: "main" }, "release"] });

    await expect(
      listProjectGithubBranches("prj_one", 82, signal),
    ).resolves.toEqual([{ name: "main" }, { name: "release" }]);
    expect(apiFetch).toHaveBeenCalledWith(
      "/projects/prj_one/github/repositories/82/branches",
      { auth: "human", signal },
    );
  });

  it("gets a service binding with human authority and treats absence honestly", async () => {
    apiFetch
      .mockResolvedValueOnce({
        installation_id: 41,
        repository_id: 82,
        repository_full_name: "acme/web",
        branch: "main",
        binding_state: "bound",
        repository_access_verified_at: "2026-07-29T07:00:00Z",
        updated_at: "2026-07-29T07:01:00Z",
      })
      .mockRejectedValueOnce(
        new ApiError(404, {
          error: { code: "not_found", message: "source not linked" },
        }),
      );

    await expect(getHumanServiceGithubLink("svc one")).resolves.toMatchObject({
      repository_id: 82,
      repository_full_name: "acme/web",
    });
    await expect(getHumanServiceGithubLink("svc one")).resolves.toBeNull();
    expect(apiFetch).toHaveBeenNthCalledWith(
      1,
      "/services/svc%20one/github",
      { auth: "human", signal: undefined },
    );
  });

  it("puts exactly the canonical source identity with a stable key", async () => {
    apiFetch.mockResolvedValueOnce({
      installation_id: 41,
      repository_id: 82,
      repository_full_name: "acme/web",
      branch: "release",
      binding_state: "bound",
    });

    await putHumanServiceGithubLink(
      "svc_one",
      { installationId: 41, repositoryId: 82, branch: "release" },
      "github-link-stable",
    );

    expect(apiFetch).toHaveBeenCalledWith("/services/svc_one/github", {
      auth: "human",
      method: "PUT",
      headers: { "Idempotency-Key": "github-link-stable" },
      body: {
        installation_id: 41,
        repository_id: 82,
        branch: "release",
      },
    });
  });

  it("deletes through the scoped service route with the supplied stable key", async () => {
    apiFetch.mockResolvedValueOnce(undefined);

    await deleteHumanServiceGithubLink("svc_one", "github-unlink-stable");

    expect(apiFetch).toHaveBeenCalledWith("/services/svc_one/github", {
      auth: "human",
      method: "DELETE",
      headers: { "Idempotency-Key": "github-unlink-stable" },
    });
  });

  it("fails closed before any request for invalid ids, branches, or keys", async () => {
    await expect(
      listProjectGithubBranches("prj_one", 0),
    ).rejects.toThrow("valid repository id");
    await expect(
      putHumanServiceGithubLink(
        "svc_one",
        { installationId: 41, repositoryId: 82, branch: " refs/heads/main " },
        "stable",
      ),
    ).rejects.toThrow("Enter a branch name");
    await expect(
      deleteHumanServiceGithubLink("svc_one", " "),
    ).rejects.toThrow("Something went wrong preparing that request");
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
