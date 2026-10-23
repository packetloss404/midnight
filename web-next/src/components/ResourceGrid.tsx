import { useMemo, useRef } from "react";
import type {
  EnvironmentDeploymentSummary,
  EnvironmentServiceResource,
} from "@/api/client";
import { StatusPill } from "@/components/StatusPill";
import { describeServiceOperation } from "@/lib/serviceOperations";

type Props = {
  resources: EnvironmentServiceResource[];
  serviceDraftOperations?: Record<string, DraftOperation[]>;
  selectedServiceId?: string;
  onSelectService: (
    serviceId: string,
    activation?: "pointer" | "keyboard",
  ) => void;
  onViewLogs?: (serviceId: string) => void;
  onViewDeployments?: (serviceId: string) => void;
  onConfigureService?: (serviceId: string, section: "source") => void;
  /**
   * False when callers have only tenant-scoped service metadata. Missing
   * deployment rows must then render as unavailable evidence, not "not
   * deployed", because the latter is a product-state claim.
   */
  deploymentEvidenceAvailable?: boolean;
  scopeLabel?: string;
};

export function ResourceGrid({
  resources,
  serviceDraftOperations = {},
  selectedServiceId,
  onSelectService,
  onViewLogs,
  onViewDeployments,
  onConfigureService,
  deploymentEvidenceAvailable = true,
  scopeLabel = "in this environment",
}: Props) {
  const serviceButtons = useRef<Array<HTMLButtonElement | null>>([]);
  const ordered = useMemo(
    () =>
      [...resources].sort(
        (a, b) =>
          compareText(a.service.slug, b.service.slug) ||
          compareText(a.service.id, b.service.id),
      ),
    [resources],
  );

  const focusAt = (index: number) => {
    const normalized = (index + ordered.length) % ordered.length;
    serviceButtons.current[normalized]?.focus();
  };

  return (
    <section
      className="resource-grid-stage h-full overflow-auto rounded-lg border border-line bg-bg-2"
      aria-labelledby="resource-grid-title"
    >
      <div className="sticky top-0 z-10 flex flex-wrap items-center justify-between gap-2 border-b border-line bg-bg-2/95 px-4 py-3 backdrop-blur">
        <div>
          <h2
            id="resource-grid-title"
            className="m-0 text-sm font-semibold text-text"
          >
            Resource grid
          </h2>
          <p className="m-0 mt-0.5 text-xs text-muted">
            {ordered.length}{" "}
            {ordered.length === 1 ? "service" : "services"}{" "}
            {scopeLabel}
          </p>
        </div>
        <span className="font-mono text-[10px] text-subtle">
          {onViewLogs
            ? "Arrow keys move · Enter opens · L shows logs"
            : "Arrow keys move · Enter opens"}
        </span>
      </div>

      <ul className="resource-grid m-0 list-none p-4">
        {ordered.map((resource, index) => {
          const {
            service,
            latest_deployment: latestDeployment,
            active_deployment: activeDeployment,
            source,
            endpoint,
          } = resource;
          const deployment = presentationDeployment(
            latestDeployment,
            activeDeployment,
          );
          const currentRelease =
            activeDeployment ??
            (latestDeployment?.active
              ? latestDeployment
              : undefined);
          const rollout =
            latestDeployment &&
            latestDeployment.id !== currentRelease?.id
              ? latestDeployment
              : undefined;
          const healthDeployment = currentRelease ?? latestDeployment;
          const currentRevision = currentRelease?.commit_sha;
          const selected = service.id === selectedServiceId;
          // A stuck or failed rollout should explain itself on the card,
          // not only inside the operation timeline. When an older release
          // is still serving, the failed rollout still owns the reason.
          const rolloutReason =
            describeRolloutReason(deployment) ??
            describeRolloutReason(rollout);
          const operationalSummary = describeServiceOperation(
            resource,
            deploymentEvidenceAvailable,
          );
          const draftOperations = serviceDraftOperations[service.id] ?? [];

          return (
            <li key={service.id} className="min-w-0">
              <article
                className="resource-card"
                data-selected={selected ? "true" : "false"}
                data-status={
                  deployment?.status?.toLowerCase() ??
                  "absent"
                }
              >
                <div className="flex min-w-0 items-start justify-between gap-3">
                  <button
                    ref={(node) => {
                      serviceButtons.current[index] =
                        node;
                    }}
                    type="button"
                    className="min-w-0 flex-1 rounded-sm text-left"
                    data-service-open={service.id}
                    aria-current={
                      selected ? "true" : undefined
                    }
                    aria-label={`Open ${service.display_name || service.slug}`}
                    onClick={(event) =>
                      onSelectService(
                        service.id,
                        event.detail === 0
                          ? "keyboard"
                          : "pointer",
                      )
                    }
                    onKeyDown={(event) => {
                      if (
                        event.key === "ArrowRight" ||
                        event.key === "ArrowDown"
                      ) {
                        event.preventDefault();
                        focusAt(index + 1);
                      } else if (
                        event.key === "ArrowLeft" ||
                        event.key === "ArrowUp"
                      ) {
                        event.preventDefault();
                        focusAt(index - 1);
                      } else if (event.key === "Home") {
                        event.preventDefault();
                        focusAt(0);
                      } else if (event.key === "End") {
                        event.preventDefault();
                        focusAt(ordered.length - 1);
                      } else if (
                        onViewLogs &&
                        event.key.toLowerCase() ===
                          "l" &&
                        !event.metaKey &&
                        !event.ctrlKey &&
                        !event.altKey
                      ) {
                        event.preventDefault();
                        onViewLogs(service.id);
                      }
                    }}
                  >
                    <span className="block truncate text-[15px] font-semibold tracking-tight text-text">
                      {service.display_name ||
                        service.slug}
                    </span>
                    <span className="mt-0.5 block truncate font-mono text-[11px] text-subtle">
                      {service.slug}
                    </span>
                    {draftOperations.length > 0 ? (
                      <span
                        className="mt-1.5 flex flex-wrap items-center gap-1"
                        aria-label="Draft changes"
                      >
                        <span className="text-[9px] font-semibold uppercase tracking-wider text-subtle">
                          Draft
                        </span>
                        {draftOperations.map((operation) => (
                          <DraftBadge key={operation} operation={operation} />
                        ))}
                      </span>
                    ) : null}
                  </button>
                  {deployment ? (
                    <StatusPill
                      signal={{
                        status: deployment.status,
                        operation_state:
                          deployment.operation?.state,
                        active: deployment.active,
                      }}
                    />
                  ) : (
                    <span
                      className="status-pill"
                      data-tone="neutral"
                      role="status"
                    >
                      {deploymentEvidenceAvailable
                        ? "not deployed"
                        : "deployment unavailable"}
                    </span>
                  )}
                </div>

                <p
                  className="m-0 text-sm leading-5 text-muted"
                  role="status"
                >
                  {operationalSummary}
                </p>

                <div className="grid gap-2 rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-2 text-xs">
                  <ReleaseLine
                    label="Serving"
                    deployment={currentRelease}
                    absent={
                      deploymentEvidenceAvailable
                        ? "No active release"
                        : "Deployment status unavailable"
                    }
                  />
                  {rollout ? (
                    <ReleaseLine
                      label="New rollout"
                      deployment={rollout}
                    />
                  ) : null}
                  <span className="flex min-w-0 items-center justify-between gap-3">
                    <span className="text-subtle">
                      Source
                    </span>
                    <span
                      className="truncate font-mono text-muted"
                      title={sourceLabel(
                        source,
                        deployment,
                      )}
                    >
                      {sourceLabel(source, deployment) ||
                        "Not connected"}
                    </span>
                  </span>
                  {healthDeployment?.replicas ? (
                    <span className="flex min-w-0 items-center justify-between gap-3">
                      <span className="text-subtle">
                        Health
                      </span>
                      <span className="truncate text-muted">
                        {
                          healthDeployment.replicas
                            .health
                        }{" "}
                        ·{" "}
                        {
                          healthDeployment.replicas
                            .ready
                        }
                        /
                        {
                          healthDeployment.replicas
                            .desired
                        }{" "}
                        ready
                      </span>
                    </span>
                  ) : null}
                </div>

                <details className="rounded border border-line-soft bg-bg-2 px-3 py-2 text-xs">
                  <summary className="cursor-pointer font-semibold text-muted">
                    Technical details
                  </summary>
                  <dl className="resource-card-details mt-3">
                    <ResourceFact
                      label="Current release"
                      value={currentRelease?.id}
                      absent={
                        deploymentEvidenceAvailable
                          ? "No deployment recorded"
                          : "Not available with this session"
                      }
                      mono
                    />
                    {rollout ? (
                      <ResourceFact
                        label="Latest rollout"
                        value={`${rollout.id} · ${rollout.status.toLowerCase()}`}
                        mono
                      />
                    ) : null}
                    <ResourceFact
                      label="Current revision"
                      value={currentRevision}
                      absent="No revision reported"
                      mono
                    />
                    {rollout?.commit_sha &&
                    rollout.commit_sha !==
                      currentRevision ? (
                      <ResourceFact
                        label="Rollout revision"
                        value={rollout.commit_sha}
                        mono
                      />
                    ) : null}
                    {endpoint ? (
                      <EndpointFact
                        host={endpoint.host}
                        url={endpoint.url}
                      />
                    ) : null}
                  </dl>
                </details>

                {rolloutReason ? (
                  <div
                    role="status"
                    className="m-0 grid gap-1 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-2.5 py-2 text-xs text-danger"
                  >
                    <span className="font-medium">
                      {rolloutReason.headline}
                    </span>
                    {rolloutReason.detail ? (
                      <span className="break-words text-danger/80">
                        {rolloutReason.detail}
                      </span>
                    ) : null}
                  </div>
                ) : null}

                <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-line-soft pt-3">
                  <div className="flex items-center gap-2">
                    {nextAction(resource) === "logs" &&
                    onViewLogs ? (
                      <button
                        type="button"
                        className="rounded-[var(--r-sm)] px-2 py-1 text-xs font-medium text-muted hover:bg-surface-2 hover:text-text"
                        onClick={() =>
                          onViewLogs(service.id)
                        }
                      >
                        Review logs
                      </button>
                    ) : nextAction(resource) ===
                        "deployments" &&
                      onViewDeployments ? (
                      <button
                        type="button"
                        className="rounded-[var(--r-sm)] px-2 py-1 text-xs font-medium text-muted hover:bg-surface-2 hover:text-text"
                        onClick={() =>
                          onViewDeployments(
                            service.id,
                          )
                        }
                      >
                        View rollout
                      </button>
                    ) : nextAction(resource) ===
                        "configure" &&
                      onConfigureService ? (
                      <button
                        type="button"
                        className="rounded-[var(--r-sm)] px-2 py-1 text-xs font-medium text-muted hover:bg-surface-2 hover:text-text"
                        onClick={() =>
                          onConfigureService(
                            service.id,
                            "source",
                          )
                        }
                      >
                        Connect source
                      </button>
                    ) : endpoint?.url &&
                      /^https?:\/\//i.test(endpoint.url) ? (
                      <a
                        className="rounded-[var(--r-sm)] px-2 py-1 text-xs font-medium text-accent hover:bg-surface-2"
                        href={endpoint.url}
                        target="_blank"
                        rel="noreferrer"
                      >
                        Open live ↗
                      </a>
                    ) : null}
                    <button
                      type="button"
                      className="rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-2.5 py-1 text-xs font-semibold text-text hover:bg-surface-3"
                      onClick={(event) =>
                        onSelectService(
                          service.id,
                          event.detail === 0
                            ? "keyboard"
                            : "pointer",
                        )
                      }
                    >
                      Details
                    </button>
                  </div>
                </footer>
              </article>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export type DraftOperation = "add" | "edit" | "remove";

function DraftBadge({ operation }: { operation: DraftOperation }) {
  const presentation = {
    add: { label: "New", className: "border-accent/50 bg-accent-soft text-accent" },
    edit: { label: "Edited", className: "border-staged/50 bg-staged/10 text-staged" },
    remove: { label: "Removed", className: "border-danger/50 bg-danger-soft text-danger" },
  } as const;
  const current = presentation[operation];
  return (
    <span
      className={`rounded border px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider ${current.className}`}
    >
      {current.label}
    </span>
  );
}

function ReleaseLine({
  label,
  deployment,
  absent,
}: {
  label: string;
  deployment?: EnvironmentDeploymentSummary;
  absent?: string;
}) {
  return (
    <span className="flex min-w-0 items-center justify-between gap-3">
      <span className="text-subtle">{label}</span>
      {deployment ? (
        <span className="flex min-w-0 items-center gap-2">
          <span
            className="truncate font-mono text-text"
            title={deployment.id}
          >
            {deployment.id}
          </span>
          <span className="text-subtle">
            {deployment.status.toLowerCase()}
          </span>
        </span>
      ) : (
        <span className="truncate text-subtle">
          {absent || "Not recorded"}
        </span>
      )}
    </span>
  );
}

function ResourceFact({
  label,
  value,
  absent,
  mono = false,
}: {
  label: string;
  value?: string;
  absent?: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-[10px] uppercase tracking-wider text-subtle">
        {label}
      </dt>
      <dd
        className={`m-0 mt-0.5 truncate text-xs ${mono ? "font-mono" : ""} ${
          value ? "text-muted" : "text-subtle"
        }`}
        title={value}
      >
        {value || absent || "Not recorded"}
      </dd>
    </div>
  );
}

function EndpointFact({ host, url }: { host?: string; url?: string }) {
  const label = host?.trim() || url?.trim();
  if (!label) return null;
  const safeURL = /^https?:\/\//i.test(url ?? "") ? url : undefined;
  return (
    <div className="min-w-0">
      <dt className="text-[10px] uppercase tracking-wider text-subtle">
        Published endpoint
      </dt>
      <dd className="m-0 mt-0.5 truncate font-mono text-xs text-accent">
        {safeURL ? (
          <a
            href={safeURL}
            target="_blank"
            rel="noreferrer"
            title={label}
          >
            {label}
          </a>
        ) : (
          <span title={label}>{label}</span>
        )}
      </dd>
    </div>
  );
}

function presentationDeployment(
  latest?: EnvironmentDeploymentSummary,
  active?: EnvironmentDeploymentSummary,
): EnvironmentDeploymentSummary | undefined {
  if (!latest) return active;
  const status = latest.status.toLowerCase();
  if (
    latest.id !== active?.id &&
    ["queued", "building", "deploying", "failed", "canceled"].includes(
      status,
    )
  ) {
    return latest;
  }
  return active ?? latest;
}

function sourceLabel(
  source: EnvironmentServiceResource["source"],
  deployment?: EnvironmentDeploymentSummary,
): string | undefined {
  const repository = source?.repository || deployment?.source_repository;
  const ref = source?.ref || deployment?.source_ref;
  if (repository) return ref ? `${repository} · ${ref}` : repository;
  const type = source?.type || deployment?.source_type;
  if (type) return type;
  return deployment?.image ? "container image" : undefined;
}

function nextAction(
  resource: EnvironmentServiceResource,
): "logs" | "deployments" | "configure" | "live" | "details" {
  const latest = resource.latest_deployment;
  const health = (resource.active_deployment ?? latest)?.replicas?.health;
  if (
    latest?.status.toLowerCase() === "failed" ||
    latest?.operation?.state.toLowerCase() === "failed" ||
    health === "unhealthy" ||
    health === "degraded"
  ) {
    return "logs";
  }
  if (
    latest &&
    ["queued", "building", "deploying"].includes(
      latest.status.toLowerCase(),
    )
  ) {
    return "deployments";
  }
  if (!resource.source && !latest?.image) return "configure";
  if (resource.endpoint?.url) return "live";
  return "details";
}

function compareText(a: string, b: string): number {
  if (a === b) return 0;
  return a < b ? -1 : 1;
}

/**
 * Plain-language explanation of why a rollout stopped or is stuck, built
 * from the deployment's typed failure code and the worker's own reason.
 *
 * The headline says what happened in words anyone on the team can act on;
 * the detail keeps the worker's exact reason (exit code, container log
 * tail, probe error) directly underneath it.
 */
function describeRolloutReason(
  deployment?: EnvironmentDeploymentSummary,
): { headline: string; detail?: string } | undefined {
  const reason = deployment?.operation?.failure_reason?.trim();
  const code = deployment?.operation?.failure_code?.trim().toUpperCase();
  if (!reason && !code) return undefined;
  const headline = rolloutHeadline(code);
  if (!headline) return { headline: reason ?? (code as string) };
  return { headline, detail: reason };
}

function rolloutHeadline(code?: string): string | undefined {
  switch (code) {
    case "DEPLOY_CANDIDATE_EXITED":
      return "The container stopped before it was ready.";
    case "DEPLOY_CANDIDATE_RESTART_LOOP":
      return "The container keeps starting and crashing.";
    case "RECONCILE_STEP_FAILED":
      return "The service never became ready in time.";
    case "DEPLOY_CANCELED_BEFORE_PROMOTION":
    case "DEPLOY_CANCELED_BEFORE_LEASE":
      return "This deploy was canceled before traffic moved.";
    case "PREDEPLOY_FAILED":
    case "PREDEPLOY_TIMEOUT":
      return "The pre-deploy command did not finish successfully.";
    default:
      return undefined;
  }
}
