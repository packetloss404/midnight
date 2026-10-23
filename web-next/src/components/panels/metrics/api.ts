// Panel-local data layer for MetricsPanel.
//
// HONESTY CONTRACT: service state and desired/running replica counts come from
// the point-in-time runtime inventory. Resource history comes only from the
// durable scoped observability metrics endpoint. The in-session replica series
// remains clearly labeled and missing collector/sample states stay explicit.

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

// Subset of `dashboardRuntimeService` (internal/api/dashboard.go) we consume.
export type RuntimeService = {
  id: string;
  project_id?: string;
  slug?: string;
  name: string;
  kind: string;
  runtime_state: string;
  replicas: string;
  route: string;
  health: string;
  updated: string;
  operation_id?: string;
  operation_state?: string;
  last_event?: string;
  last_event_at?: string;
};

export type RuntimeInventory = {
  schema_version: string;
  generated_at: string;
  services: RuntimeService[];
};

export type DashboardSummary = {
  status?: string;
  server_version?: string;
  project_count?: number;
  service_count?: number;
  operation_count?: number;
};

export type MetricRange = "5m" | "15m" | "1h";

export type HistoricalMetricPoint = {
  observed_at: string;
  replica_id?: string;
  cpu_percent: number;
  memory_bytes: number;
  memory_limit_bytes: number;
  network_rx_bytes: number;
  network_tx_bytes: number;
  block_read_bytes: number;
  block_write_bytes: number;
  disk_usage_bytes?: number;
  restart_count: number;
  replicas: number;
  samples: number;
};

export type HistoricalMetricSeries = {
  mode: "sum" | "replica";
  step: string;
  points: HistoricalMetricPoint[];
  deployment_markers: {
    deployment_id: string;
    marker: string;
    observed_at: string;
  }[];
  collectors: string[];
};

export const RANGE_WINDOW_MS: Record<MetricRange, number> = {
  "5m": 5 * 60_000,
  "15m": 15 * 60_000,
  "1h": 60 * 60_000,
};

const POLL_INTERVAL_MS = 10_000;
const MAX_RETAIN_MS = RANGE_WINDOW_MS["1h"];

export const metricsKeys = {
  runtimeInventory: () => ["dashboard", "runtime-inventory"] as const,
  summary: () => ["dashboard", "summary"] as const,
  historical: (
    projectId: string | undefined,
    environmentId: string | undefined,
    serviceId: string | undefined,
    range: MetricRange,
    mode: "sum" | "replica",
  ) => ["observability", "metrics", projectId, environmentId, serviceId, range, mode] as const,
};

// ── Replica parsing ───────────────────────────────────────────────
// Replica strings look like "1/1", "0/1 starting", "0/0". We only trust the
// numeric running/desired pair; the trailing word is a state hint we ignore.

export type ReplicaCount = { running: number; desired: number };

export function parseReplicas(raw: string | undefined): ReplicaCount | null {
  if (!raw) return null;
  const m = raw.match(/(\d+)\s*\/\s*(\d+)/);
  if (!m) return null;
  return { running: Number(m[1]), desired: Number(m[2]) };
}

// ── In-session sample store ───────────────────────────────────────
// Module-level so samples survive tab switches / panel remounts within the
// session. Pruned to the widest supported window so memory stays bounded.

export type ReplicaSample = { t: number; running: number; desired: number };

const sampleStore = new Map<string, ReplicaSample[]>();

function pushSample(serviceId: string, at: number, count: ReplicaCount): ReplicaSample[] {
  const series = sampleStore.get(serviceId) ?? [];
  const last = series[series.length - 1];
  // De-dupe by snapshot timestamp so repeated cache reads don't stack points.
  if (last && last.t === at) return series;
  const next = [...series, { t: at, running: count.running, desired: count.desired }];
  const cutoff = at - MAX_RETAIN_MS;
  const pruned = next.filter((s) => s.t >= cutoff);
  sampleStore.set(serviceId, pruned);
  return pruned;
}

// ── Hooks ─────────────────────────────────────────────────────────

export function useRuntimeInventory(enabled = true) {
  return useQuery<RuntimeInventory, Error>({
    queryKey: metricsKeys.runtimeInventory(),
    queryFn: ({ signal }) =>
      apiFetch<RuntimeInventory>("/dashboard/runtime-inventory", { signal }),
    enabled,
    refetchInterval: enabled ? POLL_INTERVAL_MS : false,
  });
}

export function useDashboardSummary(enabled = true) {
  return useQuery<DashboardSummary, Error>({
    queryKey: metricsKeys.summary(),
    queryFn: ({ signal }) => apiFetch<DashboardSummary>("/dashboard/summary", { signal }),
    enabled,
    refetchInterval: enabled ? POLL_INTERVAL_MS : false,
  });
}

export function useHistoricalServiceMetrics(
  projectId: string | undefined,
  environmentId: string | undefined,
  serviceId: string | undefined,
  range: MetricRange,
  mode: "sum" | "replica" = "sum",
) {
  return useQuery<HistoricalMetricSeries, Error>({
    queryKey: metricsKeys.historical(projectId, environmentId, serviceId, range, mode),
    queryFn: ({ signal }) => {
      const params = new URLSearchParams({
        project_id: projectId as string,
        service_id: serviceId as string,
        mode,
        since: new Date(Date.now() - RANGE_WINDOW_MS[range]).toISOString(),
      });
      if (environmentId) params.set("environment_id", environmentId);
      return apiFetch<HistoricalMetricSeries>(`/observability/metrics?${params.toString()}`, {
        signal,
      });
    },
    enabled: Boolean(projectId && serviceId),
    refetchInterval: projectId && serviceId ? POLL_INTERVAL_MS : false,
  });
}

export type DerivedMetrics = {
  /** The matched runtime snapshot for this service, if present. */
  service: RuntimeService | undefined;
  replicas: ReplicaCount | null;
  /** In-session observed replica series, clipped to the selected range. */
  samples: ReplicaSample[];
  generatedAt: string | undefined;
  isPending: boolean;
  isFetching: boolean;
  error: Error | null;
  refetch: () => void;
};

/**
 * Real, honest metrics for one service derived from the runtime snapshot,
 * plus an in-session replica series accumulated as the snapshot is polled.
 */
export function useDerivedServiceMetrics(
  serviceId: string | undefined,
  range: MetricRange,
  enabled = true,
): DerivedMetrics {
  const query = useRuntimeInventory(enabled && Boolean(serviceId));
  const [tick, setTick] = useState(0);

  const service = useMemo(
    () => query.data?.services.find((s) => s.id === serviceId),
    [query.data, serviceId],
  );

  const generatedAt = query.data?.generated_at;

  // Accumulate the observed replica reading into the in-session series.
  useEffect(() => {
    if (!serviceId || !service || !generatedAt) return;
    const count = parseReplicas(service.replicas);
    if (!count) return;
    const at = Date.parse(generatedAt);
    if (Number.isNaN(at)) return;
    pushSample(serviceId, at, count);
    setTick((n) => n + 1);
  }, [serviceId, service, generatedAt]);

  const samples = useMemo(() => {
    if (!serviceId) return [];
    const all = sampleStore.get(serviceId) ?? [];
    if (all.length === 0) return all;
    const newest = all[all.length - 1].t;
    const cutoff = newest - RANGE_WINDOW_MS[range];
    return all.filter((s) => s.t >= cutoff);
    // `tick` bumps after the sampling effect appends, so the newest observed
    // point is reflected in the same commit rather than one render late.
  }, [serviceId, range, tick]);

  return {
    service,
    replicas: parseReplicas(service?.replicas),
    samples,
    generatedAt,
    isPending: enabled && query.isPending,
    isFetching: query.isFetching,
    error: enabled ? query.error : null,
    refetch: () => query.refetch(),
  };
}
