import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/client";

const state = vi.hoisted(() => ({
  authStatus: "human",
  operatorFeaturesAvailable: false,
  accessCalls: [] as Array<{
    projectId: string;
    environmentId?: string;
    enabled: boolean;
  }>,
  access: {
    data: {
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: "prj_1",
      environment_restricted: false,
      permissions: ["project.read", "environment.write"],
    } as
      | {
          principal_kind: string;
          workspace_id: string;
          project_id: string;
          workspace_role?: "admin" | "member" | "deployer" | "viewer";
          environment_id?: string;
          environment_restricted: boolean;
          permissions: string[];
        }
      | undefined,
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  environmentAccess: {
    data: {
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: "prj_1",
      environment_id: "env_prod",
      environment_restricted: false,
      permissions: ["project.read", "environment.write"],
    } as
      | {
          principal_kind: string;
          workspace_id: string;
          project_id: string;
          environment_id?: string;
          environment_restricted: boolean;
          permissions: string[];
        }
      | undefined,
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  environments: {
    data: [
      {
        id: "env_prod",
        project_id: "prj_1",
        slug: "production",
        name: "Production",
        tier: "production",
        is_protected: true,
        resource_version: 4,
        created_at: "2026-07-29T12:00:00Z",
        updated_at: "2026-07-29T12:00:00Z",
      },
    ],
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  createEnvironment: vi.fn(),
  updateEnvironmentMetadata: vi.fn(),
  createEnvironmentMetadataIntentKey: vi.fn(),
  invalidateQueries: vi.fn(),
  setQueryData: vi.fn(),
  communityForks: {
    data: [],
    isPending: false,
    error: null,
    refetch: vi.fn(),
  },
  updateCommunityTemplateFork: vi.fn(),
  navigate: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => state.navigate,
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({
    invalidateQueries: state.invalidateQueries,
    setQueryData: state.setQueryData,
  }),
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    state:
      state.authStatus === "human"
        ? { status: "human", me: { principal: { user_id: "usr_1" } } }
        : { status: state.authStatus },
    operatorFeaturesAvailable: state.operatorFeaturesAvailable,
  }),
}));

vi.mock("@/api/hooks", () => ({
  queryKeys: {
    environments: (projectId?: string) =>
      ["environments", projectId ?? null] as const,
  },
  useProject: () => ({
    data: {
      id: "prj_1",
      slug: "midnight",
      display_name: "Midnight",
    },
  }),
  useEnvironments: () => state.environments,
  useServices: () => ({
    data: [],
    isPending: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

vi.mock("@/components/panels/automation-tokens/api", () => ({
  useProjectAccess: (
    projectId: string,
    environmentId: string | undefined,
    enabled: boolean,
  ) => {
    state.accessCalls.push({ projectId, environmentId, enabled });
    return environmentId === undefined ? state.access : state.environmentAccess;
  },
}));

vi.mock("@/components/panels/create/api", () => ({
  createEnvironment: state.createEnvironment,
  useCommunityTemplateForks: () => state.communityForks,
  updateCommunityTemplateFork: state.updateCommunityTemplateFork,
  newIdempotencyKey: vi
    .fn()
    .mockReturnValueOnce("environment-create-stable")
    .mockReturnValue("environment-create-next"),
}));

vi.mock("@/components/panels/environments/api", () => ({
  ENVIRONMENT_METADATA_TIERS: [
    "production",
    "staging",
    "preview",
    "development",
  ],
  environmentMetadataTier: (value: string) =>
    ["production", "staging", "preview", "development"].includes(value)
      ? value
      : undefined,
  createEnvironmentMetadataIntentKey:
    state.createEnvironmentMetadataIntentKey,
  updateEnvironmentMetadata: state.updateEnvironmentMetadata,
}));

vi.mock(
  "@/components/panels/automation-tokens/ProjectAutomationTokens",
  () => ({ ProjectAutomationTokens: () => null }),
);
vi.mock("@/components/panels/invitations/ProjectInvitations", () => ({
  ProjectInvitations: () => null,
}));
vi.mock("@/components/panels/project-admin/ProjectMembers", () => ({
  ProjectMembers: () => null,
}));
vi.mock("@/components/panels/webhooks/ProjectWebhookDeliveries", () => ({
  ProjectWebhookDeliveries: () => null,
}));

import { ProjectSettings } from "./ProjectSettings";

beforeEach(() => {
  vi.clearAllMocks();
  state.authStatus = "human";
  state.operatorFeaturesAvailable = false;
  state.accessCalls = [];
  state.access.data = {
    principal_kind: "human",
    workspace_id: "wrk_1",
    project_id: "prj_1",
    environment_restricted: false,
    permissions: ["project.read", "environment.write"],
  };
  state.access.isPending = false;
  state.access.error = null;
  state.environmentAccess.data = {
    principal_kind: "human",
    workspace_id: "wrk_1",
    project_id: "prj_1",
    environment_id: "env_prod",
    environment_restricted: false,
    permissions: ["project.read", "environment.write"],
  };
  state.environmentAccess.isPending = false;
  state.environmentAccess.error = null;
  state.environments.data = [
    {
      id: "env_prod",
      project_id: "prj_1",
      slug: "production",
      name: "Production",
      tier: "production",
      is_protected: true,
      resource_version: 4,
      created_at: "2026-07-29T12:00:00Z",
      updated_at: "2026-07-29T12:00:00Z",
    },
  ];
  state.environments.isPending = false;
  state.environments.error = null;
  state.environments.refetch
    .mockReset()
    .mockResolvedValue({ data: state.environments.data });
  state.createEnvironment.mockResolvedValue({
    id: "env_staging",
    project_id: "prj_1",
    slug: "staging",
    name: "Staging",
    tier: "staging",
    is_protected: false,
    resource_version: 1,
    created_at: "2026-07-29T12:00:00Z",
    updated_at: "2026-07-29T12:00:00Z",
  });
  state.updateEnvironmentMetadata
    .mockReset()
    .mockResolvedValue({
      ...state.environments.data[0],
      resource_version: 5,
      updated_at: "2026-07-29T13:00:00Z",
    });
  state.createEnvironmentMetadataIntentKey
    .mockReset()
    .mockReturnValueOnce("environment-metadata-stable")
    .mockReturnValue("environment-metadata-next");
  state.invalidateQueries.mockResolvedValue(undefined);
});

describe("ProjectSettings environment creation authority", () => {
  it("offers GitHub App setup only to an exact workspace administrator", () => {
    state.access.data = {
      ...state.access.data!,
      workspace_role: "admin",
    };
    const { rerender } = render(<ProjectSettings projectId="prj_1" />);
    expect(
      screen.getByRole("button", { name: "Install GitHub App" }),
    ).toBeInTheDocument();

    state.access.data = {
      ...state.access.data,
      workspace_role: "member",
    };
    rerender(<ProjectSettings projectId="prj_1" />);
    expect(
      screen.queryByRole("button", { name: "Install GitHub App" }),
    ).not.toBeInTheDocument();
  });

  it("does not render an inert project deletion placeholder", () => {
    render(<ProjectSettings projectId="prj_1" />);

    expect(
      screen.queryByText("Danger zone"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Delete project/i }),
    ).not.toBeInTheDocument();
  });

  it("does not invent project deletion UI from an unsupported permission bit", () => {
    state.operatorFeaturesAvailable = true;
    state.access.data = {
      ...state.access.data!,
      permissions: [...state.access.data!.permissions, "project.delete"],
    };

    const { rerender } = render(<ProjectSettings projectId="prj_1" />);
    expect(
      screen.queryByRole("button", { name: /Delete project/i }),
    ).not.toBeInTheDocument();

    state.access.data = { ...state.access.data, project_id: "prj_other" };
    rerender(<ProjectSettings projectId="prj_1" />);
    expect(
      screen.queryByRole("button", { name: /Delete project/i }),
    ).not.toBeInTheDocument();
  });

  it("does not request access or expose environment mutations outside a human session", () => {
    state.authStatus = "operator";

    render(<ProjectSettings projectId="prj_1" />);

    expect(state.accessCalls.at(-1)).toEqual({
      projectId: "prj_1",
      environmentId: undefined,
      enabled: false,
    });
    expect(
      screen.queryByRole("button", { name: "+ New environment" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Duplicate" }),
    ).not.toBeInTheDocument();
  });

  it("keeps creation unmounted while exact access is pending", () => {
    state.access.isPending = true;

    render(<ProjectSettings projectId="prj_1" />);

    expect(screen.getByText("Checking environment access…")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "+ New environment" }),
    ).not.toBeInTheDocument();
  });

  it("reports access errors, supports retry, and keeps mutations unavailable", async () => {
    const user = userEvent.setup();
    state.access.data = undefined;
    state.access.error = new Error("permission service unavailable");

    render(<ProjectSettings projectId="prj_1" />);

    expect(
      screen.getByText("Environment changes unavailable"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "+ New environment" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(state.access.refetch).toHaveBeenCalledTimes(1);
  });

  it("requires an exact project-scoped environment.write grant", () => {
    state.access.data = {
      ...state.access.data!,
      environment_id: "env_prod",
      permissions: ["project.read", "environment.write"],
    };
    const view = render(<ProjectSettings projectId="prj_1" />);

    expect(
      screen.queryByRole("button", { name: "+ New environment" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText("Environment changes unavailable"),
    ).toBeInTheDocument();

    state.access.data = {
      ...state.access.data,
      environment_id: undefined,
      permissions: ["project.read"],
    };
    view.rerender(<ProjectSettings projectId="prj_1" />);

    expect(
      screen.queryByRole("button", { name: "+ New environment" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText(/does not grant environment creation/i),
    ).toBeInTheDocument();
  });

  it("retries one unchanged create intent with the same idempotency key", async () => {
    const user = userEvent.setup();
    state.createEnvironment
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValueOnce({
        id: "env_staging",
        project_id: "prj_1",
        slug: "staging",
        name: "Staging",
        tier: "staging",
        is_protected: false,
        resource_version: 1,
        created_at: "2026-07-29T12:00:00Z",
        updated_at: "2026-07-29T12:00:00Z",
      });

    render(<ProjectSettings projectId="prj_1" />);

    await user.click(
      screen.getByRole("button", { name: "+ New environment" }),
    );
    await user.type(screen.getByLabelText("Name"), "Staging");
    await user.click(
      screen.getByRole("button", { name: "Create environment" }),
    );
    expect(await screen.findByText("response lost")).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Create environment" }),
    );

    expect(state.createEnvironment).toHaveBeenCalledTimes(2);
    expect(state.createEnvironment).toHaveBeenNthCalledWith(
      1,
      { projectId: "prj_1", name: "Staging", tier: "STAGING" },
      "environment-create-stable",
    );
    expect(state.createEnvironment).toHaveBeenNthCalledWith(
      2,
      { projectId: "prj_1", name: "Staging", tier: "STAGING" },
      "environment-create-stable",
    );
    expect(state.invalidateQueries).toHaveBeenCalledWith({
      queryKey: ["environments", "prj_1"],
    });
    expect(
      screen.queryByRole("button", { name: "Create environment" }),
    ).not.toBeInTheDocument();
  });
});

describe("ProjectSettings environment metadata editing", () => {
  it("loads exact environment access before exposing a narrow protected-state-immutable editor", async () => {
    const user = userEvent.setup();

    render(<ProjectSettings projectId="prj_1" />);
    await user.click(
      screen.getByRole("button", { name: "Edit Production" }),
    );

    expect(state.accessCalls.at(-1)).toEqual({
      projectId: "prj_1",
      environmentId: "env_prod",
      enabled: true,
    });
    expect(screen.getByLabelText("Name")).toHaveValue("Production");
    expect(screen.getByLabelText("Slug")).toHaveValue("production");
    expect(screen.getByLabelText("Tier")).toHaveValue("production");
    expect(screen.getByLabelText("Slug")).toHaveAccessibleDescription(
      /lowercase letters, numbers, and hyphens/i,
    );
    expect(screen.getByLabelText("Tier")).toHaveAccessibleDescription(
      /organizational metadata only/i,
    );
    expect(screen.getByText(/Protection remains enabled/)).toBeInTheDocument();
    expect(
      screen.getByText(/does not deploy, restart, promote, duplicate, or copy/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByLabelText(/protected/i),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /delete environment/i }),
    ).not.toBeInTheDocument();
  });

  it("keeps the edit form unmounted while exact environment access is pending", async () => {
    const user = userEvent.setup();
    state.environmentAccess.isPending = true;

    render(<ProjectSettings projectId="prj_1" />);
    await user.click(
      screen.getByRole("button", { name: "Edit Production" }),
    );

    expect(
      screen.getByText("Checking exact environment access…"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Save environment metadata" }),
    ).not.toBeInTheDocument();
    expect(state.updateEnvironmentMetadata).not.toHaveBeenCalled();
  });

  it("requires an exact environment.write grant and supports access retry without sending a mutation", async () => {
    const user = userEvent.setup();
    state.environmentAccess.data = {
      ...state.environmentAccess.data!,
      environment_id: "env_other",
    };

    render(<ProjectSettings projectId="prj_1" />);
    await user.click(
      screen.getByRole("button", { name: "Edit Production" }),
    );

    expect(
      screen.getByText("Environment editing unavailable"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(state.environmentAccess.refetch).toHaveBeenCalledTimes(1);
    expect(state.updateEnvironmentMetadata).not.toHaveBeenCalled();
  });

  it("reuses an unchanged canonical retry key and rotates it after metadata changes", async () => {
    const user = userEvent.setup();
    state.updateEnvironmentMetadata
      .mockRejectedValueOnce(new Error("response lost"))
      .mockRejectedValueOnce(new Error("response still lost"))
      .mockResolvedValueOnce({
        ...state.environments.data[0],
        name: "Production renamed",
        slug: "production-v2",
        resource_version: 5,
        updated_at: "2026-07-29T13:00:00Z",
      });

    render(<ProjectSettings projectId="prj_1" />);
    await user.click(
      screen.getByRole("button", { name: "Edit Production" }),
    );
    const name = screen.getByLabelText("Name");
    await user.clear(name);
    await user.type(name, "Production renamed");
    const save = screen.getByRole("button", {
      name: "Save environment metadata",
    });

    await user.click(save);
    expect(await screen.findByText("response lost")).toBeInTheDocument();
    await user.click(save);
    expect(await screen.findByText("response still lost")).toBeInTheDocument();

    expect(state.updateEnvironmentMetadata.mock.calls[0]?.[0]).toMatchObject({
      projectId: "prj_1",
      environmentId: "env_prod",
      expectedVersion: 4,
      expectedIsProtected: true,
      name: "Production renamed",
      slug: "production",
      tier: "production",
      idempotencyKey: "environment-metadata-stable",
    });
    expect(
      state.updateEnvironmentMetadata.mock.calls[1]?.[0].idempotencyKey,
    ).toBe("environment-metadata-stable");

    const slug = screen.getByLabelText("Slug");
    await user.clear(slug);
    await user.type(slug, "production-v2");
    await user.click(save);

    expect(
      state.updateEnvironmentMetadata.mock.calls[2]?.[0].idempotencyKey,
    ).toBe("environment-metadata-next");
    expect(state.invalidateQueries).toHaveBeenCalledWith({
      queryKey: ["environments", "prj_1"],
    });
    expect(
      await screen.findByText(
        "Saved metadata version 5. No deployment started.",
      ),
    ).toBeInTheDocument();
  });

  it("reloads the exact latest environment after a 409 and discards the stale draft", async () => {
    const user = userEvent.setup();
    state.updateEnvironmentMetadata.mockRejectedValueOnce(
      new ApiError(409, {
        error: { code: "RESOURCE_CONFLICT", message: "version conflict" },
      }),
    );
    const latest = {
      ...state.environments.data[0],
      name: "Production latest",
      resource_version: 5,
      updated_at: "2026-07-29T13:00:00Z",
    };
    state.environments.refetch.mockResolvedValueOnce({ data: [latest] });

    render(<ProjectSettings projectId="prj_1" />);
    await user.click(
      screen.getByRole("button", { name: "Edit Production" }),
    );
    const name = screen.getByLabelText("Name");
    await user.clear(name);
    await user.type(name, "Stale draft");
    await user.click(
      screen.getByRole("button", { name: "Save environment metadata" }),
    );

    expect(
      await screen.findByRole("button", {
        name: "Reload latest & discard draft",
      }),
    ).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", {
        name: "Reload latest & discard draft",
      }),
    );

    expect(state.environments.refetch).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText("Name")).toHaveValue("Production latest");
  });

  it("preserves the stale draft when conflict reload fails", async () => {
    const user = userEvent.setup();
    state.updateEnvironmentMetadata.mockRejectedValueOnce(
      new ApiError(409, {
        error: { code: "RESOURCE_CONFLICT", message: "version conflict" },
      }),
    );
    state.environments.refetch.mockResolvedValueOnce({
      data: state.environments.data,
      error: new Error("reload unavailable"),
    });

    render(<ProjectSettings projectId="prj_1" />);
    await user.click(
      screen.getByRole("button", { name: "Edit Production" }),
    );
    const name = screen.getByLabelText("Name");
    await user.clear(name);
    await user.type(name, "Keep this draft");
    await user.click(
      screen.getByRole("button", { name: "Save environment metadata" }),
    );
    await user.click(
      await screen.findByRole("button", {
        name: "Reload latest & discard draft",
      }),
    );

    expect(await screen.findByText("reload unavailable")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("Keep this draft");
  });
});
