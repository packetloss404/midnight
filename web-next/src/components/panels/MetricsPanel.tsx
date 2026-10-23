// MetricsPanel — runtime inventory plus durable resource history for a service.
//
// PROP CONTRACT (stable):
//   serviceId       required
//   environmentId?  environment scope for durable resource samples
//   range?          time window "5m" | "15m" | "1h" (default "15m")
//   runtimeInventoryAvailable? whether the installation-wide compatibility
//                              snapshot is authorized for this session
//
// Runtime state comes from the inventory endpoint. CPU, memory, network,
// block-I/O, and writable-layer history comes from the scoped observability
// endpoint. Missing collectors/samples remain explicit unavailable states.

import { useState, type ReactNode } from "react";
import { Loading, ErrorState } from "@/components/states";
import { SparkLine } from "./metrics/SparkLine";
import {
  RANGE_WINDOW_MS,
  useHistoricalServiceMetrics,
  useDerivedServiceMetrics,
  type HistoricalMetricSeries,
  type MetricRange,
  type ReplicaCount,
  type ReplicaSample,
  type RuntimeService,
} from "./metrics/api";

export type MetricsPanelProps = {
  projectId?: string;
  serviceId: string;
  environmentId?: string;
  range?: MetricRange;
  runtimeInventoryAvailable?: boolean;
};

const RANGES: MetricRange[] = ["5m", "15m", "1h"];

export function MetricsPanel({
  projectId,
  serviceId,
  environmentId,
  range = "15m",
  runtimeInventoryAvailable = true,
}: MetricsPanelProps) {
  const [activeRange, setActiveRange] = useState<MetricRange>(range);
  const { service, replicas, samples, generatedAt, isPending, isFetching, error, refetch } =
    useDerivedServiceMetrics(serviceId, activeRange, runtimeInventoryAvailable);
  const historical = useHistoricalServiceMetrics(
    projectId,
    environmentId,
    serviceId,
    activeRange,
  );

  if (isPending) return <Loading label="Loading runtime metrics…" />;
  if (error)
    return <ErrorState title="Failed to load runtime inventory" error={error} onRetry={refetch} />;

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-[11px] text-subtle">
          {runtimeInventoryAvailable ? (
            <>
              <span
                className="h-1.5 w-1.5 rounded-full"
                style={{ background: isFetching ? "var(--accent)" : "var(--subtle)" }}
                aria-hidden="true"
              />
              <span>{isFetching ? "polling" : "live"}</span>
              {generatedAt ? (
                <span className="font-mono">· snapshot {new Date(generatedAt).toLocaleTimeString()}</span>
              ) : null}
            </>
          ) : (
            <span>Recorded history for this project</span>
          )}
        </div>
        <RangeSelector active={activeRange} onChange={setActiveRange} />
      </div>

      {!runtimeInventoryAvailable ? null : !service ? (
        <div className="rounded-lg border border-dashed border-line bg-bg-2 px-4 py-6 text-center text-sm text-muted">
          <strong className="block text-text">Service not in the runtime snapshot</strong>
          <span>
            This service has no live runtime record yet. Metrics appear once it has been deployed and
            reports into <span className="font-mono text-subtle">/dashboard/runtime-inventory</span>.
          </span>
        </div>
      ) : (
        <RealTiles service={service} replicas={replicas} samples={samples} range={activeRange} />
      )}

      <ResourceHistory
        projectId={projectId}
        series={historical.data}
        pending={historical.isPending}
        error={historical.error}
        onRetry={() => historical.refetch()}
      />
    </div>
  );
}

// ── Range selector ────────────────────────────────────────────────

function RangeSelector({
  active,
  onChange,
}: {
  active: MetricRange;
  onChange: (r: MetricRange) => void;
}) {
  return (
    <div
      role="group"
      aria-label="Time range"
      className="inline-flex overflow-hidden rounded-[var(--r-sm)] border border-line"
    >
      {RANGES.map((r) => (
        <button
          key={r}
          type="button"
          onClick={() => onChange(r)}
          aria-pressed={active === r}
          className={
            "px-2.5 py-1 text-xs font-semibold transition-colors " +
            (active === r
              ? "bg-accent-soft text-accent"
              : "bg-surface-2 text-muted hover:bg-surface-3")
          }
        >
          {r}
        </button>
      ))}
    </div>
  );
}

// ── Real, derived tiles ───────────────────────────────────────────

function RealTiles({
  service,
  replicas,
  samples,
  range,
}: {
  service: RuntimeService;
  replicas: ReplicaCount | null;
  samples: ReplicaSample[];
  range: MetricRange;
}) {
  const spark = samples.map((s) => ({ t: s.t, v: s.running }));
  const windowMin = Math.round(RANGE_WINDOW_MS[range] / 60_000);

  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <Tile
        label="Replicas"
        value={replicas ? `${replicas.running}/${replicas.desired}` : service.replicas || "—"}
        hint={
          spark.length > 0
            ? `${spark.length} sample${spark.length === 1 ? "" : "s"} · last ${windowMin}m`
            : "collecting samples…"
        }
        accent={1}
      >
        <SparkLine
          points={spark}
          colorIndex={1}
          label="Running replicas"
          formatValue={(v) => `${v} replica${v === 1 ? "" : "s"}`}
        />
        <p className="m-0 mt-1 text-[10px] text-subtle">
          Replica count observed from runtime inventory in this browser session.
        </p>
      </Tile>

      <Tile label="Health" value={service.health || "unknown"} hint="from runtime snapshot" accent={2}>
        <StateBar state={service.health} />
      </Tile>

      <Tile
        label="Runtime state"
        value={service.runtime_state || "unknown"}
        hint={service.last_event_at ? `updated ${service.updated}` : "from runtime snapshot"}
        accent={3}
      >
        {service.last_event ? (
          <p className="m-0 truncate font-mono text-[10px] text-subtle" title={service.last_event}>
            {service.last_event}
          </p>
        ) : null}
      </Tile>

      <Tile
        label="Route"
        value={service.route && service.route !== "not-promoted" ? service.route : "not promoted"}
        hint={service.route === "not-promoted" ? "no public route yet" : "public endpoint"}
        accent={4}
        mono={Boolean(service.route && service.route !== "not-promoted")}
      />
    </div>
  );
}

function Tile({
  label,
  value,
  hint,
  accent,
  mono,
  children,
}: {
  label: string;
  value: string;
  hint?: string;
  accent: 1 | 2 | 3 | 4;
  mono?: boolean;
  children?: ReactNode;
}) {
  return (
    <div className="grid gap-1.5 rounded-lg border border-line bg-surface-2 px-4 py-3">
      <div className="flex items-center gap-2">
        <span
          className="h-2 w-2 rounded-full"
          style={{ background: `var(--chart-${accent})` }}
          aria-hidden="true"
        />
        <span className="text-[10px] font-semibold uppercase tracking-wider text-subtle">{label}</span>
      </div>
      <div className={"text-lg font-semibold text-text " + (mono ? "font-mono text-sm break-all" : "")}>
        {value}
      </div>
      {children}
      {hint ? <p className="m-0 text-[10px] text-subtle">{hint}</p> : null}
    </div>
  );
}

// A tiny categorical state bar (not a metric) — a filled dot scale keyed to a
// coarse health class, purely to give the Health tile a glance-able mark.
function StateBar({ state }: { state: string }) {
  const s = state.toLowerCase();
  const tone =
    s.includes("health") || s === "active" || s === "ready"
      ? "var(--status-success)"
      : s.includes("deploy") || s.includes("queue") || s.includes("starting")
        ? "var(--status-warning)"
        : s.includes("fail") || s.includes("crash") || s.includes("unhealthy") || s.includes("lost")
          ? "var(--status-error)"
          : "var(--status-unknown)";
  return (
    <div className="flex items-center gap-1" aria-hidden="true">
      <span className="h-1.5 flex-1 rounded-full" style={{ background: tone }} />
    </div>
  );
}

// ── Durable historical resource charts ───────────────────────────

function ResourceHistory({
  projectId,
  series,
  pending,
  error,
  onRetry,
}: {
  projectId?: string;
  series?: HistoricalMetricSeries;
  pending: boolean;
  error: Error | null;
  onRetry: () => void;
}) {
  if (!projectId) {
    return <UnavailableResourceHistory reason="Open this service from inside a project to see its recorded history." />;
  }
  if (pending) return <Loading label="Loading historical resource metrics…" />;
  if (error) {
    return <ErrorState title="Historical metrics unavailable" error={error} onRetry={onRetry} />;
  }
  if (!series || !series.collectors.includes("docker")) {
    return <UnavailableResourceHistory reason="No Docker metrics collector has reported in the last two minutes." />;
  }
  if (series.points.length === 0) {
    return <UnavailableResourceHistory reason="The collector is active, but this scope has no samples in the selected range." />;
  }
  const points = series.points;
  const latest = points[points.length - 1];
  const chart = (value: (point: (typeof points)[number]) => number) =>
    points.map((point) => ({ t: Date.parse(point.observed_at), v: value(point) }));
  const diskAvailable = points.some((point) => point.disk_usage_bytes != null);

  return (
    <div className="grid gap-3">
      <div className="flex items-center gap-2">
        <span className="text-[10px] font-semibold uppercase tracking-wider text-subtle">
          Resource usage
        </span>
        <span className="rounded-full border border-line bg-surface px-2 py-0.5 font-mono text-[9px] uppercase tracking-wider text-subtle">
          recorded · {series.mode} · {series.step}
        </span>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <ResourceTile label="CPU" value={`${latest.cpu_percent.toFixed(1)}%`} hint={`${latest.replicas} replica${latest.replicas === 1 ? "" : "s"}`}>
          <SparkLine points={chart((point) => point.cpu_percent)} label="CPU utilization" formatValue={(value) => `${value.toFixed(1)}%`} />
        </ResourceTile>
        <ResourceTile label="Memory" value={formatBytes(latest.memory_bytes)} hint={latest.memory_limit_bytes > 0 ? `limit ${formatBytes(latest.memory_limit_bytes)}` : "limit not reported"}>
          <SparkLine points={chart((point) => point.memory_bytes)} colorIndex={2} label="Memory usage" formatValue={formatBytes} />
        </ResourceTile>
        <ResourceTile label="Network I/O" value={formatBytes(latest.network_rx_bytes + latest.network_tx_bytes)} hint={`rx ${formatBytes(latest.network_rx_bytes)} · tx ${formatBytes(latest.network_tx_bytes)}`}>
          <SparkLine points={chart((point) => point.network_rx_bytes + point.network_tx_bytes)} colorIndex={3} label="Network bytes" formatValue={formatBytes} />
        </ResourceTile>
        <ResourceTile
          label={diskAvailable ? "Writable disk" : "Block I/O"}
          value={
            diskAvailable && latest.disk_usage_bytes != null
              ? formatBytes(latest.disk_usage_bytes)
              : formatBytes(latest.block_read_bytes + latest.block_write_bytes)
          }
          hint={
            diskAvailable
              ? "container writable layer"
              : `read ${formatBytes(latest.block_read_bytes)} · write ${formatBytes(latest.block_write_bytes)}`
          }
        >
          <SparkLine
            points={chart((point) =>
              diskAvailable && point.disk_usage_bytes != null
                ? point.disk_usage_bytes
                : point.block_read_bytes + point.block_write_bytes,
            )}
            colorIndex={4}
            label={diskAvailable ? "Writable disk usage" : "Block I/O bytes"}
            formatValue={formatBytes}
          />
        </ResourceTile>
      </div>
      <p className="m-0 text-[11px] text-muted">
        Historical samples come from Docker stats and are retained for 30 days. Network and block
        values are cumulative counters; writable-layer disk is shown only when Docker reports it.
      </p>
    </div>
  );
}

function ResourceTile({
  label,
  value,
  hint,
  children,
}: {
  label: string;
  value: string;
  hint: string;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-1.5 rounded-lg border border-line bg-surface-2 px-4 py-3">
      <span className="text-[10px] font-semibold uppercase tracking-wider text-subtle">{label}</span>
      <div className="text-lg font-semibold text-text">{value}</div>
      {children}
      <p className="m-0 text-[10px] text-subtle">{hint}</p>
    </div>
  );
}

function UnavailableResourceHistory({ reason }: { reason: string }) {
  return (
    <div className="rounded-lg border border-dashed border-line bg-bg-2 px-4 py-4 text-sm text-muted">
      <strong className="block text-text">Historical resource metrics unavailable</strong>
      <span>{reason}</span>
    </div>
  );
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const scaled = value / 1024 ** index;
  return `${scaled >= 10 || index === 0 ? scaled.toFixed(0) : scaled.toFixed(1)} ${units[index]}`;
}
