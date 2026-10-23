import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  operatorFeaturesAvailable: false,
  workspaceError: null as Error | null,
  workspacePending: false,
  navigate: vi.fn(),
  invalidateQueries: vi.fn(),
  useTemplates: vi.fn(),
  createProject: vi.fn(),
  createHumanProject: vi.fn(),
  createHumanService: vi.fn(),
  createService: vi.fn(),
  deployTemplate: vi.fn(),
  discoverCommunityTemplate: vi.fn(),
  createCommunityTemplateFork: vi.fn(),
  updateCommunityTemplateFork: vi.fn(),
  useTemplate: vi.fn(),
  useTemplateProvisionRun: vi.fn(),
  linkServiceGithub: vi.fn(),
  putHumanServiceGithubLink: vi.fn(),
  workspaces: [
    {
      id: "wrk_admin",
      slug: "admin",
      display_name: "Admin Workspace",
      role: "admin",
    },
  ],
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    state: { status: "human", me: { principal: { user_id: "usr_1" } } },
    operatorFeaturesAvailable: state.operatorFeaturesAvailable,
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => state.navigate,
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: state.invalidateQueries }),
}));

vi.mock("@/api/hooks", () => ({
  useIdentityWorkspaces: () => ({
    data: state.workspaces,
    isPending: state.workspacePending,
    error: state.workspaceError,
    refetch: vi.fn(),
  }),
}));

vi.mock("@/components/panels/create/api", () => ({
  createProject: state.createProject,
  createHumanProject: state.createHumanProject,
  createHumanService: state.createHumanService,
  createService: state.createService,
  deployTemplate: state.deployTemplate,
  discoverCommunityTemplate: state.discoverCommunityTemplate,
  createCommunityTemplateFork: state.createCommunityTemplateFork,
  updateCommunityTemplateFork: state.updateCommunityTemplateFork,
  slugify: (value: string) => value.toLowerCase().replace(/\s+/g, "-"),
  useTemplate: state.useTemplate,
  useTemplates: state.useTemplates,
  useTemplateProvisionRun: state.useTemplateProvisionRun,
}));

vi.mock("@/components/panels/github/RepoPicker", () => ({
  RepoPicker: ({ onSelect }: { onSelect: (selection: unknown) => void }) => (
    <button
      type="button"
      onClick={() =>
        onSelect({
          installationId: "42",
          repo: {
            id: 4201,
            name: "widget",
            full_name: "acme/widget",
            private: true,
            default_branch: "main",
          },
        })
      }
    >
      Choose repository
    </button>
  ),
}));

vi.mock("@/components/panels/github/api", () => ({
  linkServiceGithub: state.linkServiceGithub,
  putHumanServiceGithubLink: state.putHumanServiceGithubLink,
}));

import { NewProject } from "./NewProject";

beforeEach(() => {
  vi.clearAllMocks();
  state.operatorFeaturesAvailable = false;
  state.workspaceError = null;
  state.workspacePending = false;
  state.workspaces = [
    {
      id: "wrk_admin",
      slug: "admin",
      display_name: "Admin Workspace",
      role: "admin",
    },
  ];
  state.createHumanProject.mockResolvedValue({ id: "prj_created" });
  state.createHumanService.mockResolvedValue({ id: "svc_created" });
  state.putHumanServiceGithubLink.mockResolvedValue({
    installation_id: 42,
    repository_id: 4201,
    repository_full_name: "acme/widget",
    branch: "main",
  });
  state.useTemplate.mockReturnValue({
    data: undefined,
    isPending: false,
    error: null,
    refetch: vi.fn(),
  });
  state.useTemplateProvisionRun.mockReturnValue({
    data: undefined,
    error: null,
    refetch: vi.fn(),
  });
  state.invalidateQueries.mockResolvedValue(undefined);
});

describe("human new-project flow", () => {
  it("does not mount operator template or GitHub catalog queries", () => {
    render(<NewProject />);

    expect(state.useTemplates).not.toHaveBeenCalled();
    expect(screen.queryByText("GitHub repo")).not.toBeInTheDocument();
    expect(screen.queryByText("operator repository picker")).not.toBeInTheDocument();
    expect(screen.getByText("Empty project")).toBeInTheDocument();
  });

  it("creates an optional starter service with explicit human authority", async () => {
    const user = userEvent.setup();
    render(<NewProject />);

    await user.type(screen.getByLabelText("Project name"), "Tenant App");
    await user.click(
      screen.getByRole("checkbox", { name: "Also create a starter service" }),
    );
    expect(screen.getByLabelText("Service name")).toHaveValue("api");
    await user.clear(screen.getByLabelText("Service name"));
    await user.type(screen.getByLabelText("Service name"), "API");
    await user.click(screen.getByRole("button", { name: "Create project" }));

    expect(state.createHumanProject).toHaveBeenCalledWith(
      { name: "Tenant App", workspaceId: "wrk_admin" },
      expect.stringMatching(/^project-/),
    );
    expect(state.createHumanService).toHaveBeenCalledWith(
      { projectId: "prj_created", name: "API" },
      expect.stringMatching(/^service-/),
    );
    expect(state.createProject).not.toHaveBeenCalled();
    expect(state.createService).not.toHaveBeenCalled();
    expect(state.navigate).toHaveBeenCalledWith({
      to: "/projects/$projectId",
      params: { projectId: "prj_created" },
      search: { service: "svc_created" },
    });
  });

  it("retains the created project and retries only a failed starter service", async () => {
    const user = userEvent.setup();
    state.createHumanService
      .mockRejectedValueOnce(new Error("worker unavailable"))
      .mockResolvedValueOnce({ id: "svc_created" });
    render(<NewProject />);

    await user.type(screen.getByLabelText("Project name"), "Tenant App");
    await user.click(
      screen.getByRole("checkbox", { name: "Also create a starter service" }),
    );
    await user.clear(screen.getByLabelText("Service name"));
    await user.type(screen.getByLabelText("Service name"), "API");
    await user.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText("worker unavailable")).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Retry starter service" }),
    );

    expect(state.createHumanProject).toHaveBeenCalledTimes(1);
    expect(state.createHumanService).toHaveBeenCalledTimes(2);
    expect(state.createHumanService.mock.calls[1]?.[1]).toBe(
      state.createHumanService.mock.calls[0]?.[1],
    );
  });

  it("does not offer project creation to a viewer", () => {
    state.workspaces = [
      {
        id: "wrk_viewer",
        slug: "viewer",
        display_name: "Viewer Workspace",
        role: "viewer",
      },
    ];

    render(<NewProject />);

    expect(screen.getByText("Project creation is unavailable")).toBeInTheDocument();
    expect(screen.queryByLabelText("Project name")).not.toBeInTheDocument();
  });

  it("does not fetch the installation catalog before workspace access is proven", () => {
    state.operatorFeaturesAvailable = true;
    state.workspaces = [
      {
        id: "wrk_viewer",
        slug: "viewer",
        display_name: "Viewer Workspace",
        role: "viewer",
      },
    ];
    state.useTemplates.mockReturnValue({
      data: undefined,
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });

    render(<NewProject />);

    expect(state.useTemplates).toHaveBeenCalledWith({ enabled: false });
    expect(
      screen.getByText(/current workspace role does not grant project creation/i),
    ).toBeInTheDocument();
  });

  it("gives an authorized installation admin the unified source catalog without changing human mutation authority", async () => {
    const user = userEvent.setup();
    state.operatorFeaturesAvailable = true;
    state.useTemplates.mockReturnValue({
      data: [
        {
          slug: "postgres",
          name: "PostgreSQL",
          version: "16",
          summary: "Database",
          tags: ["database"],
          provisionable: true,
        },
        {
          slug: "postgres",
          name: "Postgres",
          version: "16",
          summary: "Database",
          tags: ["database"],
          provisionable: true,
        },
      ],
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });

    render(<NewProject />);

    expect(state.useTemplates).toHaveBeenCalledWith({ enabled: true });
    expect(screen.getByRole("button", { name: /GitHub repo/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Template/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Empty project/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Database/ })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: /Empty project/ }));
    await user.type(screen.getByLabelText("Project name"), "Admin Tenant App");
    await user.click(screen.getByRole("button", { name: "Create project" }));

    expect(state.createHumanProject).toHaveBeenCalledWith(
      { name: "Admin Tenant App", workspaceId: "wrk_admin" },
      expect.stringMatching(/^project-/),
    );
    expect(state.createProject).not.toHaveBeenCalled();
  });

  it("uses the project-authorized human GitHub link contract for an administrator session", async () => {
    const user = userEvent.setup();
    state.operatorFeaturesAvailable = true;
    state.useTemplates.mockReturnValue({
      data: [],
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });

    render(<NewProject />);

    await user.click(screen.getByRole("button", { name: /GitHub repo/ }));
    await user.click(screen.getByRole("button", { name: "Choose repository" }));
    await user.click(screen.getByRole("button", { name: "Create and connect" }));

    expect(state.createHumanProject).toHaveBeenCalledWith(
      { name: "widget", workspaceId: "wrk_admin" },
      expect.stringMatching(/^project-/),
    );
    expect(state.createHumanService).toHaveBeenCalledWith(
      { projectId: "prj_created", name: "widget" },
      expect.stringMatching(/^service-/),
    );
    expect(state.putHumanServiceGithubLink).toHaveBeenCalledWith(
      "svc_created",
      { installationId: 42, repositoryId: 4201, branch: "main" },
      expect.stringMatching(/^service-/),
    );
    expect(state.linkServiceGithub).not.toHaveBeenCalled();
  });

  it("discovers immutable community provenance and creates a local project fork", async () => {
    const user = userEvent.setup();
    state.operatorFeaturesAvailable = true;
    state.useTemplates.mockReturnValue({ data: [], isPending: false, error: null, refetch: vi.fn() });
    const manifest = {
      schema_version: 1,
      template: {
        slug: "queue-worker",
        name: "Queue worker",
        version: "1.0.0",
        summary: "Pinned queue worker",
        services: [{ slug: "worker", image: `ghcr.io/acme/worker@sha256:${"a".repeat(64)}` }],
      },
      metadata: {
        maintainer: "Acme",
        license: "Apache-2.0",
        security_review: "unreviewed",
        risk_class: "moderate",
        update_policy: "Monthly",
        deprecation_policy: "Six months",
        readme_path: "README.md",
        sbom: { path: "sbom.json", sha256: "c".repeat(64) },
      },
    };
    const provenance = {
      source: "https://github.com/acme/queue",
      ref: "d".repeat(40),
      manifest_path: "template.yaml",
      manifest_sha256: "a".repeat(64),
      readme_path: "README.md",
      readme_sha256: "b".repeat(64),
      sbom_path: "sbom.json",
      sbom_sha256: "c".repeat(64),
    };
    const discovery = {
      trust_class: "community",
      manifest,
      provenance,
      readme: "# Queue worker",
      findings: [],
      valid: true,
    };
    state.discoverCommunityTemplate.mockResolvedValue(discovery);
    state.createCommunityTemplateFork.mockResolvedValue({
      id: `tpl_${"1".repeat(32)}`,
      project_id: "prj_created",
      trust_class: "local",
      upstream_manifest: manifest,
      draft: manifest,
      provenance,
      readme: discovery.readme,
      findings: [],
      valid: true,
      resource_version: 1,
      created_at: "2026-08-30T12:00:00Z",
      updated_at: "2026-08-30T12:00:00Z",
    });

    render(<NewProject />);
    await user.click(screen.getByRole("button", { name: /Template/ }));
    await user.type(screen.getByLabelText(/GitHub repository/), provenance.source);
    await user.type(screen.getByLabelText(/Exact commit SHA/), provenance.ref);
    await user.click(screen.getByRole("button", { name: "Inspect immutable source" }));
    expect(await screen.findByText("Validation passed")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create project and fork" }));

    expect(await screen.findByText("local")).toBeInTheDocument();
    expect(screen.getByText(/stage valid desired state from Project settings/i)).toBeInTheDocument();
    expect(state.discoverCommunityTemplate).toHaveBeenCalledWith(
      { source: provenance.source, ref: provenance.ref },
      "human",
    );
    expect(state.createHumanProject).toHaveBeenCalledWith(
      { name: "Queue worker", workspaceId: "wrk_admin" },
      expect.stringMatching(/^project-/),
    );
    expect(state.createCommunityTemplateFork).toHaveBeenCalledWith(
      { projectId: "prj_created", discovery },
      expect.stringMatching(/^service-/),
      "human",
    );
  });

  it("reuses project and provision intent keys when a template response is lost", async () => {
    const user = userEvent.setup();
    state.operatorFeaturesAvailable = true;
    state.useTemplates.mockReturnValue({
      data: [
        {
          slug: "postgres",
          name: "PostgreSQL",
          version: "16",
          summary: "Database",
          tags: ["database"],
          provisionable: true,
        },
      ],
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });
    state.useTemplate.mockReturnValue({
      data: {
        slug: "postgres",
        name: "PostgreSQL",
        version: "16",
        summary: "Database",
        tags: ["database"],
        services: [{ slug: "postgres", image: "postgres@sha256:abc" }],
      },
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });
    state.deployTemplate
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValueOnce({
        operation_id: "op_1",
        provision_run_id: "tpl_1",
        template_slug: "postgres",
        state: "queued",
        environment_id: "env_1",
        deployment_id: "dep_1",
        status_url: "/templates/postgres/instances/tpl_1",
      });

    render(<NewProject />);
    await user.click(screen.getByRole("button", { name: /Database/ }));
    await user.click(screen.getByRole("button", { name: /PostgreSQL/ }));
    await user.click(screen.getByRole("button", { name: "Deploy template" }));
    expect(await screen.findByText("response lost")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Deploy template" }));

    expect(state.createHumanProject).toHaveBeenCalledTimes(1);
    expect(state.deployTemplate).toHaveBeenCalledTimes(2);
    expect(state.deployTemplate.mock.calls[1]?.[2]).toBe(
      state.deployTemplate.mock.calls[0]?.[2],
    );
  });

  it("unmounts an active create flow if workspace authorization disappears", async () => {
    const user = userEvent.setup();
    state.operatorFeaturesAvailable = true;
    state.useTemplates.mockReturnValue({
      data: [],
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });
    const view = render(<NewProject />);

    await user.click(screen.getByRole("button", { name: /Empty project/ }));
    expect(screen.getByLabelText("Project name")).toBeInTheDocument();

    state.workspaces = [{
      id: "wrk_admin",
      slug: "admin",
      display_name: "Admin Workspace",
      role: "viewer",
    }];
    view.rerender(<NewProject />);

    expect(screen.queryByLabelText("Project name")).not.toBeInTheDocument();
    expect(
      screen.getByText(/no longer authorized for project creation/i),
    ).toBeInTheDocument();
    expect(state.createHumanProject).not.toHaveBeenCalled();
  });

  it("does not silently retarget an active flow when its selected workspace is revoked", async () => {
    const user = userEvent.setup();
    state.operatorFeaturesAvailable = true;
    state.workspaces = [
      {
        id: "wrk_admin",
        slug: "admin",
        display_name: "Admin Workspace",
        role: "admin",
      },
      {
        id: "wrk_member",
        slug: "member",
        display_name: "Member Workspace",
        role: "member",
      },
    ];
    state.useTemplates.mockReturnValue({
      data: [],
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });
    const view = render(<NewProject />);

    await user.selectOptions(screen.getByLabelText("Workspace"), "wrk_admin");
    await user.click(screen.getByRole("button", { name: /Empty project/ }));
    expect(screen.getByLabelText("Project name")).toBeInTheDocument();

    state.workspaces = [
      { ...state.workspaces[0], role: "viewer" },
      state.workspaces[1],
    ];
    view.rerender(<NewProject />);

    expect(screen.queryByLabelText("Project name")).not.toBeInTheDocument();
    expect(
      screen.getByText(/no longer authorized for project creation/i),
    ).toBeInTheDocument();
    expect(state.createHumanProject).not.toHaveBeenCalled();
  });

  it("fails a live create flow closed when workspace verification errors", async () => {
    const user = userEvent.setup();
    state.operatorFeaturesAvailable = true;
    state.useTemplates.mockReturnValue({
      data: [],
      isPending: false,
      error: null,
      refetch: vi.fn(),
    });
    const view = render(<NewProject />);

    await user.click(screen.getByRole("button", { name: /Empty project/ }));
    expect(screen.getByLabelText("Project name")).toBeInTheDocument();

    state.workspaceError = new Error("workspace verification unavailable");
    view.rerender(<NewProject />);

    expect(screen.queryByLabelText("Project name")).not.toBeInTheDocument();
    expect(screen.getByText("Could not load project workspaces")).toBeInTheDocument();
    expect(state.useTemplates).toHaveBeenLastCalledWith({ enabled: false });
    expect(state.createHumanProject).not.toHaveBeenCalled();
  });
});
