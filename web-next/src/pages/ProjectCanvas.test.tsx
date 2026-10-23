import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  authStatus: "human",
  operatorFeaturesAvailable: false,
  summaryOptions: [] as Array<{ enabled?: boolean }>,
  serviceOptions: [] as Array<{ enabled?: boolean }>,
  accessCalls: [] as Array<{
    projectId: string;
    environmentId?: string;
    enabled: boolean;
  }>,
  access: {
    data: {
      principal_kind: "human",
      project_id: "prj_1",
      workspace_id: "wrk_1",
      environment_id: "env_1",
      environment_restricted: false,
      permissions: [
        "project.read",
        "service.read",
        "service.write",
        "variables.write",
        "deployment.trigger.cli",
      ],
    },
    isPending: false,
    error: null as Error | null,
  },
  projectAccess: {
    data: {
      principal_kind: "human",
      project_id: "prj_1",
      workspace_id: "wrk_1",
      environment_id: undefined as string | undefined,
      environment_restricted: false,
      permissions: ["project.read", "service.read", "service.write", "variables.write"],
    },
    isPending: false,
    error: null as Error | null,
  },
  addServiceProps: [] as Array<{
    projectId: string;
    environmentId?: string;
    installationAdminAuthorized?: boolean;
    onClose?: () => void;
    onCreated?: (result: { serviceId?: string }) => void;
  }>,
  drawerProps: [] as Array<{
    installationAdminAuthorized?: boolean;
    installationAdminMutationAuthorized?: boolean;
    onSectionChange?: (section: string) => void;
  }>,
  durableChangeset: null as null | {
    state: "draft";
    items: Array<{
      service_id: string;
      operation: "add" | "edit" | "remove";
    }>;
  },
  deploymentFeed: {
    items: [
      {
        service: {
          id: "svc_human",
          project_id: "prj_1",
          slug: "human-service",
          display_name: "Human service",
        },
        deployment: {
          id: "dep_1",
          deployment_id: "dep_1",
          service_id: "svc_human",
          project_id: "prj_1",
          environment_id: "env_1",
          operation_id: "op_1",
          operation_state: "SUCCEEDED",
          status: "active",
          active: true,
          image: "registry.example/app@sha256:abc",
          last_event_at: "2026-08-12T12:00:00Z",
        },
      },
    ],
    isPending: false,
    isFetching: false,
    error: null as Error | null,
    hasInFlight: false,
    refetchAll: vi.fn(),
  },
  navigate: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => state.navigate,
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
  useProject: () => ({
    data: {
      id: "prj_1",
      slug: "midnight",
      display_name: "Midnight",
    },
  }),
  useEnvironmentResources: (
    _environmentId: string | undefined,
    options: { enabled?: boolean },
  ) => {
    state.summaryOptions.push(options);
    return {
      data: {
        project: {
          id: "prj_1",
          slug: "midnight",
          display_name: "Operator summary",
        },
        resources: [
          {
            service: {
              id: "svc_operator",
              project_id: "prj_1",
              slug: "operator-only",
              display_name: "Operator only",
            },
          },
        ],
      },
      isPending: false,
      error: null,
      refetch: vi.fn(),
    };
  },
  useEnvironmentChangeset: () => ({
    data: { changeset: state.durableChangeset },
  }),
  useServices: (
    _projectId: string,
    _environmentId: string | undefined,
    options: { enabled?: boolean },
  ) => {
    state.serviceOptions.push(options);
    return {
      data: [
        {
          id: "svc_human",
          project_id: "prj_1",
          slug: "human-service",
          display_name: "Human service",
        },
      ],
      isPending: false,
      error: null,
      refetch: vi.fn(),
    };
  },
}));

vi.mock("@/components/ResourceGrid", () => ({
  ResourceGrid: ({
    resources,
    deploymentEvidenceAvailable,
    onViewLogs,
  }: {
    resources: Array<{ service: { display_name: string } }>;
    deploymentEvidenceAvailable: boolean;
    onViewLogs?: unknown;
  }) => (
    <div
      data-testid="resource-grid"
      data-deployment-evidence={String(deploymentEvidenceAvailable)}
      data-has-log-action={String(Boolean(onViewLogs))}
    >
      {resources.map((resource) => resource.service.display_name).join(",")}
    </div>
  ),
}));

vi.mock("@/components/Canvas", () => ({
  Canvas: () => <div data-testid="map-canvas" />,
}));
vi.mock("@/components/ServiceDetailDrawer", () => ({
  ServiceDetailDrawer: (props: {
    installationAdminAuthorized?: boolean;
    installationAdminMutationAuthorized?: boolean;
    onSectionChange?: (section: string) => void;
  }) => {
    state.drawerProps.push(props);
    return null;
  },
}));
vi.mock("@/components/panels/add-resource/AddResourcePalette", () => ({
  AddResourcePalette: (props: {
    projectId: string;
    environmentId?: string;
    installationAdminAuthorized?: boolean;
    onClose?: () => void;
    onCreated?: (result: { serviceId?: string }) => void;
  }) => {
    state.addServiceProps.push(props);
    return <div data-testid="add-resource-palette" />;
  },
}));
vi.mock("@/components/panels/automation-tokens/api", () => ({
  useProjectAccess: (
    projectId: string,
    environmentId: string | undefined,
    enabled: boolean,
  ) => {
    state.accessCalls.push({ projectId, environmentId, enabled });
    return environmentId === undefined ? state.projectAccess : state.access;
  },
}));
vi.mock("@/components/panels/deployments/api", () => ({
  useProjectDeploymentsFeed: () => state.deploymentFeed,
}));
vi.mock("@/components/StagedChangesBanner", () => ({
  StagedChangesBanner: () => <div data-testid="staged-changes-banner" />,
}));
vi.mock("@/components/AgentPanel/agent-dock-state", () => ({
  setAgentDockCollapsed: vi.fn(),
}));

import {
  ProjectCanvas,
  requiredConfigurePermission,
  requiredServiceEvidencePermission,
  serviceDraftOperationsFrom,
} from "./ProjectCanvas";
import {
  __resetStagedChangesForTests,
  getAllStaged,
  stageChange,
} from "@/lib/stagedChanges";

beforeEach(() => {
  vi.clearAllMocks();
  __resetStagedChangesForTests();
  state.authStatus = "human";
  state.operatorFeaturesAvailable = false;
  state.summaryOptions = [];
  state.serviceOptions = [];
  state.accessCalls = [];
  state.access.data.project_id = "prj_1";
  state.access.data.environment_id = "env_1";
  state.access.data.permissions = [
    "project.read",
    "service.read",
    "service.write",
    "variables.write",
    "deployment.trigger.cli",
  ];
  state.access.isPending = false;
  state.access.error = null;
  state.projectAccess.data.project_id = "prj_1";
  state.projectAccess.data.environment_id = undefined;
  state.projectAccess.data.permissions = [
    "project.read",
    "service.read",
    "service.write",
    "variables.write",
  ];
  state.projectAccess.isPending = false;
  state.projectAccess.error = null;
  state.addServiceProps = [];
  state.drawerProps = [];
  state.durableChangeset = null;
});

describe("ProjectCanvas authority-aware grid", () => {
  it("maps installation evidence and mutation panels to their canonical RBAC permissions", () => {
    expect(requiredServiceEvidencePermission("overview")).toBe("service.read");
    expect(requiredServiceEvidencePermission("logs")).toBe("logs.read");
    expect(requiredServiceEvidencePermission("metrics")).toBe("metrics.read");
    expect(requiredConfigurePermission("variables")).toBe("variables.write");
    expect(requiredConfigurePermission("networking")).toBe("domains.write");
    expect(requiredConfigurePermission("storage")).toBe("volume.write");
    expect(requiredConfigurePermission("runtime")).toBe("service.write");
    expect(requiredConfigurePermission("source")).toBe("service.write");
  });

  it("does not promote resource add/remove intent to service lifecycle tags", () => {
    expect(
      serviceDraftOperationsFrom(
        [
          { serviceId: "svc_api", op: "edit" },
          { serviceId: "svc_api", op: "edit" },
          { serviceId: "svc_worker", op: "remove" },
        ],
        [
          { service_id: "svc_api", operation: "add" },
          { service_id: "svc_worker", operation: "remove" },
        ],
      ),
    ).toEqual({
      svc_api: ["edit"],
      svc_worker: ["edit"],
    });
  });

  it("lets Configure choose its own source-first or remembered default", () => {
    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        serviceId="svc_operator"
        section="overview"
      />,
    );

    state.drawerProps.at(-1)?.onSectionChange?.("configure");
    expect(state.navigate).toHaveBeenCalledWith(
      expect.objectContaining({
        params: expect.objectContaining({ section: "configure" }),
        search: expect.objectContaining({ configure: undefined }),
      }),
    );
  });

  it("uses the tenant-safe canonical environment summary for a human session", () => {
    render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    expect(state.summaryOptions.at(-1)?.enabled).toBe(true);
    expect(state.serviceOptions.at(-1)?.enabled).toBe(false);
    expect(screen.getByText("Environment resources")).toBeInTheDocument();
    expect(screen.getByTestId("resource-grid")).toHaveTextContent(
      "Operator only",
    );
    expect(screen.getByTestId("resource-grid")).toHaveAttribute(
      "data-deployment-evidence",
      "true",
    );
    expect(screen.getByTestId("resource-grid")).toHaveAttribute(
      "data-has-log-action",
      "true",
    );
    expect(screen.queryByText("Human service")).not.toBeInTheDocument();
  });

  it("retains the canonical environment summary for the operator principal", () => {
    state.authStatus = "operator";
    state.operatorFeaturesAvailable = true;

    render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    expect(state.summaryOptions.at(-1)?.enabled).toBe(true);
    expect(state.serviceOptions.at(-1)?.enabled).toBe(false);
    expect(screen.getByText("Environment resources")).toBeInTheDocument();
    expect(screen.getByTestId("resource-grid")).toHaveTextContent(
      "Operator only",
    );
    expect(screen.getByTestId("resource-grid")).toHaveAttribute(
      "data-deployment-evidence",
      "true",
    );
    expect(screen.getByTestId("resource-grid")).toHaveAttribute(
      "data-has-log-action",
      "true",
    );
    expect(screen.queryByTestId("staged-changes-banner")).not.toBeInTheDocument();
  });

  it("uses installation evidence for an administrator human only after exact project access", () => {
    state.operatorFeaturesAvailable = true;

    const view = render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );
    expect(state.summaryOptions.at(-1)?.enabled).toBe(true);
    expect(state.serviceOptions.at(-1)?.enabled).toBe(false);
    expect(screen.getByTestId("resource-grid")).toHaveTextContent("Operator only");

    state.access.data.project_id = "prj_other";
    view.rerender(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );
    expect(state.summaryOptions.at(-1)?.enabled).toBe(false);
    expect(state.serviceOptions.at(-1)?.enabled).toBe(false);
    expect(screen.queryByTestId("resource-grid")).not.toBeInTheDocument();
    expect(screen.getByText("No resources in this environment")).toBeInTheDocument();
  });

  it("separates read-only installation evidence from service mutation authority", () => {
    state.operatorFeaturesAvailable = true;
    state.access.data.permissions = ["project.read", "service.read"];

    const view = render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        serviceId="svc_human"
        view="grid"
      />,
    );
    expect(state.drawerProps.at(-1)).toMatchObject({
      installationAdminAuthorized: true,
      installationAdminMutationAuthorized: false,
    });

    state.access.data.permissions.push("variables.write");
    view.rerender(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        serviceId="svc_human"
        view="grid"
      />,
    );
    expect(state.drawerProps.at(-1)).toMatchObject({
      installationAdminAuthorized: true,
      installationAdminMutationAuthorized: true,
    });
  });

  it("retains human drafts and mounts the durable save surface with exact variables-write authority", async () => {
    stageChange({
      serviceId: "svc_human",
      environmentId: "env_1",
      name: "DATABASE_URL",
      op: "add",
      scope: "secret",
      value: "plaintext-secret",
    });

    render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    expect(screen.getByTestId("staged-changes-banner")).toBeInTheDocument();
    await waitFor(() => expect(getAllStaged()).toHaveLength(1));
  });

  it("does not let installation-admin capability substitute for exact variables-write access", async () => {
    state.operatorFeaturesAvailable = true;
    state.access.data.permissions = ["project.read", "service.read"];
    stageChange({
      serviceId: "svc_human",
      environmentId: "env_1",
      name: "DATABASE_URL",
      op: "add",
      scope: "secret",
      value: "plaintext-secret",
    });

    render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    expect(screen.queryByTestId("staged-changes-banner")).not.toBeInTheDocument();
    await waitFor(() => expect(getAllStaged()).toEqual([]));
  });

  it("retains the staged save surface for an exact variables-write grant", async () => {
    state.operatorFeaturesAvailable = true;
    state.access.data.permissions.push("variables.write");
    stageChange({
      serviceId: "svc_human",
      environmentId: "env_1",
      name: "DATABASE_URL",
      op: "add",
      scope: "secret",
      value: "plaintext-secret",
    });

    render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    await waitFor(() => expect(getAllStaged()).toHaveLength(1));
    expect(screen.getByTestId("staged-changes-banner")).toBeInTheDocument();
  });

  it("clears a staged resource kind when its exact write capability is lost", async () => {
    state.access.data.permissions = ["project.read", "service.write"];
    stageChange({
      kind: "storage",
      serviceId: "svc_human",
      environmentId: "env_1",
      name: "data",
      op: "add",
      storage: { size_bytes: 1024, mount_path: "/data" },
    });

    render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    await waitFor(() => expect(getAllStaged()).toEqual([]));
  });

  it("preserves drafts while authority is checking but clears them in operator recovery", async () => {
    state.authStatus = "checking";
    stageChange({
      serviceId: "svc_operator",
      environmentId: "env_1",
      name: "DATABASE_URL",
      op: "add",
      scope: "secret",
      value: "plaintext-secret",
    });

    const view = render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );
    await waitFor(() => expect(getAllStaged()).toHaveLength(1));
    expect(
      screen.queryByTestId("staged-changes-banner"),
    ).not.toBeInTheDocument();

    state.authStatus = "operator";
    state.operatorFeaturesAvailable = true;
    view.rerender(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    await waitFor(() => expect(getAllStaged()).toEqual([]));
    expect(screen.queryByTestId("staged-changes-banner")).not.toBeInTheDocument();
  });

  it("opens the tenant-safe resource palette for exact project service-write access", () => {
    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        tab="add-service"
      />,
    );

    expect(screen.getByTestId("add-resource-palette")).toBeInTheDocument();
    expect(state.addServiceProps.at(-1)).toMatchObject({
      projectId: "prj_1",
      environmentId: "env_1",
      installationAdminAuthorized: false,
    });
    expect(state.accessCalls.at(-1)).toEqual({
      projectId: "prj_1",
      environmentId: "env_1",
      enabled: true,
    });
  });

  it("opens a created service without racing the palette close route", () => {
    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        tab="add-service"
      />,
    );

    state.addServiceProps.at(-1)?.onCreated?.({ serviceId: "svc_created" });

    expect(state.navigate).toHaveBeenCalledWith({
      to: "/projects/$projectId/services/$serviceId/$section",
      params: {
        projectId: "prj_1",
        serviceId: "svc_created",
        section: "overview",
      },
      search: {
        env: "env_1",
        view: undefined,
        deployment: undefined,
        configure: undefined,
      },
      replace: false,
    });
  });

  it("does not confer installation capability when environment evidence is not exact", () => {
    state.access.data.environment_id = "env_other";
    state.access.data.permissions = ["project.read", "service.write"];

    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        tab="add-service"
      />,
    );

    expect(state.addServiceProps.at(-1)).toMatchObject({
      installationAdminAuthorized: false,
    });
  });

  it("exposes installation-admin GitHub capability while retaining human mutation authority", () => {
    state.operatorFeaturesAvailable = true;

    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        tab="add-service"
      />,
    );

    expect(state.addServiceProps.at(-1)).toMatchObject({
      installationAdminAuthorized: true,
    });
  });

  it("keeps environment-scoped installation features closed when no environment is selected", () => {
    render(
      <ProjectCanvas projectId="prj_1" tab="add-service" />,
    );

    expect(state.addServiceProps.at(-1)).toMatchObject({
      installationAdminAuthorized: false,
    });
    expect(state.accessCalls).toContainEqual({
      projectId: "prj_1",
      environmentId: undefined,
      enabled: true,
    });
  });

  it("does not mount the form while exact access admission is pending", () => {
    state.projectAccess.isPending = true;

    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        tab="add-service"
      />,
    );

    expect(screen.queryByTestId("add-resource-palette")).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "Checking the current project role before enabling service creation.",
      ),
    ).toBeInTheDocument();
  });

  it("keeps service creation reachable when the project already has services", () => {
    render(
      <ProjectCanvas projectId="prj_1" environmentId="env_1" view="grid" />,
    );

    fireEvent.click(
      screen.getByRole("button", { name: "+ Add resource" }),
    );
    expect(state.navigate).toHaveBeenCalledWith({
      to: "/projects/$projectId",
      params: { projectId: "prj_1" },
      search: { env: "env_1", tab: "add-service" },
    });
  });

  it("does not mount service creation without service-write permission", () => {
    state.projectAccess.data.permissions = ["project.read"];

    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
      />,
    );

    expect(
      screen.queryByRole("button", { name: "+ Add resource" }),
    ).not.toBeInTheDocument();
  });

  it("does not mount the service form without service-write permission", () => {
    state.projectAccess.data.permissions = ["project.read"];

    render(
      <ProjectCanvas
        projectId="prj_1"
        environmentId="env_1"
        tab="add-service"
      />,
    );

    expect(screen.queryByTestId("add-resource-palette")).not.toBeInTheDocument();
    expect(screen.getByText("Service creation")).toBeInTheDocument();
    expect(
      screen.getByText(
        "The current project role does not grant service-write permission.",
      ),
    ).toBeInTheDocument();
  });
});
