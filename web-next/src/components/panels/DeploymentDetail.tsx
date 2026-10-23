// DeploymentDetail — single-deployment sub-view: metadata, the 3-dot action
// menu (Redeploy / Rollback / Stop / Restart / Remove / Cancel), and the live log stream.
//
// PROP CONTRACT:
//   deployment      the row to inspect (from DeploymentList onSelect)
//   environmentId?  env scope — threads into action cache invalidation
//   onBack?         optional back handler (renders a ‹ Back affordance)
//
// Reads reuse the shared hooks (`useOperation` for FSM timestamps/state).
// Actions go through the panel-local `useDeploymentActions`; every mutation
// uses its tenant-coordinated REST route. Logs render the shared `LogViewer`
// by operation id.

import { useEffect, useRef, useState } from "react";
import type { DeploymentResource } from "@/api/client";
import { useOperation } from "@/api/hooks";
import { StatusPill } from "@/components/StatusPill";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { LogViewer } from "@/components/panels/LogViewer";
import {
  deploySubject,
  formatDuration,
  formatRelative,
} from "./deployments/format";
import {
  deploymentActionState,
  useDeploymentActions,
  type DeploymentActionKind,
} from "./deployments/api";
import { useDeploymentMutationAuthority } from "./deployments/authority";
import { RuntimeConsoleCapability } from "./RuntimeConsoleCapability";

export type DeploymentDetailProps = {
  deployment: DeploymentResource;
  environmentId?: string;
  onBack?: () => void;
};

export function DeploymentDetail({ deployment, environmentId, onBack }: DeploymentDetailProps) {
  const d = deployment;
  const operation = useOperation(d.operation_id);
  const stateCaps = deploymentActionState(d);
  const authority = useDeploymentMutationAuthority(
    d.project_id,
    d.environment_id,
  );
  const caps = {
    ...stateCaps,
    canRedeploy: stateCaps.canRedeploy && authority.canReplay,
    canRollback: stateCaps.canRollback && authority.canReplay,
    canRestart: stateCaps.canRestart && authority.canRuntime,
    canStop: stateCaps.canStop && authority.canRuntime,
    canRemove: stateCaps.canRemove && authority.canRuntime,
    canCancel: stateCaps.canCancel && authority.canCancel,
  };
  const { action, cancel } = useDeploymentActions(d, environmentId);

  const busy = action.isPending || cancel.isPending;
  const errorMsg = action.error?.message ?? cancel.error?.message ?? null;
  const duration = formatDuration(operation.data?.created_at, operation.data?.updated_at);
  const routeURL =
    d.route_apply_state === "ready" && d.route_reachable === true
      ? d.route
      : undefined;
  const routeReachability =
    d.route_reachable === true
      ? "Trusted ingress ready"
      : d.route_reachable === false
        ? "Trusted ingress not ready"
        : undefined;

  const onAction = (kind: DeploymentActionKind) => action.mutate(kind);

  return (
    <section className="flex flex-col gap-4">
      <header className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1.5">
          <div className="flex items-center gap-2">
            {onBack ? (
              <button
                type="button"
                onClick={onBack}
                className="rounded-[var(--r-sm)] border border-line px-2 py-0.5 text-xs text-muted transition-colors hover:bg-surface-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              >
                ‹ Back
              </button>
            ) : null}
            <StatusPill signal={{ status: d.status, operation_state: d.operation_state, active: d.active }} />
            {d.active ? (
              <span className="rounded-full border border-accent/50 bg-accent-soft px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-accent">
                live
              </span>
            ) : null}
          </div>
          <h2 className="m-0 truncate font-mono text-sm font-semibold text-text" title={d.image || d.id}>
            {deploySubject(d)}
          </h2>
          <p className="m-0 flex items-center gap-2 text-[11px] text-subtle">
            <span>{formatRelative(d.last_event_at)}</span>
            {duration ? <span>· {duration}</span> : null}
            <span>· {d.source_type || d.action || "source not reported"}</span>
          </p>
        </div>

        <ActionsMenu
          caps={caps}
          busy={busy}
          unavailableReason={authority.detail}
          onAction={onAction}
          onCancel={() => cancel.mutate()}
        />
      </header>

      {errorMsg ? (
        <p
          role="alert"
          className="m-0 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger"
        >
          {errorMsg}
        </p>
      ) : null}

      {action.isSuccess && action.data ? (
        <p className="m-0 rounded-[var(--r-sm)] border border-accent/40 bg-accent-soft px-3 py-2 text-xs text-accent">
          Queued as activity{" "}
          <span className="font-mono">{action.data.operationId}</span>.
        </p>
      ) : null}

      <CapabilityBoundary
        state={
          authority.state !== "available"
            ? authority.state
            : caps.canRedeploy ||
                caps.canRollback ||
                caps.canStop ||
                caps.canRestart ||
                caps.canRemove ||
                caps.canCancel
              ? "available"
              : "unavailable"
        }
        title="Deployment actions"
        detail={
          authority.state !== "available"
            ? authority.detail
            : caps.canRedeploy || caps.canRollback || caps.canStop || caps.canRestart || caps.canRemove
            ? "Redeploy and roll back reuse the exact image and settings this deployment already has. Stop, restart, and remove are tracked separately in Activity."
            : caps.canCancel
              ? "Cancellation withdraws unleased work immediately or asks the build/deploy worker to stop at its next safe point before traffic promotion."
              : "No action is safe for this deployment right now. Cancel is only available while its work is still queued or running."
        }
      />

      <dl className="grid grid-cols-1 gap-px overflow-hidden rounded border border-line bg-line sm:grid-cols-2">
        <Meta label="Operation" value={d.operation_id} mono />
        <Meta label="Operation state" value={d.operation_state} mono />
        <Meta label="Deployment" value={d.deployment_id || d.id} mono />
        <Meta label="Instance" value={d.instance_id} mono />
        <Meta label="Image" value={d.image} mono span />
        <Meta label="Action" value={d.action} mono />
        <Meta label="Source" value={d.source_type} mono />
        <Meta label="Repository" value={d.source_repository} mono span />
        <Meta label="Source ref" value={d.source_ref} mono span />
        <Meta label="Commit" value={d.commit_sha} mono span />
        <Meta
          label="Actor"
          value={d.trigger_actor_label || "Not recorded by the deployment ledger"}
        />
        <Meta label="Created" value={d.created_at} mono />
        <Meta label="Started" value={d.started_at} mono />
        <Meta label="Completed" value={d.completed_at} mono />
        <Meta
          label="Route URL (public reachability unverified)"
          value={routeURL}
          mono
          span
        />
        {d.route_intent ? (
          <Meta label="Requested hostname" value={d.route_intent} mono span />
        ) : null}
        {d.route_apply_state ? (
          <Meta label="Route state" value={d.route_apply_state} mono />
        ) : null}
        {routeReachability ? (
          <Meta label="Route reachability" value={routeReachability} />
        ) : null}
        <Meta label="Environment" value={d.environment_id} mono />
        <Meta label="Project" value={d.project_id} mono />
        {d.failure_code ? <Meta label="Failure code" value={d.failure_code} tone="danger" mono /> : null}
        {d.failure_reason ? <Meta label="Failure reason" value={d.failure_reason} tone="danger" span /> : null}
      </dl>

      <div className="flex flex-col gap-2">
        <h3 className="m-0 text-[10px] font-semibold uppercase tracking-wider text-subtle">
          Logs for this deployment
        </h3>
        <p className="m-0 text-xs text-muted">
          The full log history for this exact deployment. Every line keeps the
          build, runtime, HTTP, or platform source it came from.
        </p>
        <LogViewer operationId={d.operation_id} />
      </div>

      <RuntimeConsoleCapability />
    </section>
  );
}

// ── Metadata cell ─────────────────────────────────────────────────────────

function Meta({
  label,
  value,
  mono,
  span,
  tone,
}: {
  label: string;
  value?: string | null;
  mono?: boolean;
  span?: boolean;
  tone?: "danger";
}) {
  return (
    <div className={["bg-bg-2 px-3 py-2", span ? "sm:col-span-2" : ""].join(" ")}>
      <dt className="text-[10px] font-semibold uppercase tracking-wider text-subtle">{label}</dt>
      <dd
        className={[
          "m-0 mt-0.5 break-all text-xs",
          mono ? "font-mono" : "",
          tone === "danger" ? "text-danger" : "text-text",
        ].join(" ")}
      >
        {value && value.trim() !== "" ? value : (
          <span className="text-subtle">—</span>
        )}
      </dd>
    </div>
  );
}

// ── 3-dot actions menu ─────────────────────────────────────────────────────

function ActionsMenu({
  caps,
  busy,
  unavailableReason,
  onAction,
  onCancel,
}: {
  caps: ReturnType<typeof deploymentActionState>;
  busy: boolean;
  unavailableReason: string;
  onAction: (kind: DeploymentActionKind) => void;
  onCancel: () => void;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const run = (fn: () => void) => {
    setOpen(false);
    fn();
  };

  const items: { label: string; enabled: boolean; onClick: () => void; danger?: boolean; reason?: string }[] = [
    {
      label: "Redeploy",
      enabled: caps.canRedeploy,
      onClick: () => onAction("redeploy"),
      reason: "Unavailable while this deployment is in flight or removed",
    },
    {
      label: "Rollback",
      enabled: caps.canRollback,
      onClick: () => onAction("rollback"),
      reason: "Choose a non-active immutable deployment to roll back to",
    },
    {
      label: "Stop",
      enabled: caps.canStop,
      onClick: () => onAction("stop"),
      danger: true,
      reason: "Only a running active deployment can be stopped",
    },
    {
      label: "Restart",
      enabled: caps.canRestart,
      onClick: () => onAction("restart"),
      reason: "Only the active non-removed deployment can be restarted",
    },
    {
      label: "Remove",
      enabled: caps.canRemove,
      onClick: () => onAction("remove"),
      danger: true,
      reason: "Only an inactive non-removed deployment can be removed",
    },
    {
      label: "Cancel",
      enabled: caps.canCancel,
      onClick: onCancel,
      danger: true,
      reason: "Only queued or running work can be cancelled",
    },
  ];
  const hasEnabledAction = items.some((item) => item.enabled);

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Deployment actions"
        title={!hasEnabledAction ? unavailableReason : undefined}
        disabled={busy || !hasEnabledAction}
        onClick={() => setOpen((v) => !v)}
        className="grid h-8 w-8 place-items-center rounded-[var(--r-sm)] border border-line bg-surface-2 text-muted transition-colors hover:bg-surface-3 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        {busy ? (
          <span
            aria-hidden="true"
            className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line-strong border-t-accent"
          />
        ) : (
          <svg viewBox="0 0 16 16" className="h-4 w-4" fill="currentColor" aria-hidden="true">
            <circle cx="8" cy="3" r="1.4" />
            <circle cx="8" cy="8" r="1.4" />
            <circle cx="8" cy="13" r="1.4" />
          </svg>
        )}
      </button>

      {open ? (
        <div
          role="menu"
          className="absolute right-0 z-20 mt-1 min-w-[10rem] overflow-hidden rounded-[var(--r-sm)] border border-line-strong bg-surface-raised shadow-lg"
        >
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              disabled={!item.enabled}
              title={!item.enabled ? item.reason : undefined}
              onClick={() => run(item.onClick)}
              className={[
                "block w-full px-3 py-2 text-left text-xs transition-colors",
                "focus-visible:outline-none focus-visible:bg-surface-3",
                item.danger ? "text-danger" : "text-text",
                item.enabled ? "hover:bg-surface-3" : "cursor-not-allowed text-subtle opacity-50",
              ].join(" ")}
            >
              <span>{item.label}</span>
              {!item.enabled ? (
                <span className="ml-2 font-mono text-[9px] uppercase tracking-wider">
                  unavailable
                </span>
              ) : null}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
