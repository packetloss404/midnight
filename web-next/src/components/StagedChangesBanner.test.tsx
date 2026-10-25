import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError } from "@/api/client";
import {
  __resetStagedChangesForTests,
  stageChange,
} from "@/lib/stagedChanges";
import { StagedChangesBanner } from "./StagedChangesBanner";

const changesetHook = vi.hoisted(() => ({
  data: { changeset: null as null | Record<string, unknown> },
  error: null,
  refetch: vi.fn(),
}));
const commitMutation = vi.hoisted(() => ({
  error: null as Error | null,
  isPending: false,
  mutate: vi.fn(),
}));
const apiMocks = vi.hoisted(() => ({
  applyEnvironmentChangeset: vi.fn(),
  applyAndDeployEnvironmentChangeset: vi.fn(),
  getEnvironmentOperationGroup: vi.fn(),
  discardEnvironmentChangeset: vi.fn(),
}));

vi.mock("./panels/variables/api", () => ({
  useCommitStagedChanges: () => commitMutation,
}));
vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  api: apiMocks,
}));
vi.mock("@/api/hooks", () => ({
  queryKeys: {
    environmentChangeset: (id?: string) => ["environment-changeset", id],
    variables: (serviceId?: string, environmentId?: string) => ["variables", serviceId, environmentId],
    environmentResources: (id?: string) => ["environment-resources", id],
  },
  useEnvironmentChangeset: () => changesetHook,
}));

afterEach(() => {
  act(() => {
    __resetStagedChangesForTests();
  });
  changesetHook.data.changeset = null;
  delete (changesetHook.data as Record<string, unknown>).validation;
  delete (changesetHook.data as Record<string, unknown>).capabilities;
  commitMutation.error = null;
  commitMutation.isPending = false;
  commitMutation.mutate.mockReset();
  changesetHook.refetch.mockReset();
  changesetHook.refetch.mockResolvedValue({ data: changesetHook.data });
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("StagedChangesBanner", () => {
  it("labels unsaved input honestly and never renders secret or sealed values", () => {
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "NEW_SECRET",
      op: "add",
      scope: "secret",
      value: "secret-must-never-render",
    });
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "SEALED_CERT",
      op: "add",
      scope: "sealed",
      value: "sealed-must-never-render",
    });
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "ROTATED_TOKEN",
      op: "edit",
      scope: "secret",
      value: "rotated-must-never-render",
      base: {
        id: "var-rotated-token",
        workspaceId: "",
        projectId: "project-1",
        environmentId: "env-production",
        serviceId: "svc-api",
        scope: "plain",
        version: 3,
        lengthBucket: "17-32",
        sealed: false,
      },
    });
    stageChange({
      serviceId: "svc-web",
      environmentId: "env-production",
      name: "PUBLIC_ORIGIN",
      op: "add",
      scope: "plain",
      value: "https://midnight.test",
    });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner
          projectId="project-1"
          environmentId="env-production"
        />
      </QueryClientProvider>,
    );

    expect(screen.getByText("Changes")).toBeVisible();
    expect(screen.getByText("4 changes")).toBeVisible();
    expect(screen.getByText("Secure values wait for confirmation")).toBeVisible();
    expect(screen.getByRole("button", { name: "Save for review" })).toBeVisible();
    expect(container.textContent).not.toMatch(/staged change/i);

    fireEvent.click(screen.getByRole("button", { name: "Review" }));

    expect(screen.getAllByText("value set")).toHaveLength(2);
    expect(screen.getByText("value replaced")).toBeVisible();
    expect(screen.getByText("value hidden")).toBeVisible();
    expect(screen.getByText("https://midnight.test")).toBeVisible();
    expect(container.textContent).not.toContain("secret-must-never-render");
    expect(container.textContent).not.toContain("sealed-must-never-render");
    expect(container.textContent).not.toContain("rotated-must-never-render");
  });

  it("reviews a server-side draft after refresh without exposing values", () => {
    changesetHook.data.changeset = {
      id: "chg-1",
      workspace_id: "workspace-1",
      project_id: "project-1",
      environment_id: "env-production",
      created_by: "user-1",
      state: "draft",
      revision: 4,
      items: [{
        id: "item-1",
        kind: "variable",
        operation: "edit",
        service_id: "svc-api",
        target_id: "var-1",
        name: "DATABASE_URL",
        scope: "secret",
        expected_version: 3,
        value_change: "value replaced",
      }],
      created_at: "2026-08-13T00:00:00Z",
      updated_at: "2026-08-13T00:00:00Z",
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: true,
      affected_services: ["svc-api"],
      errors: [],
      warnings: [],
    };
    (changesetHook.data as Record<string, unknown>).capabilities = [];

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner
          projectId="project-1"
          environmentId="env-production"
          serviceLabels={{ "svc-api": "API" }}
        />
      </QueryClientProvider>,
    );

    expect(screen.getByText("1 saved · revision 4")).toBeVisible();
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByText("Saved draft · revision 4")).toBeVisible();
    expect(screen.getByText("DATABASE_URL")).toBeVisible();
    expect(screen.getByText("value replaced")).toBeVisible();
    expect(container.textContent).not.toMatch(/postgres|password|secret-must/i);
    expect(screen.getByText(/queues only the services marked for deployment/i)).toBeVisible();
    expect(screen.getByText("Impact checking")).toBeVisible();
  });

  it("reviews mixed tab-local resource intent before one durable save", () => {
    stageChange({
      kind: "networking",
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "generated public domain",
      op: "add",
      networking: { kind: "generated", public: true },
    });
    stageChange({
      kind: "storage",
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "data",
      op: "add",
      storage: { size_bytes: 1024, mount_path: "/data" },
    });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );

    expect(screen.getByText("2 changes")).toBeVisible();
    expect(screen.getByText("Autosaves securely")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByText("networking")).toBeVisible();
    expect(screen.getByText("add generated public domain")).toBeVisible();
    expect(screen.getByText("storage")).toBeVisible();
    expect(screen.getByText("create · 1024 bytes · /data")).toBeVisible();
  });

  it("uses only server-derived action labels for saved service impact", () => {
    changesetHook.data.changeset = {
      id: "chg-impact",
      state: "draft",
      revision: 12,
      items: [
        { id: "item-none", kind: "networking", operation: "add", service_id: "svc-edge", name: "domain", value_change: "intent added" },
        { id: "item-redeploy", kind: "variable", operation: "edit", service_id: "svc-api", name: "PORT", value_change: "value replaced" },
        { id: "item-rebuild", kind: "runtime", operation: "edit", service_id: "svc-web", name: "runtime", value_change: "configuration updated" },
      ],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: false,
      affected_services: ["svc-api", "svc-edge", "svc-web"],
      affected_resources: [
        { service_id: "svc-edge", resource_kinds: ["networking"], requires_deploy: false, deployment_action: "none" },
        { service_id: "svc-api", resource_kinds: ["variable"], requires_deploy: true, deployment_action: "redeploy" },
        { service_id: "svc-web", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "rebuild" },
      ],
      errors: [
        "service configuration changed since this review was prepared",
        "Cross-service policy rejected the draft",
      ],
      warnings: [],
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner
          environmentId="env-production"
          serviceLabels={{ "svc-edge": "Edge", "svc-api": "API", "svc-web": "Web" }}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByText("Apply only")).toBeVisible();
    expect(screen.getByText("Redeploy")).toBeVisible();
    expect(screen.getByText("Build and deploy")).toBeVisible();
    expect(screen.queryByText("Will redeploy")).not.toBeInTheDocument();
    expect(
      screen.getByLabelText("Validation issue for runtime"),
    ).toHaveTextContent(
      "service configuration changed since this review was prepared",
    );
    const globalIssues = screen.getByRole("region", {
      name: "Global validation issues",
    });
    expect(globalIssues).toHaveTextContent("Cross-service policy rejected the draft");
    expect(globalIssues).not.toHaveTextContent("service configuration changed");
  });

  it("autosaves non-sensitive input after a debounce but never autosaves secret input", () => {
    vi.useFakeTimers();
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "PUBLIC_ORIGIN",
      op: "add",
      scope: "plain",
      value: "https://midnight.test",
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { unmount } = render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );

    expect(commitMutation.mutate).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(700));
    expect(commitMutation.mutate).toHaveBeenCalledWith(
      { changes: [expect.objectContaining({ name: "PUBLIC_ORIGIN" })] },
      expect.objectContaining({ onSuccess: expect.any(Function), onError: expect.any(Function) }),
    );
    unmount();

    act(() => __resetStagedChangesForTests());
    commitMutation.mutate.mockReset();
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "TOKEN",
      op: "add",
      scope: "secret",
      value: "must-not-leak",
    });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );
    act(() => vi.advanceTimersByTime(2_000));
    expect(commitMutation.mutate).not.toHaveBeenCalled();
  });

  it("keeps apply-and-deploy primary and apply-only in advanced actions", async () => {
    changesetHook.data.changeset = {
      id: "chg-1",
      state: "draft",
      revision: 7,
      items: [{ id: "item-1", kind: "runtime", operation: "edit", service_id: "svc-api", name: "runtime", value_change: "configuration updated" }],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: true,
      affected_services: ["svc-api"],
      affected_resources: [{ service_id: "svc-api", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "redeploy" }],
      errors: [],
      warnings: [],
    };
    (changesetHook.data as Record<string, unknown>).capabilities = [];
    apiMocks.applyAndDeployEnvironmentChangeset.mockResolvedValue({
      applied_revision: 7,
      operation_group_id: "group-7",
      affected_services: ["svc-api"],
      changeset: null,
      validation: { valid: true, affected_services: ["svc-api"], errors: [], warnings: [] },
      capabilities: [],
      operation_group: {
        id: "group-7",
        environment_id: "env-production",
        changeset_id: "chg-1",
        configuration_revision: 7,
        state: "queued",
        items: [{ service_id: "svc-api", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "redeploy", intake_state: "queued" }],
        created_at: "2026-09-01T00:00:00Z",
      },
    });
    apiMocks.applyEnvironmentChangeset.mockResolvedValue({ changeset: null });
    apiMocks.getEnvironmentOperationGroup.mockResolvedValue({
      id: "group-7",
      environment_id: "env-production",
      changeset_id: "chg-1",
      configuration_revision: 7,
      state: "succeeded",
      items: [{
        service_id: "svc-api",
        resource_kinds: ["runtime"],
        requires_deploy: true,
        deployment_action: "redeploy",
        intake_state: "queued",
        deployment_id: "dep-7",
        operation_id: "op-7",
        operation_state: "SUCCEEDED",
        deployment_state: "active",
      }],
      created_at: "2026-09-01T00:00:00Z",
    });
    const viewService = vi.fn();
    const viewDeployment = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner
          environmentId="env-production"
          serviceLabels={{ "svc-api": "API" }}
          operations={[{
            serviceId: "svc-api",
            serviceLabel: "API",
            deployment: {
              deployment_id: "dep-7",
              operation_id: "op-7",
              operation_state: "running",
            },
          }]}
          onViewService={viewService}
          onViewDeployment={viewDeployment}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByText("Redeploy")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    await waitFor(() => expect(apiMocks.applyAndDeployEnvironmentChangeset).toHaveBeenCalledWith("env-production", 7));
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["tenant-service-environment-variables"] }));
    expect(await screen.findByRole("region", { name: "Activity" })).toBeVisible();
    await waitFor(() =>
      expect(apiMocks.getEnvironmentOperationGroup).toHaveBeenCalledWith(
        "env-production",
        "group-7",
        expect.any(AbortSignal),
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: /activity/i }));
    expect(await screen.findByText("succeeded")).toBeVisible();
    expect(screen.getByText("active")).toBeVisible();
    expect(screen.getByRole("region", { name: "Activity" })).toHaveTextContent("1 item");
    fireEvent.click(screen.getByRole("button", { name: "API" }));
    expect(viewService).toHaveBeenCalledWith("svc-api");
    fireEvent.click(screen.getByRole("button", { name: "View deployment" }));
    expect(viewDeployment).toHaveBeenCalledWith("svc-api", "dep-7");
    expect(
      screen.getByRole("region", { name: "Activity" }).querySelector(".bg-success"),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Advanced change actions"));
    expect(screen.getByRole("button", { name: "Apply without deploying" })).toBeVisible();
  });

  it("renders deployment activity separately from pending changes", () => {
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "PUBLIC_ORIGIN",
      op: "add",
      scope: "plain",
      value: "https://midnight.test",
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner
          environmentId="env-production"
          operations={[{ serviceId: "svc-api", serviceLabel: "API", deployment: { operation_id: "op-1", operation_state: "running", last_event: "building" } }]}
        />
      </QueryClientProvider>,
    );

    expect(screen.getByRole("region", { name: "Activity" })).toBeVisible();
    expect(screen.getByRole("region", { name: "Changes" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: /activity/i }));
    expect(screen.getByText("building")).toBeVisible();
  });

  it("does not apply a saved revision while newer tab edits are excluded", () => {
    changesetHook.data.changeset = {
      id: "chg-4",
      state: "draft",
      revision: 4,
      items: [{ id: "item-1", kind: "networking", operation: "add", service_id: "svc-api", name: "domain", value_change: "intent added" }],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: true,
      affected_services: ["svc-api"],
      affected_resources: [{ service_id: "svc-api", resource_kinds: ["networking"], requires_deploy: false, deployment_action: "none" }],
      errors: [],
      warnings: [],
    };
    (changesetHook.data as Record<string, unknown>).capabilities = [];
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "PORT",
      op: "add",
      scope: "plain",
      value: "8080",
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );

    expect(screen.getByText("1 not saved")).toBeVisible();
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByText(/Not in saved revision 4/i)).toBeVisible();
    expect(screen.getByRole("button", { name: "Discard unsaved" })).toBeVisible();
    expect(screen.getByText(/Discard or save the newer edits/i)).toBeVisible();
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toHaveAttribute(
      "aria-describedby",
      "apply-deploy-disabled-reason",
    );
  });

  it("recovers a 412 with the latest revision without losing in-flight local edits", async () => {
    changesetHook.data.changeset = {
      id: "chg-stale",
      state: "draft",
      revision: 7,
      items: [{ id: "item-old", kind: "runtime", operation: "edit", service_id: "svc-api", name: "runtime", value_change: "configuration updated" }],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: true,
      affected_services: ["svc-api"],
      affected_resources: [{ service_id: "svc-api", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "redeploy" }],
      errors: [],
      warnings: [],
    };
    let rejectFirst: ((error: unknown) => void) | undefined;
    apiMocks.applyAndDeployEnvironmentChangeset
      .mockImplementationOnce(
        () => new Promise((_resolve, reject) => { rejectFirst = reject; }),
      )
      .mockResolvedValueOnce({
        applied_revision: 8,
        operation_group_id: "group-8",
        affected_services: ["svc-api"],
        validation: {
          valid: true,
          affected_services: ["svc-api"],
          affected_resources: [{ service_id: "svc-api", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "redeploy" }],
          errors: [],
          warnings: [],
        },
        operation_group: {
          id: "group-8",
          environment_id: "env-production",
          changeset_id: "chg-stale",
          configuration_revision: 8,
          state: "queued",
          items: [{ service_id: "svc-api", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "redeploy", intake_state: "queued" }],
          created_at: "2026-09-01T00:00:00Z",
        },
      });
    let resolveLatest: (() => void) | undefined;
    changesetHook.refetch.mockImplementationOnce(
      () => new Promise((resolve) => {
        resolveLatest = () => {
          changesetHook.data.changeset = {
            id: "chg-latest",
            state: "draft",
            revision: 8,
            items: [{ id: "item-latest", kind: "runtime", operation: "edit", service_id: "svc-api", name: "runtime", value_change: "configuration updated" }],
          };
          resolve({ data: changesetHook.data, error: null });
        };
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    await waitFor(() =>
      expect(apiMocks.applyAndDeployEnvironmentChangeset).toHaveBeenCalledWith(
        "env-production",
        7,
      ),
    );
    act(() => {
      stageChange({
        serviceId: "svc-api",
        environmentId: "env-production",
        name: "LOCAL_ONLY",
        op: "add",
        scope: "plain",
        value: "keep-me",
      });
      rejectFirst?.(new ApiError(412, {
        error: { code: "RESOURCE_CONFLICT", message: "revision changed" },
      }));
    });

    expect(await screen.findByText(/is reloading the latest draft/i)).toBeVisible();
    await waitFor(() => expect(changesetHook.refetch).toHaveBeenCalled());
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["environment-changeset", "env-production"],
      refetchType: "none",
    });
    expect(screen.getByText("1 not saved")).toBeVisible();
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Advanced change actions"));
    expect(screen.getByRole("button", { name: "Apply without deploying" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Discard saved revision" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply without deploying" }));
    fireEvent.click(screen.getByRole("button", { name: "Discard saved revision" }));
    expect(apiMocks.applyAndDeployEnvironmentChangeset).toHaveBeenCalledTimes(1);
    expect(apiMocks.applyEnvironmentChangeset).not.toHaveBeenCalled();
    expect(apiMocks.discardEnvironmentChangeset).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Reloading latest…" })).toBeDisabled();

    act(() => resolveLatest?.());
    expect(await screen.findByText(/latest saved revision is loaded/i)).toBeVisible();
    expect(screen.getByRole("button", { name: "Review latest" })).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Review latest" }));
    expect(screen.getByText("Saved draft · revision 8")).toBeVisible();
    expect(screen.getByText("LOCAL_ONLY")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Discard unsaved" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    await waitFor(() =>
      expect(apiMocks.applyAndDeployEnvironmentChangeset).toHaveBeenLastCalledWith(
        "env-production",
        8,
      ),
    );
  });

  it.each([
    {
      name: "advanced apply",
      button: "Apply without deploying",
      mutation: apiMocks.applyEnvironmentChangeset,
    },
    {
      name: "discard",
      button: "Discard saved revision",
      mutation: apiMocks.discardEnvironmentChangeset,
    },
  ])("blocks every revision action while $name refreshes after 412", async ({ button, mutation }) => {
    changesetHook.data.changeset = {
      id: "chg-stale-secondary",
      state: "draft",
      revision: 11,
      items: [{ id: "item-secondary", kind: "networking", operation: "add", service_id: "svc-api", name: "domain", value_change: "intent added" }],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: true,
      affected_services: ["svc-api"],
      affected_resources: [{ service_id: "svc-api", resource_kinds: ["networking"], requires_deploy: false, deployment_action: "none" }],
      errors: [],
      warnings: [],
    };
    mutation.mockRejectedValueOnce(new ApiError(412, {
      error: { code: "RESOURCE_CONFLICT", message: "revision changed" },
    }));
    let resolveLatest: (() => void) | undefined;
    changesetHook.refetch.mockImplementationOnce(
      () => new Promise((resolve) => {
        resolveLatest = () => resolve({ data: changesetHook.data, error: null });
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByLabelText("Advanced change actions"));
    fireEvent.click(screen.getByRole("button", { name: button }));
    await waitFor(() => expect(changesetHook.refetch).toHaveBeenCalled());
    expect(mutation).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Apply without deploying" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Discard saved revision" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: button }));
    expect(mutation).toHaveBeenCalledTimes(1);

    act(() => resolveLatest?.());
    expect(await screen.findByRole("button", { name: "Review latest" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toBeDisabled();
  });

  it("offers Reload latest and keeps actions blocked when the refresh fails", async () => {
    changesetHook.data.changeset = {
      id: "chg-refresh-fails",
      state: "draft",
      revision: 5,
      items: [{ id: "item-refresh-fails", kind: "networking", operation: "add", service_id: "svc-api", name: "domain", value_change: "intent added" }],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: true,
      affected_services: ["svc-api"],
      affected_resources: [{ service_id: "svc-api", resource_kinds: ["networking"], requires_deploy: false, deployment_action: "none" }],
      errors: [],
      warnings: [],
    };
    apiMocks.applyAndDeployEnvironmentChangeset.mockRejectedValueOnce(
      new ApiError(412, {
        error: { code: "RESOURCE_CONFLICT", message: "revision changed" },
      }),
    );
    changesetHook.refetch.mockResolvedValueOnce({
      data: changesetHook.data,
      error: new Error("refresh failed"),
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    expect(await screen.findByRole("button", { name: "Reload latest" })).toBeVisible();
    expect(screen.getByText(/could not be reloaded/i)).toBeVisible();
    expect(screen.queryByRole("button", { name: "Review latest" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toBeDisabled();
  });

  it("keeps Escape inside review from closing the underlying inspector", () => {
    stageChange({
      serviceId: "svc-api",
      environmentId: "env-production",
      name: "TOKEN",
      op: "add",
      scope: "secret",
      value: "hidden",
    });
    const bubbleListener = vi.fn((event: KeyboardEvent) => event.defaultPrevented);
    document.addEventListener("keydown", bubbleListener);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(bubbleListener).toHaveReturnedWith(true);
    document.removeEventListener("keydown", bubbleListener);
  });

  it("clears submitted activity when the selected environment changes", async () => {
    changesetHook.data.changeset = {
      id: "chg-9",
      state: "draft",
      revision: 9,
      items: [{ id: "item-9", kind: "runtime", operation: "edit", service_id: "svc-api", name: "runtime", value_change: "configuration updated" }],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: true,
      affected_services: ["svc-api"],
      errors: [],
      warnings: [],
    };
    apiMocks.applyAndDeployEnvironmentChangeset.mockResolvedValue({
      applied_revision: 9,
      operation_group_id: "group-9",
      affected_services: ["svc-api"],
      validation: { valid: true, affected_services: ["svc-api"], errors: [], warnings: [] },
      operation_group: {
        id: "group-9",
        environment_id: "env-production",
        changeset_id: "chg-9",
        configuration_revision: 9,
        state: "queued",
        items: [{ service_id: "svc-api", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "rebuild", intake_state: "queued" }],
        created_at: "2026-09-01T00:00:00Z",
      },
    });
    apiMocks.getEnvironmentOperationGroup.mockResolvedValue({
      id: "group-9",
      environment_id: "env-production",
      changeset_id: "chg-9",
      configuration_revision: 9,
      state: "queued",
      items: [],
      created_at: "2026-09-01T00:00:00Z",
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    expect(await screen.findByRole("region", { name: "Activity" })).toBeVisible();

    changesetHook.data.changeset = null;
    view.rerender(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-staging" />
      </QueryClientProvider>,
    );
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Activity" })).not.toBeInTheDocument(),
    );
    expect(apiMocks.getEnvironmentOperationGroup).not.toHaveBeenCalledWith(
      "env-staging",
      "group-9",
      expect.anything(),
    );
  });

  it("keeps validation and mutation conflicts visible while review is closed", () => {
    changesetHook.data.changeset = {
      id: "chg-invalid",
      state: "draft",
      revision: 3,
      items: [{ id: "item", kind: "runtime", operation: "edit", service_id: "svc-api", name: "runtime", value_change: "configuration updated" }],
    };
    (changesetHook.data as Record<string, unknown>).validation = {
      valid: false,
      affected_services: ["svc-api"],
      errors: ["The saved revision conflicts with newer service state."],
      warnings: [],
    };
    commitMutation.error = new Error("revision conflict");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <StagedChangesBanner environmentId="env-production" />
      </QueryClientProvider>,
    );

    expect(screen.getByText("The saved revision conflicts with newer service state.")).toBeVisible();
    expect(screen.getByRole("button", { name: "Apply & deploy" })).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent("revision conflict");
  });
});
