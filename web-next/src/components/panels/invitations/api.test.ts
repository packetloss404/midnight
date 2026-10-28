import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));

import {
  acceptIdentityInvitation,
  createIdentityInvitation,
  fetchIdentityInvitations,
  reissueIdentityInvitation,
  revokeIdentityInvitation,
  onboardIdentityInvitation,
} from "./api";

const projectInvitation = {
  id: "inv_project",
  email: "teammate@example.com",
  workspace_id: "wrk_one",
  project_id: "prj_one",
  project_role: "editor",
  token_prefix: "midn_inv_abcd",
  invited_by_user_id: "usr_1",
  created_at: "2026-07-28T12:00:00Z",
  updated_at: "2026-07-28T12:00:00Z",
  expires_at: "2026-08-04T12:00:00Z",
  resource_version: 1,
} as const;

beforeEach(() => {
  vi.clearAllMocks();
});

describe("identity invitation API", () => {
  it("uses anonymous POST body for enrollment and returns no bearer credential", async () => {
    const result = { user_id: "usr_new", email: projectInvitation.email, invitation: { ...projectInvitation, accepted_at: "2026-10-28T12:00:00Z", accepted_by_user_id: "usr_new" } };
    apiFetch.mockResolvedValueOnce(result);
    const input = { invitation_token: "midn_inv_private", display_name: "Invited person", password: "private password" };
    await expect(onboardIdentityInvitation(input)).resolves.toEqual(result);
    expect(apiFetch).toHaveBeenCalledWith("/invitations/onboard", { auth: "none", method: "POST", body: input });
  });

  it("refuses an enrollment response for an unaccepted or different account", async () => {
    apiFetch.mockResolvedValueOnce({ user_id: "usr_other", email: projectInvitation.email, invitation: projectInvitation });
    await expect(onboardIdentityInvitation({ invitation_token: "private", display_name: "Invitee", password: "private password" })).rejects.toThrow("invalid invitation enrollment");
  });
  it("lists one encoded exact scope with human auth", async () => {
    const signal = new AbortController().signal;
    apiFetch.mockResolvedValueOnce({ items: [projectInvitation] });

    await fetchIdentityInvitations(
      { workspaceId: "wrk_one", projectId: "prj_one" },
      signal,
    );

    expect(apiFetch).toHaveBeenCalledWith(
      "/invitations?workspace_id=wrk_one&project_id=prj_one&limit=50",
      { auth: "human", signal },
    );
  });

  it("preserves exact scope and encodes the signed cursor on later pages", async () => {
    apiFetch.mockResolvedValueOnce({ items: [], next_cursor: undefined });

    await fetchIdentityInvitations(
      { workspaceId: "wrk_one", projectId: "prj_one" },
      undefined,
      { limit: 25, cursor: "signed/cursor+page=2" },
    );

    expect(apiFetch).toHaveBeenCalledWith(
      "/invitations?workspace_id=wrk_one&project_id=prj_one&limit=25&cursor=signed%2Fcursor%2Bpage%3D2",
      { auth: "human", signal: undefined },
    );
  });

  it("fails closed on cross-scope metadata instead of rendering it", async () => {
    apiFetch.mockResolvedValueOnce({
      items: [{ ...projectInvitation, project_id: "prj_other" }],
    });

    await expect(
      fetchIdentityInvitations({
        workspaceId: "wrk_one",
        projectId: "prj_one",
      }),
    ).rejects.toThrow("invalid invitation metadata page");
  });

  it("creates project and workspace invitations with exactly one role field", async () => {
    apiFetch
      .mockResolvedValueOnce({
        invitation_token: "midn_inv_project_once",
        invitation: projectInvitation,
      })
      .mockResolvedValueOnce({
        invitation_token: "midn_inv_workspace_once",
        invitation: {
          ...projectInvitation,
          id: "inv_workspace",
          project_id: undefined,
          project_role: undefined,
          workspace_role: "member",
        },
      });

    await createIdentityInvitation(
      { workspaceId: "wrk_one", projectId: "prj_one" },
      {
        email: "teammate@example.com",
        role: "editor",
        expiresAt: "2026-08-04T12:00:00.000Z",
      },
      "idem-project",
    );
    await createIdentityInvitation(
      { workspaceId: "wrk_one" },
      {
        email: "teammate@example.com",
        role: "member",
        expiresAt: "2026-08-04T12:00:00.000Z",
      },
      "idem-workspace",
    );

    expect(apiFetch).toHaveBeenNthCalledWith(1, "/invitations", {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": "idem-project" },
      body: {
        email: "teammate@example.com",
        workspace_id: "wrk_one",
        project_id: "prj_one",
        project_role: "editor",
        expires_at: "2026-08-04T12:00:00.000Z",
      },
    });
    expect(apiFetch).toHaveBeenNthCalledWith(2, "/invitations", {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": "idem-workspace" },
      body: {
        email: "teammate@example.com",
        workspace_id: "wrk_one",
        workspace_role: "member",
        expires_at: "2026-08-04T12:00:00.000Z",
      },
    });
  });

  it("rejects a role that does not belong to the exact scope", async () => {
    await expect(
      createIdentityInvitation(
        { workspaceId: "wrk_1", projectId: "prj_1" },
        {
          email: "teammate@example.com",
          role: "deployer",
          expiresAt: "2026-08-04T12:00:00.000Z",
        },
        "idem-invalid-role",
      ),
    ).rejects.toThrow("valid for this invitation scope");
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("reissues and revokes using the original opaque id plus exact scope", async () => {
    apiFetch
      .mockResolvedValueOnce({
        invitation_token: "midn_inv_reissue_once",
        invitation: {
          ...projectInvitation,
          id: "inv_replacement",
          replaces_invitation_id: "inv/original",
        },
      })
      .mockResolvedValueOnce(undefined);
    const scope = { workspaceId: "wrk_one", projectId: "prj_one" };

    await reissueIdentityInvitation(
      scope,
      {
        invitationId: "inv/original",
        expiresAt: "2026-08-04T12:00:00.000Z",
      },
      "idem-reissue",
    );
    await revokeIdentityInvitation(
      scope,
      "inv_/replacement",
      3,
      "idem-revoke",
    );

    expect(apiFetch).toHaveBeenNthCalledWith(
      1,
      "/invitations/inv%2Foriginal/reissue",
      {
        auth: "human",
        method: "POST",
        headers: { "Idempotency-Key": "idem-reissue" },
        body: {
          workspace_id: "wrk_one",
          project_id: "prj_one",
          expires_at: "2026-08-04T12:00:00.000Z",
        },
      },
    );
    expect(apiFetch).toHaveBeenNthCalledWith(
      2,
      "/invitations/inv_%2Freplacement?workspace_id=wrk_one&project_id=prj_one",
      {
        auth: "human",
        method: "DELETE",
        headers: { "Idempotency-Key": "idem-revoke", "If-Match": '"3"' },
      },
    );
  });

  it("accepts only the manual credential as the current human", async () => {
    apiFetch.mockResolvedValueOnce({
      ...projectInvitation,
      accepted_at: "2026-07-28T13:00:00Z",
      accepted_by_user_id: "usr_2",
    });

    const accepted = await acceptIdentityInvitation(
      "midn_inv_manual_secret",
      "idem-accept",
    );

    expect(apiFetch).toHaveBeenCalledWith("/invitations/accept", {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": "idem-accept" },
      body: { invitation_token: "midn_inv_manual_secret" },
    });
    expect(accepted.accepted_at).toBe("2026-07-28T13:00:00Z");
  });

  it("rejects malformed credential results instead of claiming success", async () => {
    apiFetch.mockResolvedValueOnce({
      invitation_token: "not-an-invitation",
      invitation: projectInvitation,
    });

    await expect(
      createIdentityInvitation(
        { workspaceId: "wrk_one", projectId: "prj_one" },
        {
          email: "teammate@example.com",
          role: "editor",
          expiresAt: "2026-08-04T12:00:00.000Z",
        },
        "idem-malformed",
      ),
    ).rejects.toThrow("invalid invitation credential response");
  });
});
