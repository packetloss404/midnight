import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  authStatus: "human",
  accessEnabled: [] as boolean[],
  listEnabled: [] as boolean[],
  access: {
    data: {
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: "prj_1",
      environment_restricted: false,
      permissions: ["project.manage"],
    } as
      | {
          principal_kind: string;
          workspace_id: string;
          project_id: string;
          environment_restricted: boolean;
          permissions: string[];
        }
      | undefined,
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  list: {
    data: { items: [] as Array<Record<string, unknown>> },
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  create: {
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

vi.mock("./api", () => ({
  useProjectAccess: (
    _projectId: string,
    _environmentId: string | undefined,
    enabled: boolean,
  ) => {
    state.accessEnabled.push(enabled);
    return state.access;
  },
  useProjectAutomationTokens: (_projectId: string, enabled: boolean) => {
    state.listEnabled.push(enabled);
    return state.list;
  },
  useCreateProjectAutomationToken: () => state.create,
  useRevokeProjectAutomationToken: () => state.revoke,
}));

import { ApiError } from "@/api/client";
import { ProjectAutomationTokens } from "./ProjectAutomationTokens";

const activeToken = {
  id: "sat_active",
  service_account_id: "svcacct_1",
  workspace_id: "wrk_1",
  project_id: "prj_1",
  name: "Production deploy",
  token_prefix: "midn_sa_abcd",
  profile: "ci-deploy-v1",
  created_at: "2026-07-20T12:00:00Z",
  updated_at: "2026-07-20T12:00:00Z",
  expires_at: "2030-08-20T12:00:00Z",
  resource_version: 1,
};

beforeEach(() => {
  vi.clearAllMocks();
  state.authStatus = "human";
  state.accessEnabled = [];
  state.listEnabled = [];
  state.access.data = {
    principal_kind: "human",
    workspace_id: "wrk_1",
    project_id: "prj_1",
    environment_restricted: false,
    permissions: ["project.manage"],
  };
  state.access.isPending = false;
  state.access.error = null;
  state.list.data = { items: [] };
  state.list.isPending = false;
  state.list.error = null;
  state.create.isPending = false;
  state.create.error = null;
  state.revoke.isPending = false;
  state.revoke.error = null;
  localStorage.clear();
  sessionStorage.clear();
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
  });
});

describe("ProjectAutomationTokens", () => {
  it("does not expose or query the surface outside a human session", () => {
    state.authStatus = "operator";

    const { container } = render(
      <ProjectAutomationTokens projectId="prj_1" />,
    );

    expect(container).toBeEmptyDOMElement();
    expect(state.accessEnabled).toEqual([false]);
    expect(state.listEnabled).toEqual([]);
  });

  it("does not mount token queries or controls without project.manage", () => {
    state.access.data = {
      ...state.access.data!,
      permissions: ["project.read"],
    };

    render(<ProjectAutomationTokens projectId="prj_1" />);

    expect(
      screen.getByText("Project manager access required"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /new token/i })).toBeNull();
    expect(state.listEnabled).toEqual([]);
  });

  it("reports an unavailable permission check honestly and can retry it", async () => {
    const user = userEvent.setup();
    state.access.data = undefined;
    state.access.error = new ApiError(503, {
      error: { code: "unavailable", message: "Temporarily unavailable" },
    });

    render(<ProjectAutomationTokens projectId="prj_1" />);

    expect(
      screen.getByText("Automation tokens unavailable"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/No token controls are available/),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(state.access.refetch).toHaveBeenCalledTimes(1);
    expect(state.listEnabled).toEqual([]);
  });

  it("renders metadata and keeps revoked credentials non-actionable", () => {
    state.list.data = {
      items: [
        activeToken,
        {
          ...activeToken,
          id: "sat_revoked",
          name: "Old workflow",
          token_prefix: "midn_sa_old",
          revoked_at: "2026-07-27T12:00:00Z",
        },
      ],
    };

    render(<ProjectAutomationTokens projectId="prj_1" />);

    expect(screen.getByText("Production deploy")).toBeInTheDocument();
    expect(screen.getByText("midn_sa_abcd")).toBeInTheDocument();
    expect(screen.getByText("Old workflow")).toBeInTheDocument();
    expect(screen.getByText("revoked")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Revoke" })).toHaveLength(1);
    expect(document.body).not.toHaveTextContent("midn_sa_once_only_secret");
  });

  it("reveals a created secret once, copies it, and never persists it", async () => {
    const user = userEvent.setup();
    const clipboardWrite = vi.spyOn(navigator.clipboard, "writeText");
    const rawToken = "midn_sa_once_only_secret";
    state.create.mutateAsync.mockResolvedValueOnce({
      access_token: rawToken,
      token: activeToken,
    });

    render(<ProjectAutomationTokens projectId="prj_1" />);

    await user.click(
      screen.getByRole("button", { name: "Create automation token" }),
    );
    await user.type(screen.getByLabelText("Token name"), "CI production");
    await user.selectOptions(
      screen.getByRole("combobox", { name: /Lifetime/ }),
      "7",
    );
    await user.click(screen.getByRole("button", { name: "Create token" }));

    await waitFor(() =>
      expect(state.create.mutateAsync).toHaveBeenCalledTimes(1),
    );
    const request = state.create.mutateAsync.mock.calls[0]?.[0] as {
      name: string;
      expiresAt: string;
    };
    expect(request.name).toBe("CI production");
    expect(new Date(request.expiresAt).getTime()).toBeGreaterThan(Date.now());
    expect(
      screen.getByText("Copy it now. Midnight will never show it again."),
    ).toBeInTheDocument();
    expect(screen.getByDisplayValue(rawToken)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Copy token" }));
    expect(clipboardWrite).toHaveBeenCalledWith(rawToken);
    expect(screen.getByText("Copied to clipboard.")).toBeInTheDocument();
    expect(storageValues(localStorage)).not.toContain(rawToken);
    expect(storageValues(sessionStorage)).not.toContain(rawToken);

    await user.click(
      screen.getByRole("button", { name: "I have saved this token" }),
    );
    expect(screen.queryByDisplayValue(rawToken)).toBeNull();
    expect(document.body).not.toHaveTextContent(rawToken);

    await user.click(
      screen.getByRole("button", { name: "+ New token" }),
    );
    expect(screen.queryByDisplayValue(rawToken)).toBeNull();
  });

  it("converts an absolute local expiry to the API date-time contract", async () => {
    const user = userEvent.setup();
    state.create.mutateAsync.mockResolvedValueOnce({
      access_token: "midn_sa_absolute_once",
      token: activeToken,
    });

    render(<ProjectAutomationTokens projectId="prj_1" />);

    await user.click(
      screen.getByRole("button", { name: "Create automation token" }),
    );
    await user.type(screen.getByLabelText("Token name"), "Release job");
    await user.click(screen.getByLabelText("Date and time"));
    const expiry = screen.getByLabelText(/Expires at/);
    fireEvent.change(expiry, { target: { value: "2031-01-02T03:04" } });
    await user.click(screen.getByRole("button", { name: "Create token" }));

    await waitFor(() =>
      expect(state.create.mutateAsync).toHaveBeenCalledTimes(1),
    );
    expect(state.create.mutateAsync).toHaveBeenCalledWith({
      name: "Release job",
      expiresAt: new Date("2031-01-02T03:04").toISOString(),
    });
  });

  it("requires confirmation before revoking an active token", async () => {
    const user = userEvent.setup();
    state.list.data = { items: [activeToken] };
    state.revoke.mutateAsync.mockResolvedValueOnce(undefined);

    render(<ProjectAutomationTokens projectId="prj_1" />);

    await user.click(screen.getByRole("button", { name: "Revoke" }));
    const dialog = screen.getByRole("dialog", {
      name: "Revoke automation token?",
    });
    expect(
      within(dialog).getByText(/stop authenticating immediately/),
    ).toBeInTheDocument();
    expect(state.revoke.mutateAsync).not.toHaveBeenCalled();

    await user.click(
      within(dialog).getByRole("button", { name: "Revoke token" }),
    );
    await waitFor(() =>
      expect(state.revoke.mutateAsync).toHaveBeenCalledWith({ tokenId: "sat_active", expectedVersion: 1 }),
    );
    expect(
      screen.queryByRole("dialog", { name: "Revoke automation token?" }),
    ).toBeNull();
  });
});

function storageValues(storage: Storage): string[] {
  return Array.from({ length: storage.length }, (_, index) => {
    const key = storage.key(index);
    return key ? storage.getItem(key) ?? "" : "";
  });
}
