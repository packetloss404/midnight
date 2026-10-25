// Route-driven service inspector. The same markup is a non-modal split pane on
// desktop and a full-width detail page on mobile.

import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { Drawer } from "./Drawer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./Tabs";
import {
  useDeployments,
  useEnvironmentChangeset,
  useEnvironmentResources,
  useService,
} from "@/api/hooks";
import { useAuth, type AuthState } from "@/auth/AuthProvider";
import type {
  DeploymentResource,
  EnvironmentServiceResource,
} from "@/api/client";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { ActionButton, ErrorState, Loading } from "@/components/states";
import { StatusPill } from "@/components/StatusPill";
import { DeploymentList } from "./panels/DeploymentList";
import { DeploymentDetail } from "./panels/DeploymentDetail";
import { LogViewer } from "./panels/LogViewer";
import { VariablesEditor } from "./panels/VariablesEditor";
import { TenantVariablesEditor } from "./panels/variables/TenantVariablesEditor";
import { MetricsPanel } from "./panels/MetricsPanel";
import { ServiceOverview } from "./panels/ServiceOverview";
import { ServiceSettings } from "./panels/ServiceSettings";
import { VolumeRecoveryPanel } from "./panels/VolumeRecoveryPanel";
import { ServiceSourcePanel } from "./panels/github/ServiceSourcePanel";
import { useStagedForEnv } from "@/lib/stagedChanges";
import {
  searchConfigureSettings,
  type ConfigureSetting,
} from "./configureSettingsRegistry";

export const SERVICE_SECTIONS = [
  "overview",
  "deployments",
  "logs",
  "metrics",
  "configure",
] as const;
export type ServiceSection = (typeof SERVICE_SECTIONS)[number];

export const CONFIGURE_SECTIONS = [
  "source",
  "variables",
  "networking",
  "storage",
  "runtime",
] as const;
export type ConfigureSection = (typeof CONFIGURE_SECTIONS)[number];

export function normalizeServiceSection(value?: string): ServiceSection {
  return (SERVICE_SECTIONS as readonly string[]).includes(value ?? "")
    ? (value as ServiceSection)
    : "overview";
}

export function normalizeConfigureSection(value?: string): ConfigureSection {
  return (CONFIGURE_SECTIONS as readonly string[]).includes(value ?? "")
    ? (value as ConfigureSection)
    : "variables";
}

const CONFIGURE_PREFERENCE_PREFIX = "midnight.configure-section";

export function defaultConfigureSection(input: {
  requested?: string;
  serviceId: string;
  environmentId?: string;
  hasSource?: boolean;
}): ConfigureSection {
  if ((CONFIGURE_SECTIONS as readonly string[]).includes(input.requested ?? "")) {
    return input.requested as ConfigureSection;
  }
  if (input.hasSource === false) return "source";
  return readConfigureSectionPreference(input.serviceId, input.environmentId) ?? "variables";
}

export function readConfigureSectionPreference(
  serviceId: string,
  environmentId?: string,
): ConfigureSection | undefined {
  try {
    const value = window.localStorage.getItem(
      configurePreferenceKey(serviceId, environmentId),
    );
    return (CONFIGURE_SECTIONS as readonly string[]).includes(value ?? "")
      ? (value as ConfigureSection)
      : undefined;
  } catch {
    return undefined;
  }
}

export function rememberConfigureSection(
  serviceId: string,
  environmentId: string | undefined,
  section: ConfigureSection,
) {
  try {
    window.localStorage.setItem(
      configurePreferenceKey(serviceId, environmentId),
      section,
    );
  } catch {
    // Deep links continue to work when browser storage is unavailable.
  }
}

export function serviceDetailQueryPolicy(
  section: ServiceSection,
  summaryEvidenceAvailable: boolean,
): { summary: boolean; deployments: boolean } {
  return {
    summary:
      summaryEvidenceAvailable &&
      (section === "overview" || section === "configure"),
    deployments:
      section === "deployments" ||
      section === "logs" ||
      (!summaryEvidenceAvailable && section === "overview"),
  };
}

type Props = {
  serviceId: string | undefined;
  projectId?: string;
  environmentId?: string;
  section?: string;
  deploymentId?: string;
  configureSection?: string;
  installationAdminAuthorized?: boolean;
  installationAdminMutationAuthorized?: boolean;
  onSectionChange?: (section: ServiceSection) => void;
  onDeploymentChange?: (deploymentId?: string) => void;
  onConfigureSectionChange?: (section: ConfigureSection) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

export function ServiceDetailDrawer({
  serviceId,
  projectId,
  environmentId,
  section,
  deploymentId,
  configureSection,
  installationAdminAuthorized,
  installationAdminMutationAuthorized,
  onSectionChange,
  onDeploymentChange,
  onConfigureSectionChange,
  open,
  onOpenChange,
}: Props) {
  const service = useService(serviceId);
  const inspectorRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!open || !serviceId) return;
    const focusKey = "midnight.service-inspector.focus";
    let shouldFocus = false;
    try {
      shouldFocus = window.sessionStorage.getItem(focusKey) === serviceId;
      window.sessionStorage.removeItem(focusKey);
    } catch {
      // Storage is optional; pointer activation still remains non-modal.
    }
    if (shouldFocus) {
      inspectorRef.current
        ?.querySelector<HTMLElement>("#service-inspector-title")
        ?.focus();
    }
  }, [open, serviceId]);

  useEffect(() => {
    if (!open) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      onOpenChange(false);
    };
    document.addEventListener("keydown", closeOnEscape);
    return () => document.removeEventListener("keydown", closeOnEscape);
  }, [onOpenChange, open]);

  return (
    <Drawer
      ref={inspectorRef}
      open={open}
      onOpenChange={onOpenChange}
      title={service.data?.display_name ?? service.data?.slug ?? "Service"}
      subtitle={
        service.data ? (
          <span className="font-mono">
            {service.data.slug} · {environmentId || "environment not selected"}
          </span>
        ) : (
          "Loading service…"
        )
      }
    >
      {serviceId ? (
        <ServiceDetailBody
          serviceId={serviceId}
          projectId={projectId}
          environmentId={environmentId}
          section={normalizeServiceSection(section)}
          deploymentId={deploymentId}
          configureSection={configureSection}
          installationAdminAuthorized={installationAdminAuthorized}
          installationAdminMutationAuthorized={installationAdminMutationAuthorized}
          onSectionChange={onSectionChange}
          onDeploymentChange={onDeploymentChange}
          onConfigureSectionChange={onConfigureSectionChange}
        />
      ) : (
        <div className="p-6 text-muted">No service selected.</div>
      )}
    </Drawer>
  );
}

function ServiceDetailBody({
  serviceId,
  projectId,
  environmentId,
  section,
  deploymentId,
  configureSection,
  installationAdminAuthorized,
  installationAdminMutationAuthorized,
  onSectionChange,
  onDeploymentChange,
  onConfigureSectionChange,
}: {
  serviceId: string;
  projectId?: string;
  environmentId?: string;
  section: ServiceSection;
  deploymentId?: string;
  configureSection?: string;
  installationAdminAuthorized?: boolean;
  installationAdminMutationAuthorized?: boolean;
  onSectionChange?: (section: ServiceSection) => void;
  onDeploymentChange?: (deploymentId?: string) => void;
  onConfigureSectionChange?: (section: ConfigureSection) => void;
}) {
  const auth = useAuth();
  const {
    humanSession,
    operatorEvidenceAvailable,
    operatorMutationAvailable,
  } = serviceDetailAuthority(
    auth.state.status,
    auth.operatorFeaturesAvailable,
    installationAdminAuthorized,
    installationAdminMutationAuthorized,
  );
  const summaryEvidenceAvailable = humanSession || operatorEvidenceAvailable;
  const queryPolicy = serviceDetailQueryPolicy(
    section,
    summaryEvidenceAvailable,
  );
  const summary = useEnvironmentResources(environmentId, {
    enabled: queryPolicy.summary && Boolean(environmentId),
  });
  const deployments = useDeployments(serviceId, environmentId, {
    enabled: queryPolicy.deployments,
  });
  const resource = summary.data?.resources.find(
    (item) => item.service.id === serviceId,
  );
  const selectedConfigureSection = defaultConfigureSection({
    requested: configureSection,
    serviceId,
    environmentId,
    hasSource: summary.data ? resourceHasSource(resource) : undefined,
  });
  const changeConfigureSection = (next: ConfigureSection) => {
    rememberConfigureSection(serviceId, environmentId, next);
    onConfigureSectionChange?.(next);
  };
  const selectedDeployment = useMemo(
    () => deployments.data?.find((item) => deploymentIdentity(item) === deploymentId),
    [deploymentId, deployments.data],
  );
  const logDeployment =
    selectedDeployment ??
    deployments.data?.find((item) => item.active) ??
    deployments.data?.[0];

  return (
    <Tabs
      value={section}
      onValueChange={(value) =>
        onSectionChange?.(normalizeServiceSection(value))
      }
      className="flex h-full flex-col"
    >
      <label className="service-section-select border-b border-line bg-surface px-4 py-3">
        <span className="mb-1 block font-mono text-[10px] uppercase tracking-wider text-muted">
          Service section
        </span>
        <select
          value={section}
          onChange={(event) =>
            onSectionChange?.(normalizeServiceSection(event.target.value))
          }
          className="field-input"
        >
          {SERVICE_SECTIONS.map((value) => (
            <option key={value} value={value}>
              {sectionLabel(value)}
            </option>
          ))}
        </select>
      </label>

      <TabsList className="service-section-tabs bg-surface">
        <TabsTrigger value="overview">
          Overview <ServiceShortcutHint suffix="O" />
        </TabsTrigger>
        <TabsTrigger value="deployments">
          Deployments <ServiceShortcutHint suffix="D" />
        </TabsTrigger>
        <TabsTrigger value="logs">
          Logs <ServiceShortcutHint suffix="L" />
        </TabsTrigger>
        <TabsTrigger value="metrics">
          Metrics <ServiceShortcutHint suffix="M" />
        </TabsTrigger>
        <TabsTrigger value="configure">
          Configure <ServiceShortcutHint suffix="C" />
        </TabsTrigger>
      </TabsList>

      <div className="min-h-0 flex-1 overflow-auto">
        <TabsContent value="overview" className="p-5">
          {!summaryEvidenceAvailable ? (
            <TenantServiceSetupOverview
              deployments={deployments.data}
              error={deployments.error}
              isPending={deployments.isPending}
              onRetry={() => deployments.refetch()}
              onViewDeployments={() => onSectionChange?.("deployments")}
            />
          ) : summary.isPending ? (
            <Loading label="Loading service overview…" />
          ) : summary.error ? (
            <ErrorState
              title="Failed to load service overview"
              error={summary.error}
              onRetry={() => summary.refetch()}
            />
          ) : (
            <ServiceOverview
              resource={resource}
              environmentName={
                summary.data?.environment.name ||
                summary.data?.environment.slug
              }
              onViewDeployments={() => onSectionChange?.("deployments")}
              onViewLogs={() => onSectionChange?.("logs")}
              onConfigure={(configureSection) => {
                changeConfigureSection(configureSection);
                onSectionChange?.("configure");
              }}
            />
          )}
        </TabsContent>

        <TabsContent value="deployments" className="p-5">
          {deploymentId ? (
            deployments.isPending ? (
              <Loading label="Loading deployment…" />
            ) : selectedDeployment ? (
              <DeploymentDetail
                deployment={selectedDeployment}
                environmentId={environmentId}
                onBack={() => onDeploymentChange?.()}
              />
            ) : (
              <div
                role="alert"
                className="grid gap-2 rounded-lg border border-danger/40 bg-danger-soft px-4 py-4 text-sm"
              >
                <strong className="text-danger">Deployment not found</strong>
                <span className="text-muted">
                  This service has no deployment{" "}
                  <span className="font-mono">{deploymentId}</span>.
                </span>
                <button
                  type="button"
                  onClick={() => onDeploymentChange?.()}
                  className="w-fit rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-3 py-1.5 text-xs font-semibold text-text"
                >
                  Return to timeline
                </button>
              </div>
            )
          ) : (
            <DeploymentList
              serviceId={serviceId}
              environmentId={environmentId}
              onSelect={(deployment) =>
                onDeploymentChange?.(deploymentIdentity(deployment))
              }
            />
          )}
        </TabsContent>

        <TabsContent value="logs" className="p-5">
          <CapabilityBoundary
            state={logDeployment?.operation_id ? "degraded" : "unavailable"}
            title={
              operatorEvidenceAvailable
                ? "Deployment and runtime logs"
                : "Deployment logs"
            }
            detail={
              logDeployment?.operation_id
                ? operatorEvidenceAvailable
                  ? "Build and deploy logs and running-container logs are separate sources. Each one says whether it is available."
                  : "Stored and live logs for this exact deployment, covering build, runtime, HTTP, and platform records. Logs streamed from the running container need a separate permission."
                : "There is no deployment yet, so there are no logs to show."
            }
          >
            {logDeployment?.operation_id ? (
              <LogViewer
                operationId={logDeployment.operation_id}
                follow
                height={420}
                scope={
                  operatorEvidenceAvailable && projectId
                    ? {
                        projectId,
                        environmentId,
                        serviceId,
                        deploymentId: deploymentIdentity(logDeployment),
                        operationId: logDeployment.operation_id,
                      }
                    : undefined
                }
              />
            ) : null}
          </CapabilityBoundary>
        </TabsContent>

        <TabsContent value="metrics" className="p-5">
          <CapabilityBoundary
            state={projectId ? "available" : "degraded"}
            title="Runtime signals"
            detail={
              projectId
                ? operatorEvidenceAvailable
                  ? "Live runtime inventory plus tenant-authorized historical Docker CPU, memory, network, block I/O, and writable-layer disk telemetry."
                  : "Tenant-authorized historical Docker CPU, memory, network, block I/O, and writable-layer disk telemetry. Live fleet inventory remains outside this project session."
                : "Open this service from inside a project to see its recorded history."
            }
          >
            <MetricsPanel
              projectId={projectId}
              serviceId={serviceId}
              environmentId={environmentId}
              runtimeInventoryAvailable={operatorEvidenceAvailable}
            />
          </CapabilityBoundary>
        </TabsContent>

        <TabsContent value="configure" className="p-5">
          {operatorMutationAvailable ? (
            <ConfigurePanel
              serviceId={serviceId}
              projectId={projectId}
              environmentId={environmentId}
              section={selectedConfigureSection}
              onSectionChange={changeConfigureSection}
              authority={humanSession ? "human" : "operator"}
            />
          ) : humanSession ? (
            <HumanServiceConfigure
              serviceId={serviceId}
              projectId={projectId}
              environmentId={environmentId}
              section={selectedConfigureSection}
              onSectionChange={changeConfigureSection}
            />
          ) : (
            <CapabilityBoundary
              state="unavailable"
              title="Service configuration"
              detail="Tenant-scoped configuration mutations are not installed yet. No operator-only request was sent."
            />
          )}
        </TabsContent>
      </div>
    </Tabs>
  );
}

export function serviceDetailAuthority(
  status: AuthState["status"],
  operatorFeaturesAvailable: boolean,
  resourceAuthorized?: boolean,
  mutationAuthorized?: boolean,
): {
  humanSession: boolean;
  operatorEvidenceAvailable: boolean;
  operatorMutationAvailable: boolean;
} {
  const humanSession = status === "human";
  const resourceAdmission = resourceAuthorized ?? !humanSession;
  const mutationAdmission =
    mutationAuthorized ?? (!humanSession && resourceAdmission);
  const operatorEvidenceAvailable =
    operatorFeaturesAvailable && resourceAdmission;
  return {
    humanSession,
    operatorEvidenceAvailable,
    operatorMutationAvailable:
      operatorEvidenceAvailable && mutationAdmission,
  };
}

function TenantServiceSetupOverview({
  deployments,
  error,
  isPending,
  onRetry,
  onViewDeployments,
}: {
  deployments?: DeploymentResource[];
  error: Error | null;
  isPending: boolean;
  onRetry: () => void;
  onViewDeployments: () => void;
}) {
  if (isPending) {
    return <Loading label="Checking deployment history…" />;
  }
  if (error) {
    return (
      <ErrorState
        title="Failed to load service setup"
        error={error}
        onRetry={onRetry}
      />
    );
  }

  const latest = deployments?.[0];
  return (
    <div className="grid gap-4">
      <section className="grid gap-3 rounded-lg border border-line bg-surface-2 px-4 py-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <p className="m-0 font-mono text-[10px] uppercase tracking-wider text-muted">
              Tenant-safe setup
            </p>
            <h2 className="m-0 mt-1 text-base font-semibold text-text">
              {latest
                ? "Deployment history reported"
                : "No deployment reported"}
            </h2>
          </div>
          {latest ? (
            <StatusPill
              signal={{
                status: latest.status,
                operation_state: latest.operation_state,
                active: latest.active,
              }}
            />
          ) : (
            <span className="status-pill" data-tone="neutral" role="status">
              no deployment reported
            </span>
          )}
        </div>

        <ol className="m-0 grid list-none gap-2 p-0 text-sm">
          <SetupStep
            label="Service record"
            value="Visible to this session"
            complete
          />
          <SetupStep
            label="Source"
            value="Manage a workspace-granted GitHub source in Configure"
          />
          <SetupStep
            label="Deployment"
            value={
              latest
                ? `${latest.deployment_id || latest.id} · ${
                    latest.operation_state || latest.status || "state not reported"
                  }`
                : "No deployment reported"
            }
            complete={Boolean(latest)}
          />
        </ol>

        {latest ? (
          <ActionButton onClick={onViewDeployments}>
            View deployment history
          </ActionButton>
        ) : null}
      </section>

      <CapabilityBoundary
        state="degraded"
        title="Source and deployment controls"
        detail="A permitted human can bind a workspace-granted GitHub source without starting a deployment, create a service, queue its first immutable-image deployment, and use tenant-coordinated redeploy, rollback, restart, stop, or remove actions from deployment history. Routes and other configuration still have separate authority gates; no operator-only request is used."
      />
    </div>
  );
}

function SetupStep({
  label,
  value,
  complete = false,
}: {
  label: string;
  value: string;
  complete?: boolean;
}) {
  return (
    <li className="flex items-start gap-2 rounded border border-line bg-bg-2 px-3 py-2">
      <span
        className={complete ? "text-success" : "text-subtle"}
        aria-hidden="true"
      >
        {complete ? "✓" : "○"}
      </span>
      <span className="grid gap-0.5">
        <strong className="text-xs text-text">{label}</strong>
        <span className="text-xs text-muted">{value}</span>
      </span>
    </li>
  );
}

export function HumanServiceSourceConfigure({
  serviceId,
  projectId,
}: {
  serviceId: string;
  projectId?: string;
}) {
  if (!projectId) {
    return (
      <CapabilityBoundary
        state="unavailable"
        title="GitHub source"
        detail="A project scope is required before Midnight can list workspace-granted repositories. No global installation lookup was sent."
      />
    );
  }
  return <ServiceSourcePanel projectId={projectId} serviceId={serviceId} />;
}

export function HumanServiceConfigure({
  serviceId,
  projectId,
  environmentId,
  section = "variables",
  onSectionChange,
}: {
  serviceId: string;
  projectId?: string;
  environmentId?: string;
  section?: ConfigureSection;
  onSectionChange?: (section: ConfigureSection) => void;
}) {
  return (
    <div className="grid gap-4">
      <ConfigurationLifecycle environmentId={environmentId} />
      <ConfigureSectionNavigation
        section={section}
        onSectionChange={onSectionChange}
      />

      {section === "source" ? (
        <HumanServiceSourceConfigure
          projectId={projectId}
          serviceId={serviceId}
        />
      ) : section === "variables" ? (
        projectId && environmentId ? (
          <TenantVariablesEditor
            projectId={projectId}
            serviceId={serviceId}
            environmentId={environmentId}
          />
        ) : (
          <CapabilityBoundary
            state="unavailable"
            title="Service variables"
            detail="A concrete project and environment are required before Midnight can load variable metadata. No variable request was sent."
          />
        )
      ) : section === "networking" ? (
        <ServiceSettings
          authority="human"
          projectId={projectId}
          serviceId={serviceId}
          environmentId={environmentId}
          mode="networking"
        />
      ) : section === "storage" ? (
        <VolumeRecoveryPanel
          projectId={projectId}
          serviceId={serviceId}
          environmentId={environmentId}
          recoveryAvailable={false}
        />
      ) : (
        <ServiceSettings
          authority="human"
          projectId={projectId}
          serviceId={serviceId}
          environmentId={environmentId}
          mode="runtime"
        />
      )}
    </div>
  );
}

export function ConfigurePanel({
  serviceId,
  projectId,
  environmentId,
  section,
  onSectionChange,
  authority = "operator",
}: {
  serviceId: string;
  projectId?: string;
  environmentId?: string;
  section: ConfigureSection;
  onSectionChange?: (section: ConfigureSection) => void;
  authority?: "human" | "operator";
}) {
  return (
    <div className="grid gap-4">
      <ConfigurationLifecycle environmentId={environmentId} />
      <ConfigureSectionNavigation
        section={section}
        onSectionChange={onSectionChange}
      />

      {section === "source" ? (
        <CapabilityBoundary
          state="unavailable"
          title="Source connection"
          detail="Connecting a source requires signing in as a workspace member. Emergency recovery access can't do this on your behalf."
        />
      ) : section === "variables" && authority === "human" ? (
        projectId && environmentId ? (
          <TenantVariablesEditor
            projectId={projectId}
            serviceId={serviceId}
            environmentId={environmentId}
          />
        ) : (
          <CapabilityBoundary
            state="unavailable"
            title="Service variables"
            detail="A concrete project and environment are required before Midnight can load variable metadata. No variable request was sent."
          />
        )
      ) : section === "variables" ? (
        <CapabilityBoundary
          state="degraded"
          title="Variables"
          detail="Saved values persist on the server. Non-secret edits autosave into Changes; sensitive values wait for explicit confirmation. Review the saved revision, then apply and deploy affected services together."
        >
          <VariablesEditor
            serviceId={serviceId}
            projectId={projectId}
            environmentId={environmentId}
          />
        </CapabilityBoundary>
      ) : section === "networking" ? (
        <ServiceSettings
          authority={authority}
          projectId={projectId}
          serviceId={serviceId}
          environmentId={environmentId}
          mode="networking"
        />
      ) : section === "storage" ? (
        <VolumeRecoveryPanel
          projectId={projectId}
          serviceId={serviceId}
          environmentId={environmentId}
        />
      ) : (
        <ServiceSettings
          authority={authority}
          projectId={projectId}
          serviceId={serviceId}
          environmentId={environmentId}
          mode="runtime"
        />
      )}
    </div>
  );
}

function ConfigurationLifecycle({
  environmentId,
}: {
  environmentId?: string;
}) {
  const edits = useStagedForEnv(environmentId);
  const draft = useEnvironmentChangeset(environmentId);
  const changeset = draft.data?.changeset;
  const state = edits.length > 0
    ? `${edits.length} edit${edits.length === 1 ? "" : "s"} in Changes`
    : changeset
      ? `Saved for review · revision ${changeset.revision}`
      : "Ready to edit";

  return (
    <section
      aria-label="Configuration workflow"
      className="grid gap-2 rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-2.5"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <strong className="text-xs text-text">Configuration workflow</strong>
        <span className="rounded-full border border-line bg-surface px-2 py-0.5 text-[10px] font-semibold text-muted">
          {state}
        </span>
      </div>
      <ol className="m-0 flex flex-wrap items-center gap-1 p-0 text-[11px] text-muted" aria-label="Configuration stages">
        {[
          "Edit",
          "Review changes",
          "Apply & deploy",
        ].map((label, index) => (
          <li key={label} className="flex items-center gap-1">
            {index > 0 ? <span aria-hidden>→</span> : null}
            <span>{label}</span>
          </li>
        ))}
      </ol>
      <p className="m-0 text-[11px] text-subtle">
        Apply without deploying remains available under Advanced for maintenance windows.
      </p>
    </section>
  );
}

function ConfigureSectionNavigation({
  section,
  onSectionChange,
}: {
  section: ConfigureSection;
  onSectionChange?: (section: ConfigureSection) => void;
}) {
  const [query, setQuery] = useState("");
  const [activeResult, setActiveResult] = useState(0);
  const [pendingFocus, setPendingFocus] = useState<ConfigureSetting>();
  const [focusAnnouncement, setFocusAnnouncement] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);
  const navigationRef = useRef<HTMLDivElement>(null);
  const results = useMemo(
    () => searchConfigureSettings(query).slice(0, 8),
    [query],
  );

  useEffect(() => {
    setActiveResult((current) =>
      Math.min(current, Math.max(0, results.length - 1)),
    );
  }, [results.length]);

  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if (
        event.key !== "/" ||
        event.metaKey ||
        event.ctrlKey ||
        event.altKey ||
        isTextEntry(event.target)
      ) {
        return;
      }
      event.preventDefault();
      searchRef.current?.focus();
    };
    window.addEventListener("keydown", focusSearch);
    return () => window.removeEventListener("keydown", focusSearch);
  }, []);

  useEffect(() => {
    if (!pendingFocus || pendingFocus.section !== section) return;
    const frame = requestAnimationFrame(() => {
      const scope = navigationRef.current?.parentElement;
      const control = scope
        ? findSettingControl(scope, pendingFocus.controlLabels)
        : undefined;
      const fallback = navigationRef.current?.querySelector<HTMLElement>(
        `[data-configure-section="${pendingFocus.section}"]`,
      );
      (control ?? fallback)?.focus();
      (control ?? fallback)?.scrollIntoView?.({ block: "center" });
      setFocusAnnouncement(
        control
          ? `Focused ${pendingFocus.label}.`
          : `${pendingFocus.label} is in ${sectionLabel(pendingFocus.section)}. Its control is not available yet.`,
      );
      setPendingFocus(undefined);
    });
    return () => cancelAnimationFrame(frame);
  }, [pendingFocus, section]);

  function chooseSetting(setting: ConfigureSetting) {
    setPendingFocus(setting);
    setQuery("");
    onSectionChange?.(setting.section);
  }

  function move(
    event: ReactKeyboardEvent<HTMLButtonElement>,
    current: ConfigureSection,
  ) {
    const direction =
      event.key === "ArrowRight" || event.key === "ArrowDown"
        ? 1
        : event.key === "ArrowLeft" || event.key === "ArrowUp"
          ? -1
          : 0;
    if (direction === 0) return;
    event.preventDefault();
    const index = CONFIGURE_SECTIONS.indexOf(current);
    const next = CONFIGURE_SECTIONS[
      (index + direction + CONFIGURE_SECTIONS.length) %
        CONFIGURE_SECTIONS.length
    ];
    onSectionChange?.(next);
    event.currentTarget.parentElement
      ?.querySelector<HTMLButtonElement>(`[data-configure-section="${next}"]`)
      ?.focus();
  }

  return (
    <div ref={navigationRef} className="grid gap-3">
      <div className="relative grid gap-1.5">
        <label htmlFor="configure-settings-search" className="text-xs font-semibold text-text">
          Search settings
        </label>
        <div className="relative">
          <input
            ref={searchRef}
            id="configure-settings-search"
            type="search"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setActiveResult(0);
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape" && query) {
                event.preventDefault();
                event.stopPropagation();
                setQuery("");
                return;
              }
              if (event.key === "ArrowDown" && results.length > 0) {
                event.preventDefault();
                setActiveResult((current) => (current + 1) % results.length);
                return;
              }
              if (event.key === "ArrowUp" && results.length > 0) {
                event.preventDefault();
                setActiveResult(
                  (current) => (current - 1 + results.length) % results.length,
                );
                return;
              }
              if (event.key === "Enter" && results[activeResult]) {
                event.preventDefault();
                chooseSetting(results[activeResult]);
              }
            }}
            placeholder="Health checks, replicas, domains…"
            className="field-input pr-12"
            role="combobox"
            aria-expanded={Boolean(query.trim())}
            aria-controls="configure-settings-results"
            aria-activedescendant={
              results[activeResult]
                ? `configure-setting-${results[activeResult].id}`
                : undefined
            }
          />
          <kbd className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 rounded border border-line px-1.5 py-0.5 font-mono text-[10px] text-subtle">
            /
          </kbd>
        </div>
        <span className="sr-only" role="status" aria-live="polite">
          {query.trim()
            ? `${results.length} matching setting${results.length === 1 ? "" : "s"}`
            : ""}
        </span>
        {query.trim() ? (
          <div
            id="configure-settings-results"
            role="listbox"
            aria-label="Matching settings"
            className="grid max-h-64 gap-1 overflow-auto rounded-lg border border-line bg-surface p-1 shadow-lg"
          >
            {results.length === 0 ? (
              <p className="m-0 px-3 py-4 text-center text-xs text-muted">
                No settings match “{query.trim()}”.
              </p>
            ) : (
              results.map((setting, index) => (
                <button
                  key={setting.id}
                  id={`configure-setting-${setting.id}`}
                  type="button"
                  role="option"
                  aria-selected={index === activeResult}
                  onMouseEnter={() => setActiveResult(index)}
                  onClick={() => chooseSetting(setting)}
                  className={[
                    "grid gap-0.5 rounded-md px-3 py-2 text-left",
                    index === activeResult ? "bg-accent-soft" : "hover:bg-bg-2",
                  ].join(" ")}
                >
                  <span className="flex items-center justify-between gap-3">
                    <strong className="text-xs text-text">{setting.label}</strong>
                    <span className="font-mono text-[10px] uppercase tracking-wider text-accent">
                      {sectionLabel(setting.section)}
                    </span>
                  </span>
                  <span className="text-[11px] text-muted">{setting.description}</span>
                </button>
              ))
            )}
          </div>
        ) : null}
      </div>
      <span className="sr-only" role="status" aria-live="polite">
        {focusAnnouncement}
      </span>

      <label className="grid gap-1 text-xs text-muted sm:hidden">
        Configure section
        <select
          value={section}
          onChange={(event) =>
            onSectionChange?.(normalizeConfigureSection(event.target.value))
          }
          className="field-input"
        >
          {CONFIGURE_SECTIONS.map((value) => (
            <option key={value} value={value}>
              {sectionLabel(value)}
            </option>
          ))}
        </select>
      </label>
      <div
        role="tablist"
        aria-label="Configure sections"
        className="hidden flex-wrap gap-1 rounded-[var(--r-sm)] border border-line bg-bg-2 p-1 sm:flex"
      >
        {CONFIGURE_SECTIONS.map((value) => (
          <button
            key={value}
            type="button"
            role="tab"
            aria-selected={section === value}
            tabIndex={section === value ? 0 : -1}
            data-configure-section={value}
            onKeyDown={(event) => move(event, value)}
            onClick={() => onSectionChange?.(value)}
            className={
              "rounded px-2.5 py-1.5 text-xs font-semibold " +
              (section === value
                ? "bg-surface-3 text-text"
                : "text-muted hover:text-text")
            }
          >
            {sectionLabel(value)}
          </button>
        ))}
      </div>
    </div>
  );
}

function deploymentIdentity(deployment: DeploymentResource): string {
  return deployment.deployment_id || deployment.id;
}

function ServiceShortcutHint({ suffix }: { suffix: string }) {
  return (
    <kbd
      aria-hidden="true"
      title={`Shortcut: G then ${suffix}`}
      className="ml-1 hidden rounded border border-line px-1 py-0.5 font-mono text-[9px] font-normal text-subtle xl:inline"
    >
      G {suffix}
    </kbd>
  );
}

function sectionLabel(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

function configurePreferenceKey(
  serviceId: string,
  environmentId?: string,
): string {
  return `${CONFIGURE_PREFERENCE_PREFIX}:${environmentId || "default"}:${serviceId}`;
}

function resourceHasSource(resource?: EnvironmentServiceResource): boolean {
  if (!resource) return false;
  return Boolean(
    resource.source ||
      resource.active_deployment?.source_type ||
      resource.active_deployment?.image ||
      resource.latest_deployment?.source_type ||
      resource.latest_deployment?.image,
  );
}

function isTextEntry(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.isContentEditable ||
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement
  );
}

function findSettingControl(
  scope: HTMLElement,
  visibleLabels?: readonly string[],
): HTMLElement | undefined {
  if (!visibleLabels?.length) return undefined;
  for (const visibleLabel of visibleLabels) {
    const normalized = visibleLabel.trim().toLocaleLowerCase();
    const label = Array.from(scope.querySelectorAll("label")).find((candidate) =>
      candidate.textContent?.trim().toLocaleLowerCase().startsWith(normalized),
    );
    const labelledControl =
      label?.control ??
      label?.querySelector<HTMLElement>("input, select, textarea, button");
    if (labelledControl && isFocusableSettingControl(labelledControl)) {
      return labelledControl;
    }
    const namedControl = Array.from(
      scope.querySelectorAll<HTMLElement>(
        "button, input[aria-label], select[aria-label], textarea[aria-label]",
      ),
    ).find((candidate) => {
      const name =
        candidate.getAttribute("aria-label") || candidate.textContent || "";
      return (
        name.trim().toLocaleLowerCase().startsWith(normalized) &&
        isFocusableSettingControl(candidate)
      );
    });
    if (namedControl) return namedControl;
  }
  return undefined;
}

function isFocusableSettingControl(control: HTMLElement): boolean {
  return !("disabled" in control && control.disabled === true);
}
