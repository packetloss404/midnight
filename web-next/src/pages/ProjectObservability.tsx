// Project observability (`/projects/:projectId/observability`).
//
// A fixed, env-scoped widget grid — deliberately NOT a drag/resize editor:
//   • Fleet summary       (real, from /dashboard/summary)
//   • Runtime health rollup (real, from /dashboard/runtime-inventory)
//   • Service metrics     (MetricsPanel, when a service is scoped)
//   • Project log tail    (shared LogViewer, with a service→deployment
//                          operation selector so any operation is streamable)
//
// Env scoping threads through the service/operation pickers (which are
// env-aware). The runtime snapshot itself is fleet-wide (the API exposes no
// per-environment metrics), which the rollup labels honestly.

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useServices, useDeployments } from "@/api/hooks";
import { MetricsPanel } from "@/components/panels/MetricsPanel";
import { LogViewer } from "@/components/panels/LogViewer";
import { MonitorPanel } from "@/components/panels/MonitorPanel";
import { useDashboardSummary, useRuntimeInventory } from "@/components/panels/metrics/api";
import { presentError } from "@/lib/errorPresentation";

type Props = {
  projectId: string;
  environmentId?: string;
  serviceId?: string;
};

export const OBSERVABILITY_SCOPE_LABELS = {
  description:
    "The fleet summary is instance-wide; runtime and logs are project-scoped.",
  environmentNote: "Environment filters apply only to supported widgets.",
  fleetSummary: "instance-wide · all projects",
  runtimeHealth: "project-scoped · fleet snapshot source",
} as const;

export function ProjectObservability({ projectId, environmentId, serviceId }: Props) {
  return (
    <section className="flex w-full flex-col gap-4 p-[var(--main-gutter)]">
      <header>
        <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">Insight</p>
        <h1 className="m-0 mt-1 text-xl font-semibold tracking-tight">Observability</h1>
        <p className="m-0 mt-1 text-sm text-muted">
          {OBSERVABILITY_SCOPE_LABELS.description}
          {environmentId ? ` ${OBSERVABILITY_SCOPE_LABELS.environmentNote}` : ""}
        </p>
      </header>

      <div className="grid gap-4 lg:grid-cols-2">
        <Widget title="Fleet summary" subtitle={OBSERVABILITY_SCOPE_LABELS.fleetSummary}>
          <FleetSummary />
        </Widget>

        <Widget title="Runtime health" subtitle={OBSERVABILITY_SCOPE_LABELS.runtimeHealth}>
          <RuntimeRollup projectId={projectId} />
        </Widget>

        {serviceId ? (
          <Widget title="Service metrics" subtitle="scoped service" span2>
            <MetricsPanel projectId={projectId} serviceId={serviceId} environmentId={environmentId} />
          </Widget>
        ) : null}

        <Widget title="Metric monitors" subtitle="continuous checks · signed delivery" span2>
          <MonitorPanel
            projectId={projectId}
            environmentId={environmentId}
            serviceId={serviceId}
          />
        </Widget>

        <Widget title="Log tail" subtitle="stored deployment logs" span2>
          <ProjectLogTail projectId={projectId} environmentId={environmentId} serviceId={serviceId} />
        </Widget>
      </div>
    </section>
  );
}

// ── Widget shell ──────────────────────────────────────────────────

function Widget({
  title,
  subtitle,
  span2,
  children,
}: {
  title: string;
  subtitle?: string;
  span2?: boolean;
  children: ReactNode;
}) {
  return (
    <section
      className={
        "grid gap-3 rounded-lg border border-line bg-surface px-4 py-4 " +
        (span2 ? "lg:col-span-2" : "")
      }
    >
      <header className="flex items-baseline justify-between gap-2">
        <h2 className="m-0 text-sm font-semibold text-text">{title}</h2>
        {subtitle ? <span className="font-mono text-[10px] text-subtle">{subtitle}</span> : null}
      </header>
      {children}
    </section>
  );
}

// ── Fleet summary ─────────────────────────────────────────────────

function FleetSummary() {
  const { data, isPending, error } = useDashboardSummary();
  if (isPending) return <SkeletonRow />;
  if (error) return <WidgetError error={error} />;

  const tiles: { label: string; value: number | string; accent: 1 | 2 | 3 | 4 }[] = [
    { label: "Projects", value: data?.project_count ?? 0, accent: 1 },
    { label: "Services", value: data?.service_count ?? 0, accent: 2 },
    { label: "Activity", value: data?.operation_count ?? 0, accent: 3 },
    { label: "Status", value: data?.status ?? "—", accent: 4 },
  ];
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      {tiles.map((t) => (
        <div key={t.label} className="grid gap-1 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2.5">
          <span
            className="h-1.5 w-6 rounded-full"
            style={{ background: `var(--chart-${t.accent})` }}
            aria-hidden="true"
          />
          <span className="text-lg font-semibold text-text">{t.value}</span>
          <span className="text-[10px] uppercase tracking-wider text-subtle">{t.label}</span>
        </div>
      ))}
    </div>
  );
}

// ── Runtime health rollup ─────────────────────────────────────────

function healthClass(s: string): "healthy" | "progressing" | "failing" | "other" {
  const v = s.toLowerCase();
  if (v.includes("health") || v === "active" || v === "ready") return "healthy";
  if (v.includes("deploy") || v.includes("queue") || v.includes("starting") || v.includes("draining"))
    return "progressing";
  if (v.includes("fail") || v.includes("crash") || v.includes("unhealthy") || v.includes("lost"))
    return "failing";
  return "other";
}

const HEALTH_META: Record<string, { label: string; token: string }> = {
  healthy: { label: "Healthy", token: "var(--status-success)" },
  progressing: { label: "Progressing", token: "var(--status-warning)" },
  failing: { label: "Failing", token: "var(--status-error)" },
  other: { label: "Other", token: "var(--status-unknown)" },
};

function RuntimeRollup({ projectId }: { projectId: string }) {
  const { data, isPending, error } = useRuntimeInventory();
  const services = useMemo(
    () => (data?.services ?? []).filter((s) => !s.project_id || s.project_id === projectId),
    [data, projectId],
  );

  if (isPending) return <SkeletonRow />;
  if (error) return <WidgetError error={error} />;
  if (services.length === 0)
    return (
      <p className="m-0 text-sm text-muted">
        No services in the runtime snapshot for this project yet.
      </p>
    );

  const counts: Record<string, number> = { healthy: 0, progressing: 0, failing: 0, other: 0 };
  for (const s of services) counts[healthClass(s.health || s.runtime_state)] += 1;
  const total = services.length;

  return (
    <div className="grid gap-3">
      <div className="flex h-2 overflow-hidden rounded-full bg-bg-2" role="img" aria-label="Service health distribution">
        {(["healthy", "progressing", "failing", "other"] as const).map((k) =>
          counts[k] > 0 ? (
            <span
              key={k}
              style={{ width: `${(counts[k] / total) * 100}%`, background: HEALTH_META[k].token }}
              title={`${HEALTH_META[k].label}: ${counts[k]}`}
            />
          ) : null,
        )}
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {(["healthy", "progressing", "failing", "other"] as const).map((k) => (
          <div key={k} className="flex items-center gap-2 text-xs">
            <span className="h-2 w-2 rounded-full" style={{ background: HEALTH_META[k].token }} aria-hidden="true" />
            <span className="text-text">{counts[k]}</span>
            <span className="text-subtle">{HEALTH_META[k].label}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// ── Project log tail (operation selectable) ───────────────────────

function ProjectLogTail({
  projectId,
  environmentId,
  serviceId,
}: {
  projectId: string;
  environmentId?: string;
  serviceId?: string;
}) {
  const servicesQuery = useServices(projectId, environmentId);
  const services = servicesQuery.data ?? [];
  const [selectedService, setSelectedService] = useState<string | undefined>(serviceId);

  useEffect(() => {
    if (selectedService) return;
    if (serviceId) setSelectedService(serviceId);
    else if (services.length > 0) setSelectedService(services[0].id);
  }, [serviceId, services, selectedService]);

  const deploymentsQuery = useDeployments(selectedService, environmentId);
  const deployments = deploymentsQuery.data ?? [];
  const [selectedOp, setSelectedOp] = useState<string | undefined>(undefined);
  const selectedDeployment = deployments.find((deployment) => deployment.operation_id === selectedOp);

  // Default to the newest deployment's operation once deployments load.
  useEffect(() => {
    if (selectedOp || deployments.length === 0) return;
    const withOp = deployments.find((d) => d.operation_id);
    if (withOp?.operation_id) setSelectedOp(withOp.operation_id);
  }, [deployments, selectedOp]);

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Picker
          label="Service"
          value={selectedService ?? ""}
          onChange={(v) => {
            setSelectedService(v);
            setSelectedOp(undefined);
          }}
          options={services.map((s) => ({ value: s.id, label: s.display_name || s.slug }))}
          empty={servicesQuery.isPending ? "loading…" : "no services"}
        />
        <Picker
          label="Deployment"
          value={selectedOp ?? ""}
          onChange={setSelectedOp}
          options={deployments
            .filter((d) => d.operation_id)
            .map((d) => ({
              value: d.operation_id,
              label: `${d.status} · ${(d.deployment_id || d.id).slice(0, 12)}`,
            }))}
          empty={deploymentsQuery.isPending ? "loading…" : "no deployments"}
        />
      </div>
      <LogViewer
        operationId={selectedOp}
        height={260}
        scope={
          selectedService
            ? {
                projectId,
                environmentId,
                serviceId: selectedService,
                deploymentId: selectedDeployment?.deployment_id || selectedDeployment?.id,
              }
            : undefined
        }
      />
    </div>
  );
}

function Picker({
  label,
  value,
  onChange,
  options,
  empty,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
  empty: string;
}) {
  return (
    <label className="flex items-center gap-2 text-xs">
      <span className="font-mono text-[10px] uppercase tracking-wider text-subtle">{label}</span>
      {options.length === 0 ? (
        <span className="text-muted">{empty}</span>
      ) : (
        <select
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="max-w-[220px] rounded-[var(--r-sm)] border border-line bg-surface px-2 py-1 text-xs text-text"
        >
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      )}
    </label>
  );
}

// ── Small shared bits ─────────────────────────────────────────────

function SkeletonRow() {
  return (
    <div className="flex items-center gap-2 py-4 text-xs text-subtle" role="status" aria-live="polite">
      <span className="h-3 w-3 animate-spin rounded-full border-2 border-line-strong border-t-accent" />
      Loading…
    </div>
  );
}

function WidgetError({ error }: { error: unknown }) {
  return (
    <div role="alert" className="rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger">
      {presentError(error).message}
    </div>
  );
}
