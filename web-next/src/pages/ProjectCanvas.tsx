// Project resource workspace (`/projects/:projectId?env=&service=&tab=&view=`).
// Grid is the deterministic default and reads one environment summary. The
// draggable relationship canvas remains available as explicit `view=map`.
// Environment, service, drawer tab, and view stay in the URL for deep links.

import { useEffect, useMemo, useRef } from "react";
import { useNavigate } from "@tanstack/react-router";
import {
  useEnvironmentChangeset,
  useEnvironmentResources,
  useProject,
  useServices,
} from "@/api/hooks";
import { useAuth } from "@/auth/AuthProvider";
import type { EnvironmentServiceResource } from "@/api/client";
import { Canvas } from "@/components/Canvas";
import {
  ResourceGrid,
  type DraftOperation,
} from "@/components/ResourceGrid";
import {
  ServiceDetailDrawer,
  type ConfigureSection,
  type ServiceSection,
} from "@/components/ServiceDetailDrawer";
import { AddResourcePalette } from "@/components/panels/add-resource/AddResourcePalette";
import type { ResourceKind } from "@/components/panels/add-resource/api";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import { StagedChangesBanner } from "@/components/StagedChangesBanner";
import { Loading, Empty, ErrorState, ActionButton } from "@/components/states";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { setAgentDockCollapsed } from "@/components/AgentPanel/agent-dock-state";
import {
  clearAllStagedChanges,
  stagedChangeKind,
  useStagedForEnv,
} from "@/lib/stagedChanges";
import { actionLabel, type ProductActionID } from "@/lib/productActions";

type Props = {
  projectId: string;
  environmentId?: string;
  serviceId?: string;
  tab?: string;
  view?: "grid" | "map";
  section?: string;
  deploymentId?: string;
  configureSection?: string;
  resourceKind?: ResourceKind;
};

export function ProjectCanvas({
  projectId,
  environmentId,
  serviceId,
  tab,
  view,
  section,
  deploymentId,
  configureSection,
  resourceKind,
}: Props) {
  const navigate = useNavigate();
  const auth = useAuth();
  const mode = view === "map" ? "map" : "grid";
  const addingService = tab === "add-service" && !serviceId;
  const humanSession = auth.state.status === "human";
  const projectAccess = useProjectAccess(
    projectId,
    undefined,
    humanSession,
  );
  const environmentAccess = useProjectAccess(
    projectId,
    environmentId,
    humanSession && Boolean(environmentId),
  );
  const humanCanCreateService =
    projectAccess.data?.principal_kind === "human" &&
    projectAccess.data.project_id === projectId &&
    projectAccess.data.environment_id === undefined &&
    projectAccess.data?.permissions.includes("service.write") === true;
  const canCreateService = humanSession
    ? humanCanCreateService
    : auth.operatorFeaturesAvailable;
  const humanCanReadSelectedScope =
    !humanSession ||
    (environmentId
      ? environmentAccess.data?.principal_kind === "human" &&
        environmentAccess.data.project_id === projectId &&
        environmentAccess.data.environment_id === environmentId &&
        environmentAccess.data.permissions.includes("project.read") === true
      : projectAccess.data?.principal_kind === "human" &&
        projectAccess.data.project_id === projectId &&
        projectAccess.data.environment_id === undefined &&
        projectAccess.data.permissions.includes("project.read") === true);
  const installationEvidenceAvailable =
    auth.operatorFeaturesAvailable && humanCanReadSelectedScope;
  const selectedPermissions = environmentId
    ? environmentAccess.data?.permissions
    : projectAccess.data?.permissions;
  const drawerEvidenceAvailable =
    installationEvidenceAvailable &&
    (!humanSession ||
      selectedPermissions?.includes(
        requiredServiceEvidencePermission(section || tab),
      ) === true);
  const humanCanWriteSelectedScope =
    !humanSession ||
    (environmentId
      ? environmentAccess.data?.principal_kind === "human" &&
        environmentAccess.data.project_id === projectId &&
        environmentAccess.data.environment_id === environmentId &&
        environmentAccess.data.permissions.includes(
          requiredConfigurePermission(configureSection),
        ) === true
      : projectAccess.data?.principal_kind === "human" &&
        projectAccess.data.project_id === projectId &&
        projectAccess.data.environment_id === undefined &&
        projectAccess.data.permissions.includes(
          requiredConfigurePermission(configureSection),
        ) === true);
  const installationMutationAvailable =
    auth.operatorFeaturesAvailable && humanCanWriteSelectedScope;
  const stagedInputs = useStagedForEnv(environmentId);
  const environmentPermissions =
    environmentAccess.data?.principal_kind === "human" &&
    environmentAccess.data.project_id === projectId &&
    environmentAccess.data.environment_id === environmentId
      ? environmentAccess.data.permissions
      : [];
  const humanCanManageChangesetsInSelectedEnvironment =
    !humanSession ||
    (Boolean(environmentId) &&
      environmentPermissions.some((permission) =>
        [
          "variables.write",
          "service.write",
          "domains.write",
          "volume.write",
          "volume.delete",
        ].includes(permission),
      ));
  const canManageCurrentStagedInputs = stagedInputs.every((change) => {
    const required =
      stagedChangeKind(change) === "variable"
        ? "variables.write"
        : stagedChangeKind(change) === "runtime"
          ? "service.write"
          : stagedChangeKind(change) === "networking"
            ? "domains.write"
            : change.op === "remove"
              ? "volume.delete"
              : "volume.write";
    return environmentPermissions.includes(required);
  });
  const stagedChangesAvailable =
    humanSession && humanCanManageChangesetsInSelectedEnvironment;
  const project = useProject(projectId);
  const summary = useEnvironmentResources(environmentId, {
    enabled:
      humanCanReadSelectedScope &&
      Boolean(environmentId) &&
      !addingService,
  });
  const durableChanges = useEnvironmentChangeset(environmentId, {
    enabled:
      stagedChangesAvailable &&
      Boolean(environmentId) &&
      !addingService,
  });
  const services = useServices(projectId, environmentId, {
    enabled: mode === "map" && !addingService,
  });
  const gridResources = useMemo<EnvironmentServiceResource[]>(
    () => (humanCanReadSelectedScope ? (summary.data?.resources ?? []) : []),
    [humanCanReadSelectedScope, summary.data?.resources],
  );
  const gridPending =
    (humanSession && (projectAccess.isPending || environmentAccess.isPending)) ||
    summary.isPending;
  const gridError = summary.error;
  const serviceDraftOperations = useMemo(
    () => serviceDraftOperationsFrom(
      stagedInputs,
      durableChanges.data?.changeset?.state === "draft"
        ? durableChanges.data.changeset.items
        : [],
    ),
    [durableChanges.data?.changeset, stagedInputs],
  );
  const refetchGrid = () => {
    void summary.refetch();
  };

  useEffect(() => {
    if (
      auth.state.status !== "checking" &&
      (!stagedChangesAvailable || !canManageCurrentStagedInputs)
    ) {
      clearAllStagedChanges();
    }
  }, [
    auth.state.status,
    canManageCurrentStagedInputs,
    stagedChangesAvailable,
  ]);

  const previousServiceId = useRef(serviceId);
  useEffect(() => {
    const previous = previousServiceId.current;
    previousServiceId.current = serviceId;
    if (!previous || serviceId) return;
    window.requestAnimationFrame(() => {
      document
        .querySelector<HTMLElement>(`[data-service-open="${CSS.escape(previous)}"]`)
        ?.focus();
    });
  }, [serviceId]);

  const navigateToResources = (replace = false) =>
    navigate({
      to: "/projects/$projectId",
      params: { projectId },
      search: {
        env: environmentId,
        view: mode === "map" ? "map" : undefined,
      },
      replace,
    });

  useEffect(() => {
    if (!serviceId) return;
    setAgentDockCollapsed(true);
    const closeForAgent = (event: Event) => {
      if (event instanceof CustomEvent && event.detail === "agent") {
        void navigate({
          to: "/projects/$projectId",
          params: { projectId },
          search: {
            env: environmentId,
            view: mode === "map" ? "map" : undefined,
          },
          replace: true,
        });
      }
    };
    window.addEventListener("midnight:utility-pane-open", closeForAgent);
    return () =>
      window.removeEventListener(
        "midnight:utility-pane-open",
        closeForAgent,
      );
  }, [environmentId, mode, navigate, projectId, serviceId]);

  const navigateToService = (
    id: string,
    nextSection: ServiceSection,
    next?: { deployment?: string; configure?: ConfigureSection },
    replace = false,
  ) =>
    navigate({
      to: "/projects/$projectId/services/$serviceId/$section",
      params: {
        projectId,
        serviceId: id,
        section: nextSection,
      },
      search: {
        env: environmentId,
        view: mode === "map" ? "map" : undefined,
        deployment: next?.deployment,
        configure: next?.configure,
      },
      replace,
    } as never);

  const closeDrawer = () => navigateToResources(true);
  const selectService = (
    id: string,
    activation: "pointer" | "keyboard" = "pointer",
  ) => {
    if (activation === "keyboard") {
      try {
        window.sessionStorage.setItem(
          "midnight.service-inspector.focus",
          id,
        );
      } catch {
        // Storage is optional; the route remains keyboard accessible.
      }
    }
    void navigateToService(id, "overview");
  };
  const setSection = (nextSection: ServiceSection) => {
    if (!serviceId) return;
    void navigateToService(
      serviceId,
      nextSection,
      nextSection === "configure" && configureSection
        ? { configure: configureSection as ConfigureSection }
        : undefined,
    );
  };
  const setDeployment = (nextDeploymentId?: string) => {
    if (!serviceId) return;
    void navigateToService(serviceId, "deployments", {
      deployment: nextDeploymentId,
    });
  };
  const setConfigureSection = (nextConfigureSection: ConfigureSection) => {
    if (!serviceId) return;
    void navigateToService(serviceId, "configure", {
      configure: nextConfigureSection,
    });
  };
  const viewLogs = (id: string) => navigateToService(id, "logs");
  const setMode = (next: "grid" | "map") =>
    serviceId
      ? navigate({
          to: "/projects/$projectId/services/$serviceId/$section",
          params: {
            projectId,
            serviceId,
            section: (section || "overview") as ServiceSection,
          },
          search: {
            env: environmentId,
            deployment: deploymentId,
            configure: configureSection,
            view: next === "map" ? "map" : undefined,
          },
        } as never)
      : navigate({
          to: "/projects/$projectId",
          params: { projectId },
          search: {
            env: environmentId,
            tab,
            view: next === "map" ? "map" : undefined,
          },
        });

  const serviceLabels = Object.fromEntries(
    (mode === "grid"
      ? gridResources.map(({ service }) => service)
      : (services.data ?? [])
    ).map((service) => [
      service.id,
      service.display_name || service.slug || service.id,
    ]),
  );

  const projectNavHeight = "var(--project-nav-height)";
  const stageHeight = `calc(100dvh - var(--top-nav-height) - ${projectNavHeight} - (var(--main-gutter) * 2))`;

  return (
    <div
      className="project-workspace flex w-full flex-col gap-3 p-[var(--main-gutter)]"
      data-inspector-open={serviceId ? "true" : "false"}
      style={{
        height: `calc(100dvh - var(--top-nav-height) - ${projectNavHeight})`,
      }}
    >
      <header className="flex shrink-0 flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
            {mode === "grid" ? "Environment resources" : "Relationship map"}
          </p>
          <h1 className="m-0 mt-0.5 truncate text-lg font-semibold tracking-tight">
            {summary.data?.project.display_name ||
              summary.data?.project.slug ||
              project.data?.display_name ||
              project.data?.slug ||
              projectId}
          </h1>
        </div>
        <div className="flex min-w-0 items-center gap-3">
          {!addingService && canCreateService ? (
            <ActionButton
              actionId="resource.add"
              onClick={() =>
                navigate({
                  to: "/projects/$projectId",
                  params: { projectId },
                  search: { env: environmentId, tab: "add-service" },
                })
              }
            >
              + {actionLabel("resource.add")}
            </ActionButton>
          ) : null}
          <div
            className="inline-flex shrink-0 rounded-[var(--r-sm)] border border-line bg-surface-2 p-0.5"
            role="group"
            aria-label="Resource view"
          >
            <ViewButton actionId="topology.grid.open" active={mode === "grid"} onClick={() => setMode("grid")}>
              Grid
            </ViewButton>
            <ViewButton actionId="topology.map.open" active={mode === "map"} onClick={() => setMode("map")}>
              Map
            </ViewButton>
          </div>
          <span className="hidden shrink-0 font-mono text-[11px] text-subtle sm:inline">
            {projectId}
          </span>
        </div>
      </header>

      <div
        className="service-workspace min-h-0 flex-1"
        style={{ minHeight: stageHeight }}
      >
        <div className="service-workspace-primary relative min-h-0 overflow-hidden">
          {addingService ? (
            <div className="max-w-2xl">
              {humanSession && projectAccess.isPending ? (
                <CapabilityBoundary
                  state="degraded"
                  title="Service creation"
                  detail="Checking the current project role before enabling service creation."
                />
              ) : canCreateService ? (
                <AddResourcePalette
                  projectId={projectId}
                  environmentId={environmentId}
                  initialKind={resourceKind}
                  installationAdminAuthorized={installationEvidenceAvailable}
                  onClose={() => void navigateToResources(true)}
                  onCreated={(result) => {
                    if (result.serviceId) {
                      void navigateToService(result.serviceId, "overview");
                      return;
                    }
                    refetchGrid();
                    void navigateToResources(true);
                  }}
                />
              ) : (
                <CapabilityBoundary
                  state="unavailable"
                  title="Service creation"
                  detail={
                    projectAccess.error
                      ? "Midnight could not verify service-write permission for this project. No create request was sent."
                      : "The current project role does not grant service-write permission."
                  }
                />
              )}
            </div>
          ) : mode === "grid" && gridPending ? (
            <Loading label="Loading environment resources…" />
          ) : mode === "grid" && gridError ? (
            <ErrorState
              title="Failed to load environment resources"
              error={gridError}
              onRetry={refetchGrid}
            />
          ) : mode === "grid" && gridResources.length === 0 ? (
            <Empty
              title="No resources in this environment"
              detail="Add a resource to start the first service or deployment."
              action={
                canCreateService ? (
                  <ActionButton
                    actionId="resource.add"
                    onClick={() =>
                      navigate({
                        to: "/projects/$projectId",
                        params: { projectId },
                        search: { env: environmentId, tab: "add-service" },
                      })
                    }
                  >
                    + {actionLabel("resource.add")}
                  </ActionButton>
                ) : humanSession && !projectAccess.isPending ? (
                  <ActionButton onClick={() => navigate({ to: "/settings" })}>
                    Review project access
                  </ActionButton>
                ) : (
                  <span className="text-xs text-subtle">
                    Checking project access…
                  </span>
                )
              }
            />
          ) : mode === "grid" ? (
            <ResourceGrid
              resources={gridResources}
              serviceDraftOperations={serviceDraftOperations}
              selectedServiceId={serviceId}
              onSelectService={selectService}
              onViewLogs={viewLogs}
              onViewDeployments={(id) => navigateToService(id, "deployments")}
              onConfigureService={(id, configure) =>
                navigateToService(id, "configure", { configure })
              }
              deploymentEvidenceAvailable
              scopeLabel="in this environment"
            />
          ) : services.isPending ? (
            <Loading label="Loading services…" />
          ) : services.error ? (
            <ErrorState
              title="Failed to load services"
              error={services.error}
              onRetry={() => services.refetch()}
            />
          ) : (services.data ?? []).length === 0 ? (
            <Empty
              title="No resources in this project"
              detail={
                humanSession
                  ? "Add a resource to create the first project service."
                  : "Add a resource to see it appear on the canvas."
              }
              action={
                canCreateService ? (
                  <ActionButton actionId="resource.add" onClick={() => navigate({ to: "/projects/$projectId", params: { projectId }, search: { env: environmentId, tab: "add-service" } })}>
                    + {actionLabel("resource.add")}
                  </ActionButton>
                ) : humanSession && !projectAccess.isPending ? (
                  <ActionButton onClick={() => navigate({ to: "/settings" })}>
                    Review project access
                  </ActionButton>
                ) : (
                  <span className="text-xs text-subtle">
                    Checking project access…
                  </span>
                )
              }
            />
          ) : (
            <Canvas
              services={services.data ?? []}
              selectedServiceId={serviceId}
              environmentId={environmentId}
              onSelectService={selectService}
            />
          )}
        </div>
        <ServiceDetailDrawer
          serviceId={serviceId}
          projectId={projectId}
          environmentId={environmentId}
          section={section || tab}
          deploymentId={deploymentId}
          configureSection={configureSection}
          installationAdminAuthorized={drawerEvidenceAvailable}
          installationAdminMutationAuthorized={installationMutationAvailable}
          onSectionChange={setSection}
          onDeploymentChange={setDeployment}
          onConfigureSectionChange={setConfigureSection}
          open={Boolean(serviceId)}
          onOpenChange={(open) => {
            if (!open) closeDrawer();
          }}
        />

        {/* One environment Changes journey and a visually separate Activity
            indicator. The reviewed durable revision can be applied and queued
            for affected-service deployment as one idempotent operation. */}
        {stagedChangesAvailable ? (
          <StagedChangesBanner
            projectId={projectId}
            environmentId={environmentId}
            serviceLabels={serviceLabels}
            operations={gridResources.flatMap((resource) => {
              const deployment = resource.latest_deployment;
              if (!deployment?.operation) return [];
              return [{
                serviceId: resource.service.id,
                serviceLabel:
                  resource.service.display_name ||
                  resource.service.slug ||
                  resource.service.id,
                deployment: {
                  deployment_id: deployment.id,
                  operation_id: deployment.operation.id,
                  operation_state: deployment.operation.state,
                  last_event:
                    deployment.operation.failure_reason || deployment.status,
                },
              }];
            })}
            onViewService={(id) => navigateToService(id, "overview")}
            onViewDeployment={(id, idDeployment) =>
              navigateToService(id, "deployments", {
                deployment: idDeployment,
              })
            }
          />
        ) : null}
      </div>
    </div>
  );
}

export function serviceDraftOperationsFrom(
  local: Array<{ serviceId: string; op: DraftOperation }>,
  durable: Array<{ service_id: string; operation: DraftOperation }>,
): Record<string, DraftOperation[]> {
  const byService = new Map<string, Set<DraftOperation>>();
  for (const change of local) {
    const operations = byService.get(change.serviceId) ?? new Set<DraftOperation>();
    // These are resource/configuration mutations on an existing service. A
    // variable or volume add/remove edits the service; it does not create or
    // remove the service lifecycle itself.
    operations.add("edit");
    byService.set(change.serviceId, operations);
  }
  for (const item of durable) {
    const operations = byService.get(item.service_id) ?? new Set<DraftOperation>();
    operations.add("edit");
    byService.set(item.service_id, operations);
  }
  const order: DraftOperation[] = ["add", "edit", "remove"];
  return Object.fromEntries(
    Array.from(byService, ([serviceId, operations]) => [
      serviceId,
      order.filter((operation) => operations.has(operation)),
    ]),
  );
}

export function requiredServiceEvidencePermission(
  section?: string,
): "service.read" | "logs.read" | "metrics.read" {
  if (section === "logs") return "logs.read";
  if (section === "metrics") return "metrics.read";
  return "service.read";
}

export function requiredConfigurePermission(
  section?: string,
): "variables.write" | "domains.write" | "volume.write" | "service.write" {
  if (section === "networking") return "domains.write";
  if (section === "storage") return "volume.write";
  if (section === "runtime" || section === "source") return "service.write";
  return "variables.write";
}

function ViewButton({
  actionId,
  active,
  onClick,
  children,
}: {
  actionId: ProductActionID;
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      data-action-id={actionId}
      className={
        "rounded px-2.5 py-1 text-xs font-semibold transition-colors " +
        (active
          ? "bg-surface-3 text-text shadow-sm"
          : "text-subtle hover:text-text")
      }
      aria-pressed={active}
      onClick={onClick}
    >
      {children}
    </button>
  );
}
