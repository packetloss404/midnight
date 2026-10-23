// Project activity feed (`/projects/:projectId/deployments`).
//
// Env-scoped, cross-service deployment activity: every service's deployments
// merged into one newest-first feed with service / status / trigger filters,
// near-real-time updates (per-service polling while in-flight + a live
// event-stream banner for the newest running deploy), and cross-linking into
// the `DeploymentDetail` sub-view.

import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import type { DeploymentResource, Service } from "@/api/client";
import { useOperationEvents, useServices } from "@/api/hooks";
import { useAuth } from "@/auth/AuthProvider";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { deploymentStatus } from "@/lib/deploymentStatus";
import { StatusPill } from "@/components/StatusPill";
import { DeploymentDetail } from "@/components/panels/DeploymentDetail";
import { Loading, Empty, ErrorState, ActionButton } from "@/components/states";
import { useProjectDeploymentsFeed } from "@/components/panels/deployments/api";
import {
  TRIGGER_LABEL,
  TriggerIcon,
  deriveTrigger,
  deploySubject,
  formatRelative,
  type TriggerKind,
} from "@/components/panels/deployments/format";

type Props = {
  projectId: string;
  environmentId?: string;
  serviceId?: string;
};

const STATUS_OPTIONS = ["active", "succeeded", "deploying", "queued", "failed", "canceled"];

export function ProjectDeployments({ projectId, environmentId, serviceId }: Props) {
  const navigate = useNavigate();
  const auth = useAuth();
  const humanSession = auth.state.status === "human";
  const access = useProjectAccess(projectId, environmentId, humanSession);
  const exactHumanAccess =
    access.data?.project_id === projectId &&
    access.data?.environment_id === environmentId;
  const canReadServices =
    !humanSession ||
    (!access.isPending &&
      !access.error &&
      exactHumanAccess &&
      access.data?.permissions.includes("service.read") === true);
  const services = useServices(projectId, environmentId, {
    enabled: canReadServices,
  });
  const serviceList = useMemo<Service[]>(() => services.data ?? [], [services.data]);

  const feed = useProjectDeploymentsFeed(serviceList, environmentId);

  const [serviceFilter, setServiceFilter] = useState<string>(serviceId ?? "all");
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const [triggerFilter, setTriggerFilter] = useState<string>("all");
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const filtered = useMemo(
    () =>
      feed.items.filter(({ service, deployment }) => {
        if (serviceFilter !== "all" && service.id !== serviceFilter) return false;
        if (statusFilter !== "all" && deploymentStatus(deployment).label !== statusFilter) return false;
        if (triggerFilter !== "all" && deriveTrigger(deployment) !== triggerFilter) return false;
        return true;
      }),
    [feed.items, serviceFilter, statusFilter, triggerFilter],
  );

  const selected = useMemo(
    () => feed.items.find((it) => it.deployment.id === selectedId) ?? null,
    [feed.items, selectedId],
  );

  // Newest in-flight deployment drives the live event banner.
  const live = useMemo(
    () =>
      filtered.find(({ deployment }) =>
        ["queued", "running"].includes((deployment.operation_state ?? "").toLowerCase()),
      ) ?? null,
    [filtered],
  );

  return (
    <section className="flex w-full flex-col gap-4 p-[var(--main-gutter)]">
      <header className="flex items-end justify-between gap-3">
        <div>
          <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">Activity</p>
          <h1 className="m-0 mt-1 text-xl font-semibold tracking-tight">Deployments</h1>
        </div>
        {feed.isFetching ? (
          <span className="flex items-center gap-1.5 text-[11px] text-subtle">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-accent" />
            refreshing
          </span>
        ) : null}
      </header>

      {humanSession && access.isPending ? (
        <Loading label="Checking deployment access…" />
      ) : humanSession && access.error ? (
        <ErrorState
          title="Could not verify deployment access"
          error={access.error}
          onRetry={() => access.refetch()}
        />
      ) : humanSession && !canReadServices ? (
        <CapabilityBoundary
          state="unavailable"
          title="Deployment history"
          detail="Your current project role does not grant service and deployment reads for this environment."
        />
      ) : services.isPending ? (
        <Loading label="Loading services…" />
      ) : services.error ? (
        <ErrorState error={services.error} onRetry={() => services.refetch()} />
      ) : serviceList.length === 0 ? (
        <Empty
          title="No services to show activity for"
          detail="Add a service to this project to start seeing deployments."
          action={
            <ActionButton
              onClick={() =>
                navigate({
                  to: "/projects/$projectId",
                  params: { projectId },
                  search: { env: environmentId },
                })
              }
            >
              Open canvas
            </ActionButton>
          }
        />
      ) : (
        <>
          <Filters
            services={serviceList}
            service={serviceFilter}
            status={statusFilter}
            trigger={triggerFilter}
            onService={setServiceFilter}
            onStatus={setStatusFilter}
            onTrigger={setTriggerFilter}
          />

          {live ? (
            <LiveActivityBanner
              operationId={live.deployment.operation_id}
              subject={`${live.service.display_name || live.service.slug} · ${deploySubject(live.deployment)}`}
            />
          ) : null}

          <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
            <div className="order-2 lg:order-1">
              {feed.isPending ? (
                <Loading label="Loading deployments…" />
              ) : feed.error && feed.items.length === 0 ? (
                <ErrorState
                  title="Failed to load deployments"
                  error={feed.error}
                  onRetry={feed.refetchAll}
                />
              ) : filtered.length === 0 ? (
                <Empty
                  title="No matching deployments"
                  detail="Nothing matches the current filters yet."
                  action={
                    <ActionButton
                      onClick={() => {
                        setServiceFilter("all");
                        setStatusFilter("all");
                        setTriggerFilter("all");
                      }}
                    >
                      Clear filters
                    </ActionButton>
                  }
                />
              ) : (
                <ul className="m-0 grid list-none gap-2 p-0">
                  {filtered.map(({ service, deployment }) => (
                    <li key={`${service.id}:${deployment.id}`}>
                      <FeedRow
                        service={service}
                        deployment={deployment}
                        selected={selectedId === deployment.id}
                        onSelect={() => setSelectedId(deployment.id)}
                      />
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <div className="order-1 lg:order-2">
              {selected ? (
                <div className="rounded-lg border border-line bg-bg-2 p-4 lg:sticky lg:top-4">
                  <DeploymentDetail
                    deployment={selected.deployment}
                    environmentId={environmentId}
                    onBack={() => setSelectedId(null)}
                  />
                </div>
              ) : (
                <div className="hidden place-items-center rounded-lg border border-dashed border-line bg-bg-2 px-6 py-10 text-center text-sm text-subtle lg:grid">
                  Select a deployment to inspect metadata, actions, and logs.
                </div>
              )}
            </div>
          </div>
        </>
      )}
    </section>
  );
}

// ── Filters ────────────────────────────────────────────────────────────────

function Filters({
  services,
  service,
  status,
  trigger,
  onService,
  onStatus,
  onTrigger,
}: {
  services: Service[];
  service: string;
  status: string;
  trigger: string;
  onService: (v: string) => void;
  onStatus: (v: string) => void;
  onTrigger: (v: string) => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <FilterSelect label="Service" value={service} onChange={onService}>
        <option value="all">All services</option>
        {services.map((s) => (
          <option key={s.id} value={s.id}>
            {s.display_name || s.slug}
          </option>
        ))}
      </FilterSelect>
      <FilterSelect label="Status" value={status} onChange={onStatus}>
        <option value="all">All statuses</option>
        {STATUS_OPTIONS.map((s) => (
          <option key={s} value={s}>
            {s}
          </option>
        ))}
      </FilterSelect>
      <FilterSelect label="Trigger" value={trigger} onChange={onTrigger}>
        <option value="all">All triggers</option>
        {(["github", "cli", "manual"] as TriggerKind[]).map((t) => (
          <option key={t} value={t}>
            {TRIGGER_LABEL[t]}
          </option>
        ))}
      </FilterSelect>
    </div>
  );
}

function FilterSelect({
  label,
  value,
  onChange,
  children,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  children: ReactNode;
}) {
  return (
    <label className="flex items-center gap-1.5 text-[11px] text-subtle">
      <span className="uppercase tracking-wider">{label}</span>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-[var(--r-sm)] border border-line bg-surface-2 px-2 py-1 text-xs text-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        {children}
      </select>
    </label>
  );
}

// ── Feed row ─────────────────────────────────────────────────────────────

function FeedRow({
  service,
  deployment: d,
  selected,
  onSelect,
}: {
  service: Service;
  deployment: DeploymentResource;
  selected: boolean;
  onSelect: () => void;
}) {
  const trigger = deriveTrigger(d);
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected ? "true" : undefined}
      className={[
        "flex w-full items-center gap-3 rounded border bg-surface-2 px-4 py-3 text-left text-sm transition-colors",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
        selected ? "border-accent" : "border-line hover:border-line-strong",
      ].join(" ")}
    >
      <StatusPill signal={{ status: d.status, operation_state: d.operation_state, active: d.active }} />
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex items-center gap-2">
          <span className="truncate text-xs font-semibold text-text">
            {service.display_name || service.slug}
          </span>
          <span className="truncate font-mono text-[11px] text-muted">{deploySubject(d)}</span>
        </span>
        <span className="flex items-center gap-2 text-[11px] text-subtle">
          <span className="inline-flex items-center gap-1" title={`Triggered via ${TRIGGER_LABEL[trigger]}`}>
            <TriggerIcon kind={trigger} className="h-3 w-3" />
            {TRIGGER_LABEL[trigger]}
          </span>
          {d.route ? <span className="truncate text-muted">· {d.route}</span> : null}
        </span>
      </span>
      {d.active ? (
        <span className="shrink-0 rounded-full border border-accent/50 bg-accent-soft px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-accent">
          active
        </span>
      ) : null}
      <span className="shrink-0 whitespace-nowrap text-[11px] text-subtle">
        {formatRelative(d.last_event_at)}
      </span>
    </button>
  );
}

// ── Live event banner ─────────────────────────────────────────────────────

function LiveActivityBanner({ operationId, subject }: { operationId: string; subject: string }) {
  const { events, connected } = useOperationEvents(operationId);
  const latest = events.length > 0 ? events[events.length - 1] : null;
  return (
    <div className="flex items-center gap-2 rounded-lg border border-info/40 bg-info-soft px-3 py-2 text-xs">
      <span
        className={["h-2 w-2 rounded-full", connected ? "animate-pulse bg-info" : "bg-subtle"].join(" ")}
        aria-hidden="true"
      />
      <span className="truncate font-semibold text-text">{subject}</span>
      <span className="truncate text-muted" aria-live="polite">
        {latest ? `[${latest.state}] ${latest.message ?? ""}` : "deploy in progress…"}
      </span>
    </div>
  );
}
