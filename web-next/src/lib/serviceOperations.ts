import type { EnvironmentServiceResource } from "@/api/client";

export function describeServiceOperation(
  resource: EnvironmentServiceResource,
  deploymentEvidenceAvailable = true,
): string {
  const latest = resource.latest_deployment;
  const active =
    resource.active_deployment ?? (latest?.active ? latest : undefined);
  const rollout = latest && latest.id !== active?.id ? latest : undefined;

  if (!active && !latest) {
    return deploymentEvidenceAvailable
      ? "Nothing is serving traffic because no deployment has been recorded."
      : "Deployment state is unavailable for this session.";
  }

  if (active && rollout) {
    const rolloutState = rollout.status.toLowerCase();
    if (
      rolloutState === "failed" ||
      rollout.operation?.state.toLowerCase() === "failed"
    ) {
      return `${active.id} is serving traffic. ${rollout.id} failed and did not replace it.`;
    }
    if (["queued", "building", "deploying"].includes(rolloutState)) {
      return `${active.id} is serving traffic while ${rollout.id} is ${rolloutState}.`;
    }
    return `${active.id} is serving traffic; ${rollout.id} is the newest rollout.`;
  }

  if (active) {
    const replicas = active.replicas;
    return replicas
      ? `${active.id} is serving traffic with ${replicas.ready} of ${replicas.desired} replicas ready.`
      : `${active.id} is serving traffic; replica health has not been reported.`;
  }

  const latestState = latest?.status.toLowerCase() || "not reported";
  return `Nothing is serving traffic. ${latest?.id} is ${latestState}.`;
}

export type ServiceSetupAction =
  | { kind: "configure"; section: "source" | "runtime"; label: string }
  | { kind: "deployments" | "logs"; label: string };

export type ServiceSetupJourney = {
  complete: boolean;
  steps: Array<{
    label: string;
    detail: string;
    complete: boolean;
  }>;
  action?: ServiceSetupAction;
};

/**
 * Builds setup progress only from facts in the environment summary. A first
 * deployment is the current read model's evidence that required configuration
 * passed admission; missing health evidence remains incomplete rather than
 * being treated as healthy.
 */
export function serviceSetupJourney(
  resource: EnvironmentServiceResource,
): ServiceSetupJourney {
  const latest = resource.latest_deployment;
  const active =
    resource.active_deployment ?? (latest?.active ? latest : undefined);
  const sourceReady = Boolean(resource.source || latest?.image);
  const configurationAccepted = Boolean(latest);
  const health = (active ?? latest)?.replicas?.health.toLowerCase();
  const healthy = Boolean(active && health === "healthy");
  const rolloutState = latest?.status.toLowerCase();
  const failed =
    rolloutState === "failed" ||
    latest?.operation?.state.toLowerCase() === "failed" ||
    health === "unhealthy" ||
    health === "degraded";

  const steps = [
    {
      label: "Service created",
      detail: "The service record is available in this environment.",
      complete: true,
    },
    {
      label: "Source connected",
      detail: sourceReady
        ? resource.source
          ? "A connected source is recorded."
          : "An immutable image is recorded."
        : "Connect a repository or choose an immutable image.",
      complete: sourceReady,
    },
    {
      label: "Required configuration",
      detail: configurationAccepted
        ? "Configuration was accepted for a deployment."
        : "Review variables, build, and runtime settings.",
      complete: configurationAccepted,
    },
    {
      label: "First deployment",
      detail: latest
        ? `${latest.id} · ${latest.status.toLowerCase()}`
        : "No deployment has been recorded.",
      complete: Boolean(latest),
    },
    {
      label: "Healthy",
      detail: healthy
        ? "The active release is healthy."
        : active?.replicas
          ? `Last reported health: ${active.replicas.health}.`
          : "Healthy active replicas have not been reported.",
      complete: healthy,
    },
  ];

  let action: ServiceSetupAction | undefined;
  if (!sourceReady) {
    action = {
      kind: "configure",
      section: "source",
      label: "Continue setup",
    };
  } else if (!configurationAccepted) {
    action = {
      kind: "configure",
      section: "runtime",
      label: "Continue setup",
    };
  } else if (failed) {
    action = { kind: "logs", label: "Review failure" };
  } else if (
    !active ||
    (rolloutState &&
      ["queued", "building", "deploying"].includes(rolloutState))
  ) {
    action = { kind: "deployments", label: "Follow deployment" };
  } else if (!healthy) {
    action = { kind: "logs", label: "Inspect health" };
  }

  return { complete: healthy, steps, action };
}
