import type {
  EnvironmentDeploymentSummary,
  EnvironmentServiceResource,
} from "@/api/client";
import { StatusPill } from "@/components/StatusPill";
import {
  describeServiceOperation,
  serviceSetupJourney,
  type ServiceSetupAction,
} from "@/lib/serviceOperations";

type Props = {
  resource?: EnvironmentServiceResource;
  environmentName?: string;
  onViewDeployments?: () => void;
  onViewLogs?: () => void;
  onConfigure?: (section: "source" | "networking" | "runtime") => void;
};

export function ServiceOverview({
  resource,
  environmentName,
  onViewDeployments,
  onViewLogs,
  onConfigure,
}: Props) {
  if (!resource) {
    return (
      <div className="rounded-lg border border-dashed border-line bg-bg-2 px-4 py-6 text-sm text-muted">
        <strong className="block text-text">
          No environment summary
        </strong>
        Midnight has not reported this service in the selected
        environment. Deployment, health, and reachability are not
        inferred.
      </div>
    );
  }

  const latest = resource.latest_deployment;
  const deployment =
    resource.active_deployment ?? (latest?.active ? latest : undefined);
  const rollout = latest && latest.id !== deployment?.id ? latest : undefined;
  const source = resource.source;
  const hasCanonicalSourceConfig =
    source?.truth_source === "canonical-github-link" ||
    source?.auto_deploy !== undefined ||
    source?.wait_for_ci !== undefined;
  const endpoint = resource.endpoint;
  const privateEndpoint = resource.private_endpoint;
  const healthDeployment = deployment ?? latest;
  const replicas = healthDeployment?.replicas;
  // Why the last rollout stopped, in words rather than a code. The failed
  // rollout owns the explanation even when an older release still serves.
  const rolloutReason =
    describeRolloutReason(latest) ?? describeRolloutReason(deployment);
  const next = overviewNextAction(resource);
  const setup = serviceSetupJourney(resource);
  const operationalSummary = describeServiceOperation(resource);

  const runSetupAction = (action: ServiceSetupAction) => {
    if (action.kind === "configure") onConfigure?.(action.section);
    else if (action.kind === "deployments") onViewDeployments?.();
    else onViewLogs?.();
  };

  const setupActionAvailable = (action?: ServiceSetupAction) => {
    if (!action) return false;
    if (action.kind === "configure") return Boolean(onConfigure);
    if (action.kind === "deployments") return Boolean(onViewDeployments);
    return Boolean(onViewLogs);
  };

  return (
    <div className="grid gap-4">
      {!setup.complete ? (
        <ServiceSetupCard
          serviceName={
            resource.service.display_name || resource.service.slug
          }
          steps={setup.steps}
          action={setup.action}
          actionAvailable={setupActionAvailable(setup.action)}
          onAction={runSetupAction}
        />
      ) : null}

      <section className="grid gap-3 rounded-lg border border-line bg-surface-2 px-4 py-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <p className="m-0 font-mono text-[10px] uppercase tracking-wider text-subtle">
              Current release
            </p>
            <h2 className="m-0 mt-1 text-base font-semibold text-text">
              {deployment ? deployment.id : "No active release"}
            </h2>
          </div>
          {deployment ? (
            <StatusPill
              signal={{
                status: deployment.status,
                operation_state: deployment.operation?.state,
                active: deployment.active,
              }}
            />
          ) : (
            <span
              className="status-pill"
              data-tone="neutral"
              role="status"
            >
              not deployed
            </span>
          )}
        </div>

        <p className="m-0 text-sm leading-5 text-muted" role="status">
          {operationalSummary}
        </p>

        <dl className="m-0 grid gap-px overflow-hidden rounded border border-line bg-line sm:grid-cols-2">
          <Fact label="Environment" value={environmentName} />
          <Fact
            label="Traffic state"
            value={
              deployment?.active
                ? "Active deployment"
                : "Not active"
            }
          />
          <Fact
            label="Revision"
            value={deployment?.commit_sha}
            mono
            span
          />
          <Fact
            label="Runtime"
            value={
              deployment?.runtime_observed_state
                ? `${deployment.runtime_observed_state} observed · ${deployment.runtime_desired_state || "unknown"} desired`
                : undefined
            }
          />
          <Fact
            label="Replicas"
            value={
              replicas
                ? `${replicas.ready}/${replicas.desired} ready · ${replicas.running} running`
                : undefined
            }
          />
        </dl>

        {deployment ? (
          <details className="rounded border border-line bg-bg-2 px-3 py-2 text-xs">
            <summary className="cursor-pointer font-semibold text-muted">
              Technical details
            </summary>
            <dl className="mt-2 grid gap-px overflow-hidden rounded border border-line bg-line sm:grid-cols-2">
              <Fact
                label="Operation ID"
                value={deployment.operation?.id}
                mono
              />
              <Fact
                label="Record source"
                value={
                  deployment.truth_source ===
                  "legacy-operation"
                    ? "Legacy operation record"
                    : "Deployment record"
                }
              />
              <Fact
                label="Container image"
                value={deployment.image}
                mono
                span
              />
            </dl>
          </details>
        ) : null}

        {rollout ? (
          <div className="grid gap-1 rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-2 text-xs">
            <span className="flex flex-wrap items-center justify-between gap-2 text-muted">
              <span>
                New rollout (not serving){" "}
                <span className="font-mono text-text">
                  {rollout.id}
                </span>
              </span>
              <StatusPill
                signal={{
                  status: rollout.status,
                  operation_state: rollout.operation?.state,
                  active: rollout.active,
                }}
              />
            </span>
            {rollout.commit_sha || rollout.source_ref ? (
              <span className="font-mono text-[11px] text-subtle">
                {[rollout.source_ref, rollout.commit_sha]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
            ) : null}
            {rollout.status.toLowerCase() === "failed" &&
            deployment ? (
              <span className="text-[11px] text-muted">
                The current release remains active; this failed
                rollout did not replace it.
              </span>
            ) : null}
          </div>
        ) : null}

        {rolloutReason ? (
          <div
            role="alert"
            className="m-0 grid gap-1 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger"
          >
            <span className="font-medium">{rolloutReason.headline}</span>
            {rolloutReason.detail ? (
              <span className="break-words text-danger/80">
                {rolloutReason.detail}
              </span>
            ) : null}
          </div>
        ) : null}
      </section>

      <section className="grid gap-3 rounded-lg border border-line bg-surface-2 px-4 py-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="m-0 font-mono text-[10px] uppercase tracking-wider text-subtle">
            Connected source
          </p>
          {source?.auto_deploy !== undefined ? (
            <span className="text-[11px] text-subtle">
              Auto-deploy {source.auto_deploy ? "on" : "off"}
            </span>
          ) : null}
        </div>
        <dl className="m-0 grid gap-px overflow-hidden rounded border border-line bg-line sm:grid-cols-2">
          <Fact
            label="Type"
            value={source?.type || latest?.source_type}
          />
          <Fact
            label="Repository"
            value={source?.repository || latest?.source_repository}
            mono
          />
          <Fact
            label="Branch / ref"
            value={source?.ref || latest?.source_ref}
            mono
          />
          <Fact
            label="Latest matching revision"
            value={source?.revision}
            mono
          />
          <Fact
            label="Watch paths"
            value={
              hasCanonicalSourceConfig
                ? source?.watch_paths?.length
                  ? source.watch_paths.join(", ")
                  : "All repository paths"
                : undefined
            }
            mono
          />
          <Fact
            label="Root directory"
            value={source?.root_directory || latest?.root_directory}
            mono
          />
          <Fact
            label="Config path"
            value={
              hasCanonicalSourceConfig
                ? source?.config_path || "Repository default"
                : undefined
            }
            mono
          />
          <Fact
            label="CI gate"
            value={
              source?.wait_for_ci
                ? source.required_checks?.length
                  ? `Required: ${source.required_checks.join(", ")}`
                  : "Wait for CI"
                : source?.wait_for_ci === false
                  ? "Disabled"
                  : undefined
            }
          />
          <Fact
            label="Dockerfile"
            value={latest?.dockerfile_path}
            mono
          />
          <Fact label="Builder" value={latest?.builder} mono />
        </dl>
        {source?.verified_at ? (
          <span className="text-[11px] text-subtle">
            Repository access verified{" "}
            {formatTime(source.verified_at)}
          </span>
        ) : null}
      </section>

      <section className="grid gap-3 rounded-lg border border-line bg-surface-2 px-4 py-4">
        <p className="m-0 font-mono text-[10px] uppercase tracking-wider text-subtle">
          Health
        </p>
        {replicas ? (
          <div className="grid gap-1">
            <strong className="text-sm capitalize text-text">
              {replicas.health}
            </strong>
            <span className="text-xs text-muted">
              {replicas.ready} of {replicas.desired} replicas
              ready · {replicas.running} observed running
            </span>
            <span className="text-[11px] text-subtle">
              Last reported for{" "}
              {deployment
                ? "the current release"
                : "the latest rollout"}{" "}
              {formatTime(replicas.evidence_at)}
            </span>
          </div>
        ) : (
          <span className="text-sm text-muted">
            No health has been reported yet for the current release.
          </span>
        )}
      </section>

      <section className="grid gap-2 rounded-lg border border-line bg-surface-2 px-4 py-4">
        <p className="m-0 font-mono text-[10px] uppercase tracking-wider text-subtle">
          Reachability
        </p>
        {endpoint?.url && /^https?:\/\//i.test(endpoint.url) ? (
          <a
            href={endpoint.url}
            target="_blank"
            rel="noreferrer"
            className="break-all font-mono text-sm text-accent hover:underline"
          >
            {endpoint.host || endpoint.url} ↗
          </a>
        ) : endpoint?.host ? (
          <span className="break-all font-mono text-sm text-text">
            {endpoint.host}
          </span>
        ) : (
          <span className="text-sm text-muted">
            No published endpoint is present in the environment
            summary.
          </span>
        )}
        {endpoint ? (
          <span className="text-[11px] text-subtle">
            {endpoint.tls ? "TLS reported" : "TLS not reported"} ·
            last checked {formatTime(endpoint.evidence_at)}
          </span>
        ) : null}
        {privateEndpoint?.host ? (
          <div className="mt-1 border-t border-line-soft pt-2">
            <span className="block text-[10px] uppercase tracking-wider text-subtle">
              Private address
            </span>
            <code className="break-all text-xs text-text">
              {privateEndpoint.host}
            </code>
            <span className="mt-1 block text-[11px] text-subtle">
              Service-discovery identity; readiness is reported
              separately above.
            </span>
          </div>
        ) : null}
      </section>

      {setup.complete ? (
        <section className="flex flex-wrap items-center justify-between gap-3 rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-3">
          <div>
            <p className="m-0 text-xs font-semibold text-text">
              Next action
            </p>
            <p className="m-0 mt-0.5 text-xs text-muted">
              {next.detail}
            </p>
          </div>
          {next.kind === "live" && endpoint?.url ? (
            <a
              href={endpoint.url}
              target="_blank"
              rel="noreferrer"
              className="rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-3 py-1.5 text-xs font-semibold text-accent"
            >
              Open live ↗
            </a>
          ) : (
            <button
              type="button"
              className="rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-3 py-1.5 text-xs font-semibold text-text disabled:opacity-50"
              disabled={
                (next.kind === "logs" && !onViewLogs) ||
                (next.kind === "deployments" &&
                  !onViewDeployments) ||
                (next.kind === "configure" && !onConfigure)
              }
              onClick={() => {
                if (next.kind === "logs") onViewLogs?.();
                else if (next.kind === "deployments")
                  onViewDeployments?.();
                else if (next.kind === "configure")
                  onConfigure?.(
                    next.configureSection ?? "networking",
                  );
              }}
            >
              {next.label}
            </button>
          )}
        </section>
      ) : null}
    </div>
  );
}

function ServiceSetupCard({
  serviceName,
  steps,
  action,
  actionAvailable,
  onAction,
}: {
  serviceName: string;
  steps: ReturnType<typeof serviceSetupJourney>["steps"];
  action?: ServiceSetupAction;
  actionAvailable: boolean;
  onAction: (action: ServiceSetupAction) => void;
}) {
  const titleId = `service-setup-${serviceName
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")}`;
  return (
    <section
      className="grid gap-3 rounded-lg border border-accent/40 bg-accent-soft px-4 py-4"
      aria-labelledby={titleId}
    >
      <div>
        <p className="m-0 font-mono text-[10px] uppercase tracking-wider text-accent">
          Setup progress
        </p>
        <h2
          id={titleId}
          className="m-0 mt-1 text-base font-semibold text-text"
        >
          Finish setting up {serviceName}
        </h2>
      </div>
      <ol className="m-0 grid list-none gap-1.5 p-0 text-xs">
        {steps.map((step) => (
          <li key={step.label} className="flex items-start gap-2">
            <span
              aria-hidden="true"
              className={
                step.complete ? "text-success" : "text-subtle"
              }
            >
              {step.complete ? "✓" : "○"}
            </span>
            <span className="grid gap-0.5">
              <strong className="text-text">{step.label}</strong>
              <span className="text-muted">{step.detail}</span>
            </span>
          </li>
        ))}
      </ol>
      {action ? (
        <button
          type="button"
          className="w-fit rounded-[var(--r-sm)] border border-accent/60 bg-accent px-3 py-1.5 text-xs font-semibold text-[var(--accent-ink)] disabled:opacity-50"
          disabled={!actionAvailable}
          onClick={() => onAction(action)}
        >
          {action.label}
        </button>
      ) : null}
    </section>
  );
}

function overviewNextAction(resource: EnvironmentServiceResource): {
  kind: "live" | "logs" | "deployments" | "configure" | "details";
  label: string;
  detail: string;
  configureSection?: "source" | "networking";
} {
  const latest = resource.latest_deployment;
  const health = (resource.active_deployment ?? latest)?.replicas?.health;
  if (
    latest?.status.toLowerCase() === "failed" ||
    latest?.operation?.state.toLowerCase() === "failed" ||
    health === "unhealthy" ||
    health === "degraded"
  ) {
    return {
      kind: "logs",
      label: "Review logs",
      detail: "Inspect the failed or degraded workload before changing configuration.",
    };
  }
  if (
    latest &&
    ["queued", "building", "deploying"].includes(
      latest.status.toLowerCase(),
    )
  ) {
    return {
      kind: "deployments",
      label: "View rollout",
      detail: "Follow the latest release through build, deploy, and activation.",
    };
  }
  if (!resource.source && !latest?.image) {
    return {
      kind: "configure",
      label: "Connect source",
      detail: "Connect a repository or deploy an immutable image to create the first release.",
      configureSection: "source",
    };
  }
  if (resource.endpoint?.url) {
    return {
      kind: "live",
      label: "Open live",
      detail: "This address is routed and has answered a health check.",
    };
  }
  return {
    kind: "configure",
    label: "Configure networking",
    detail: "The workload has no verified public endpoint yet; add or verify a domain when it should be public.",
    configureSection: "networking",
  };
}

function Fact({
  label,
  value,
  mono,
  span,
}: {
  label: string;
  value?: string;
  mono?: boolean;
  span?: boolean;
}) {
  return (
    <div className={`bg-bg-2 px-3 py-2 ${span ? "sm:col-span-2" : ""}`}>
      <dt className="text-[10px] uppercase tracking-wider text-subtle">
        {label}
      </dt>
      <dd
        className={`m-0 mt-0.5 break-all text-xs ${
          mono ? "font-mono" : ""
        } ${value ? "text-text" : "text-subtle"}`}
      >
        {value || "Not reported"}
      </dd>
    </div>
  );
}

function formatTime(value: string): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime())
    ? value
    : `${parsed.toISOString().slice(0, 16).replace("T", " ")} UTC`;
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
