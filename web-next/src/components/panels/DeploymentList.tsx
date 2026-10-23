// DeploymentList — activity/timeline of a service's deployments.
//
// PROP CONTRACT (stable — consumed by the drawer and the activity feed):
//   serviceId       required
//   environmentId?  env scope (threads into the query key)
//   onSelect?       (deployment) => void  — open the DeploymentDetail sub-view
//   compact?        dense rows for the drawer vs. the full feed page
//   selectedId?     highlight the row currently open in a detail pane
//
// Read path is REAL (`GET /services/{id}/deployments` via `useDeployments`).
// Row status routes through `deploymentStatus()` (StatusPill) so a deploy
// looks the same here, on the canvas, and in the drawer.

import type { DeploymentResource } from "@/api/client";
import { useDeployments } from "@/api/hooks";
import { StatusPill } from "@/components/StatusPill";
import { Loading, Empty, ErrorState, ActionButton } from "@/components/states";
import {
  deploySubject,
  formatRelative,
} from "./deployments/format";
import { PreviewEnvironmentCapability } from "./previews/PreviewEnvironmentCapability";

export type DeploymentListProps = {
  serviceId: string;
  environmentId?: string;
  onSelect?: (deployment: DeploymentResource) => void;
  compact?: boolean;
  selectedId?: string;
};

export function DeploymentList({
  serviceId,
  environmentId,
  onSelect,
  compact,
  selectedId,
}: DeploymentListProps) {
  const { data, error, isPending, refetch } = useDeployments(serviceId, environmentId);

  if (isPending) return <Loading label="Loading deployments…" />;
  if (error)
    return <ErrorState title="Failed to load deployments" error={error} onRetry={() => refetch()} />;
  return (
    <section className="grid gap-3" aria-labelledby="deployment-timeline-title">
      <header className="grid gap-0.5">
        <h2
          id="deployment-timeline-title"
          className="m-0 text-sm font-semibold text-text"
        >
          Release timeline
        </h2>
        <p className="m-0 text-xs text-muted">
          Deployment history in the order reported by the server.
        </p>
      </header>
      <PreviewEnvironmentCapability serviceId={serviceId} />
      {!data || data.length === 0 ? (
        <Empty
          title="No deployments yet"
          detail="Trigger a deploy to populate the timeline."
          action={<ActionButton onClick={() => refetch()}>Check again</ActionButton>}
        />
      ) : (
        <ol
          className="deployment-timeline m-0 grid min-w-0 list-none p-0"
          aria-label="Deployment timeline"
        >
          {data.map((d) => (
            <li key={d.id} className="deployment-timeline-item min-w-0">
              <DeploymentRow
                deployment={d}
                compact={compact}
                selected={selectedId === d.id}
                onSelect={onSelect}
              />
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function DeploymentRow({
  deployment: d,
  compact,
  selected,
  onSelect,
}: {
  deployment: DeploymentResource;
  compact?: boolean;
  selected?: boolean;
  onSelect?: (deployment: DeploymentResource) => void;
}) {
  const deploymentId = d.deployment_id || d.id;
  return (
    <button
      type="button"
      onClick={() => onSelect?.(d)}
      aria-current={selected ? "true" : undefined}
      aria-label={`Inspect deployment ${deploymentId}`}
      className={[
        "flex w-full min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded border bg-surface-2 px-4 text-left text-sm transition-colors",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
        compact ? "py-2" : "py-3",
        selected ? "border-accent" : "border-line hover:border-line-strong",
      ].join(" ")}
    >
      <StatusPill
        className="shrink-0"
        signal={{ status: d.status, operation_state: d.operation_state, active: d.active }}
      />

      <span className="flex min-w-0 flex-1 basis-40 flex-col gap-0.5">
        <span className="truncate font-mono text-xs text-text">{deploySubject(d)}</span>
        <span
          className="truncate font-mono text-[10px] text-subtle"
          title={`${deploymentId} · ${d.operation_id}`}
        >
          {deploymentId} · {d.operation_id}
        </span>
        {!compact && d.route ? (
          <span className="truncate text-[11px] text-muted">
            {d.route}
          </span>
        ) : null}
        {!compact && (d.source_type || d.commit_sha) ? (
          <span className="truncate text-[11px] text-muted">
            {[d.source_type, d.commit_sha?.slice(0, 10)].filter(Boolean).join(" · ")}
          </span>
        ) : null}
      </span>

      <span className="ml-auto flex shrink-0 items-center gap-2">
        {d.active ? (
          <span className="rounded-full border border-accent/50 bg-accent-soft px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-accent">
            active
          </span>
        ) : null}
        <span className="whitespace-nowrap text-[11px] text-subtle">
          {formatRelative(d.last_event_at)}
        </span>
      </span>
    </button>
  );
}
