// Single source of truth mapping backend deployment/operation signals to a
// UI tone + label. Every status surface (StatusPill, ServiceNode,
// DeploymentList, the drawer) must route through here so a service shows the
// same colour everywhere.
//
// Backend inputs (see `internal/api/deployments.go#deploymentStatus`):
//   - `status`          : active | queued | deploying | succeeded | failed |
//                         canceled | unknown
//   - `operation_state` : queued | running | succeeded | failed | canceled
//   - `active`          : bool — has a promoted route (live traffic)

export type StatusTone =
  | "success"
  | "info"
  | "warning"
  | "danger"
  | "neutral";

export type DeploymentSignal = {
  status?: string | null;
  operation_state?: string | null;
  active?: boolean | null;
};

export type DeploymentStatusView = {
  tone: StatusTone;
  /** Lowercase, human label for the pill. */
  label: string;
  /** True for in-flight states that should animate/pulse. */
  pending: boolean;
};

const STATUS_TONE: Record<string, StatusTone> = {
  active: "success",
  ready: "success",
  running: "success",
  succeeded: "success",
  healthy: "success",
  queued: "info",
  pending: "info",
  deploying: "info",
  building: "info",
  warn: "warning",
  warning: "warning",
  degraded: "warning",
  failed: "danger",
  error: "danger",
  crashed: "danger",
  canceled: "neutral",
  cancelled: "neutral",
  unknown: "neutral",
};

const OPERATION_STATE_STATUS: Record<string, string> = {
  queued: "queued",
  running: "deploying",
  succeeded: "succeeded",
  failed: "failed",
  canceled: "canceled",
};

const PENDING = new Set(["queued", "pending", "deploying", "building", "running"]);

/**
 * Resolve a deployment/operation signal to a display view. `active` wins:
 * an active deployment is always "active"/success regardless of the terminal
 * operation state, matching the backend's own precedence.
 */
export function deploymentStatus(signal: DeploymentSignal): DeploymentStatusView {
  if (signal.active) {
    return { tone: "success", label: "active", pending: false };
  }
  const raw =
    (signal.status && signal.status.toLowerCase()) ||
    (signal.operation_state &&
      OPERATION_STATE_STATUS[signal.operation_state.toLowerCase()]) ||
    "unknown";
  return {
    tone: STATUS_TONE[raw] ?? "neutral",
    label: raw,
    pending: PENDING.has(raw),
  };
}

/** Convenience for callers that only have a bare status string. */
export function statusTone(status: string | undefined | null): StatusTone {
  return deploymentStatus({ status }).tone;
}
