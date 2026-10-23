import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  removeProjectMember,
  updateProjectMember,
  type ProjectMember,
} from "./api";

const member: ProjectMember = {
  project_id: "prj_1",
  user: {
    id: "usr_2",
    email: "reader@example.test",
    display_name: "Reader",
    instance_admin: false,
    mfa_enabled: true,
  },
  role: "viewer",
  invited_at: "2026-08-01T00:00:00Z",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
  resource_version: 7,
};

function response(body: unknown, init: ResponseInit = {}) {
  return new Response(body === undefined ? undefined : JSON.stringify(body), {
    status: 200,
    headers: { "content-type": "application/json", ...(init.headers ?? {}) },
    ...init,
  });
}

describe("membership conditional writes", () => {
  beforeEach(() => {
    sessionStorage.setItem("midnight.identity.session", "midn_sess_test");
    vi.restoreAllMocks();
  });

  it("uses the authoritative member GET ETag for role updates", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(response(member, { headers: { ETag: '"opaque-server-tag"' } }))
      .mockResolvedValueOnce(response({ ...member, role: "editor", resource_version: 8 }));

    await updateProjectMember("prj_1", member, "editor");

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const first = fetchMock.mock.calls[0];
    expect(first[0]).toBe("/api/v1alpha/projects/prj_1/members/usr_2");
    expect((first[1] as RequestInit).method).toBeUndefined();
    const second = fetchMock.mock.calls[1];
    expect((second[1] as RequestInit).method).toBe("PUT");
    expect((second[1] as RequestInit).headers).toMatchObject({
      "If-Match": '"opaque-server-tag"',
    });
  });

  it("rejects a stale list snapshot before issuing a mutation", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      response({ ...member, resource_version: 8 }, { headers: { ETag: '"8"' } }),
    );

    await expect(updateProjectMember("prj_1", member, "editor")).rejects.toThrow(
      /changed since this list loaded/i,
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("uses the authoritative ETag for removal", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(response(member, { headers: { ETag: '"server-delete-tag"' } }))
      .mockResolvedValueOnce(new Response(undefined, { status: 204 }));

    await removeProjectMember("prj_1", member);

    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toMatchObject({
      "If-Match": '"server-delete-tag"',
    });
  });

  it("fails closed when the member GET omits a strong ETag", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(response(member));

    await expect(updateProjectMember("prj_1", member, "editor")).rejects.toThrow(
      /without a strong ETag/i,
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
