import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  authStatus: "human",
  accessEnabled: [] as boolean[],
  listScopes: [] as Array<{
    workspaceId: string;
    projectId?: string;
  }>,
  access: {
    data: {
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: "prj_1",
      workspace_role: undefined as string | undefined,
      project_role: "editor" as string | undefined,
      environment_restricted: false,
      permissions: ["project.members.manage"],
    },
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  list: {
    data: { items: [] as Array<Record<string, unknown>> },
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
    hasNextPage: false,
    isFetchingNextPage: false,
    fetchNextPage: vi.fn(),
  },
  create: {
    mutateAsync: vi.fn(),
    reset: vi.fn(),
    isPending: false,
    error: null as Error | null,
  },
  reissue: {
    mutateAsync: vi.fn(),
    reset: vi.fn(),
    isPending: false,
    error: null as Error | null,
  },
  revoke: {
    mutateAsync: vi.fn(),
    reset: vi.fn(),
    isPending: false,
    error: null as Error | null,
  },
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    state:
      state.authStatus === "human"
        ? {
            status: "human",
            me: {
              principal: {
                kind: "user",
                user_id: "usr_1",
                session_id: "ses_1",
                auth_method: "password",
                email: "owner@example.com",
                display_name: "Owner",
              },
            },
          }
        : { status: state.authStatus },
  }),
}));

vi.mock("@/components/panels/automation-tokens/api", () => ({
  useProjectAccess: (
    _projectId: string,
    _environmentId: string | undefined,
    enabled: boolean,
  ) => {
    state.accessEnabled.push(enabled);
    return state.access;
  },
}));

vi.mock("./api", () => ({
  useIdentityInvitations: (
    scope: { workspaceId: string; projectId?: string },
  ) => {
    state.listScopes.push(scope);
    return state.list;
  },
  useCreateIdentityInvitation: () => state.create,
  useReissueIdentityInvitation: () => state.reissue,
  useRevokeIdentityInvitation: () => state.revoke,
}));

import { ApiError } from "@/api/client";
import { ProjectInvitations } from "./ProjectInvitations";

const activeInvitation = {
  id: "inv_active",
  email: "teammate@example.com",
  workspace_id: "wrk_1",
  project_id: "prj_1",
  project_role: "editor",
  token_prefix: "midn_inv_abcd",
  invited_by_user_id: "usr_1",
  created_at: "2026-07-20T12:00:00Z",
  updated_at: "2026-07-20T12:00:00Z",
  expires_at: "2030-08-20T12:00:00Z",
  resource_version: 1,
};

beforeEach(() => {
  vi.clearAllMocks();
  state.authStatus = "human";
  state.accessEnabled = [];
  state.listScopes = [];
  state.access.data = {
    principal_kind: "human",
    workspace_id: "wrk_1",
    project_id: "prj_1",
    workspace_role: undefined,
    project_role: "editor",
    environment_restricted: false,
    permissions: ["project.members.manage"],
  };
  state.access.isPending = false;
  state.access.error = null;
  state.list.data = { items: [] };
  state.list.isPending = false;
  state.list.error = null;
  state.list.hasNextPage = false;
  state.list.isFetchingNextPage = false;
  state.create.isPending = false;
  state.create.error = null;
  state.reissue.isPending = false;
  state.reissue.error = null;
  state.revoke.isPending = false;
  state.revoke.error = null;
  localStorage.clear();
  sessionStorage.clear();
});

describe("ProjectInvitations", () => {
  it("does not expose or query invitation metadata outside a human session", () => {
    state.authStatus = "operator";

    const { container } = render(<ProjectInvitations projectId="prj_1" />);

    expect(container).toBeEmptyDOMElement();
    expect(state.accessEnabled).toEqual([false]);
    expect(state.listScopes).toEqual([]);
  });

  it("does not mount exact-scope queries without membership-manage access", () => {
    state.access.data.permissions = ["project.read"];

    render(<ProjectInvitations projectId="prj_1" />);

    expect(
      screen.getByText("Project membership manager access required"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /New invitation/ })).toBeNull();
    expect(state.listScopes).toEqual([]);
  });

  it("reports an unavailable access decision and retries without controls", async () => {
    const user = userEvent.setup();
    state.access.error = new ApiError(503, {
      error: { code: "unavailable", message: "Unavailable" },
    });

    render(<ProjectInvitations projectId="prj_1" />);

    expect(
      screen.getByText("Invitation management unavailable"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(state.access.refetch).toHaveBeenCalledTimes(1);
    expect(state.listScopes).toEqual([]);
  });

  it("queries project and workspace invitations as separate exact scopes", async () => {
    const user = userEvent.setup();
    state.access.data.workspace_role = "admin";
    state.access.data.project_role = "owner";
    state.access.data.permissions = [
      "project.members.manage",
      "workspace.members.manage",
    ];

    render(<ProjectInvitations projectId="prj_1" />);

    expect(state.listScopes).toContainEqual({
      workspaceId: "wrk_1",
      projectId: "prj_1",
    });
    await user.click(screen.getByRole("button", { name: "Workspace" }));
    expect(state.listScopes).toContainEqual({ workspaceId: "wrk_1" });
    expect(
      screen.getByText(/Workspace access can apply across projects/),
    ).toBeInTheDocument();
  });

  it("loads another signed-cursor page without changing the selected scope", async () => {
    const user = userEvent.setup();
    state.list.data = { items: [activeInvitation] };
    state.list.hasNextPage = true;

    render(<ProjectInvitations projectId="prj_1" />);

    await user.click(
      screen.getByRole("button", { name: "Load more invitations" }),
    );
    expect(state.list.fetchNextPage).toHaveBeenCalledTimes(1);
    expect(state.listScopes).toContainEqual({
      workspaceId: "wrk_1",
      projectId: "prj_1",
    });
  });

  it("does not let a project editor grant the owner role", async () => {
    const user = userEvent.setup();
    render(<ProjectInvitations projectId="prj_1" />);

    await user.click(screen.getByRole("button", { name: "+ New invitation" }));
    const role = screen.getByRole("combobox", { name: /Project role/ });
    expect(within(role).queryByRole("option", { name: "Owner" })).toBeNull();
    expect(within(role).getByRole("option", { name: "Editor" })).toBeInTheDocument();
    expect(within(role).getByRole("option", { name: "Viewer" })).toBeInTheDocument();
  });

  it("reveals a created credential once with manual delivery instructions and no storage", async () => {
    const user = userEvent.setup();
    const clipboardWrite = vi.spyOn(navigator.clipboard, "writeText");
    const rawCredential = "midn_inv_once_only_secret";
    state.create.mutateAsync.mockResolvedValueOnce({
      invitation_token: rawCredential,
      invitation: activeInvitation,
    });

    render(<ProjectInvitations projectId="prj_1" />);

    await user.click(screen.getByRole("button", { name: "+ New invitation" }));
    await user.type(
      screen.getByLabelText("Recipient email"),
      "Teammate@Example.com",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: /Project role/ }),
      "viewer",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: /Lifetime/ }),
      "1",
    );
    await user.click(
      screen.getByRole("button", { name: "Create invitation" }),
    );

    await waitFor(() =>
      expect(state.create.mutateAsync).toHaveBeenCalledTimes(1),
    );
    expect(state.create.mutateAsync.mock.calls[0]?.[0]).toMatchObject({
      email: "teammate@example.com",
      role: "viewer",
    });
    expect(
      screen.getByText("Copy it now. Midnight will never show it again."),
    ).toBeInTheDocument();
    expect(screen.getByText(/does not send invitation email/)).toBeInTheDocument();
    expect(screen.getByText(/Do not put the credential in a URL or query string/)).toBeInTheDocument();
    expect(screen.getByDisplayValue(rawCredential)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "/invite" })).toHaveAttribute("href", "/invite");
    expect(screen.getAllByRole("link").some(link => link.getAttribute("href")?.includes(rawCredential))).toBe(false);

    await user.click(screen.getByRole("button", { name: "Copy credential" }));
    expect(clipboardWrite).toHaveBeenCalledWith(rawCredential);
    expect(storageValues(localStorage)).not.toContain(rawCredential);
    expect(storageValues(sessionStorage)).not.toContain(rawCredential);

    await user.click(
      screen.getByRole("button", { name: "I have saved the credential" }),
    );
    expect(document.body).not.toHaveTextContent(rawCredential);
  });

  it("reissues as an atomic replacement and reveals only the replacement", async () => {
    const user = userEvent.setup();
    state.list.data = { items: [activeInvitation] };
    state.reissue.mutateAsync.mockResolvedValueOnce({
      invitation_token: "midn_inv_replacement_once",
      invitation: {
        ...activeInvitation,
        id: "inv_replacement",
        replaces_invitation_id: "inv_active",
      },
    });

    render(<ProjectInvitations projectId="prj_1" />);

    await user.click(screen.getByRole("button", { name: "Reissue" }));
    expect(
      screen.getByText(/atomic replacement, not a second active credential/),
    ).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Reissue credential" }),
    );

    await waitFor(() =>
      expect(state.reissue.mutateAsync).toHaveBeenCalledTimes(1),
    );
    expect(state.reissue.mutateAsync.mock.calls[0]?.[0]).toMatchObject({
      invitationId: "inv_active",
    });
    expect(
      screen.getByDisplayValue("midn_inv_replacement_once"),
    ).toBeInTheDocument();
  });

  it("requires confirmation before revoking a pending invitation", async () => {
    const user = userEvent.setup();
    state.list.data = { items: [activeInvitation] };
    state.revoke.mutateAsync.mockResolvedValueOnce(undefined);

    render(<ProjectInvitations projectId="prj_1" />);

    await user.click(screen.getByRole("button", { name: "Revoke" }));
    const dialog = screen.getByRole("dialog", {
      name: "Revoke invitation?",
    });
    expect(
      within(dialog).getByText(/Existing membership is not changed/),
    ).toBeInTheDocument();
    expect(state.revoke.mutateAsync).not.toHaveBeenCalled();

    await user.click(
      within(dialog).getByRole("button", { name: "Revoke invitation" }),
    );
    await waitFor(() =>
      expect(state.revoke.mutateAsync).toHaveBeenCalledWith({ invitationId: "inv_active", expectedVersion: 1 }),
    );
  });
});

function storageValues(storage: Storage): string[] {
  return Array.from({ length: storage.length }, (_, index) => {
    const key = storage.key(index);
    return key ? storage.getItem(key) ?? "" : "";
  });
}
