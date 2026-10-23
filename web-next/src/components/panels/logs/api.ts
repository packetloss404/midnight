// Panel-local data layer for the LogViewer.
//
// The LogViewer unifies two backend sources into one normalized `LogLine`
// stream:
//
//   1. REST history — `GET /operations/{id}/logs` returns a point-in-time
//      snapshot of operation events plus app/platform log surfaces
//      (`build`, `runtime`, `http`, `platform`). Each surface carries a list
//      of structured `Entry` records. Fetched via the shared `apiFetch`.
//
//   2. SSE live tail — `GET /operations/{id}/events?stream=sse` streams
//      `OperationEvent` frames. That transport is owned by the shared
//      `useOperationEvents` hook (see `@/api/hooks`); this file only supplies
//      the normalizer that turns those events into the same `LogLine` shape.
//
// Keeping normalization here (not in the component) means the merge in
// `LogViewer` is a plain sort over one homogeneous type.

import { useEffect, useState } from "react";
import { useQuery, type UseQueryOptions } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";
import { connectSSE } from "@/api/sse";
import type { OperationEvent } from "@/api/client";
import type { LogLevel } from "./filter";

// ── Wire types — mirror `internal/api/logs.go` + `internal/logs/entry.go` ──

/** One structured log record. Mirrors `logrecords.Entry`. */
export interface LogEntry {
  at: string;
  level: string;
  component: string;
  message: string;
  fields?: Record<string, unknown>;
}

/** A single log surface within the operation-logs response. */
export interface LogSurface {
  surface: string;
  available: boolean;
  reason?: string;
  reason_code?: string;
  recovery_hint?: string;
  count?: number;
  entries?: LogEntry[];
  next_cursor?: unknown;
  truncated?: boolean;
}

interface OperationEventsRef {
  available: boolean;
  kind: string;
  url: string;
  count: number;
}

/** Response body of `GET /operations/{id}/logs`. */
export interface OperationLogsResponse {
  operation_id: string;
  operation_events: OperationEventsRef;
  deploy_logs: OperationEventsRef;
  app_logs: LogSurface[];
  platform_logs: LogSurface[];
  message: string;
}

export type ObservabilityLogScope = {
  projectId: string;
  environmentId?: string;
  serviceId?: string;
  deploymentId?: string;
  operationId?: string;
  replicaId?: string;
};

export interface ObservabilityLogRecord {
  observed_at: string;
  scope: {
    project_id: string;
    environment_id?: string;
    service_id?: string;
    deployment_id?: string;
    operation_id?: string;
    replica_id?: string;
  };
  surface: string;
  stream?: string;
  level: string;
  component: string;
  message: string;
  attributes?: Record<string, unknown>;
}

export interface ObservabilityLogPage {
  records: ObservabilityLogRecord[];
  truncated: boolean;
  dropped_at_ingest: number;
  next_cursor?: { token: string };
}

// ── Normalized model ────────────────────────────────────────────────

export type LogOrigin = "log" | "event";

/** Homogeneous line the viewer renders and filters over. */
export interface LogLine {
  id: string;
  /** RFC3339 timestamp; may be empty for events without one. */
  ts: string;
  /** Millis for sorting; 0 when the timestamp is missing/unparseable. */
  tsMs: number;
  level: LogLevel;
  /** Component or surface the line came from. */
  source: string;
  surface: string;
  message: string;
  fields: Record<string, unknown>;
  origin: LogOrigin;
  /** Monotonic sequence for SSE events; used as a sort tiebreak. */
  seq?: number;
}

// ── Normalizers ─────────────────────────────────────────────────────

/** Map a raw level/state string onto the viewer's four render levels. */
export function normalizeLevel(raw: string | undefined): LogLevel {
  const v = (raw ?? "").toLowerCase();
  if (/err|fail|fatal|panic|denied|reject/.test(v)) return "error";
  if (/warn/.test(v)) return "warn";
  if (/debug|trace|verbose/.test(v)) return "debug";
  return "info";
}

function parseTsMs(ts: string): number {
  if (!ts) return 0;
  const ms = Date.parse(ts);
  return Number.isNaN(ms) ? 0 : ms;
}

function entryToLine(entry: LogEntry, surface: string, index: number): LogLine {
  const ts = entry.at ?? "";
  return {
    id: `log:${surface}:${index}:${ts}:${entry.message.slice(0, 24)}`,
    ts,
    tsMs: parseTsMs(ts),
    level: normalizeLevel(entry.level),
    source: entry.component || surface,
    surface,
    message: entry.message,
    fields: { ...(entry.fields ?? {}), level: entry.level, component: entry.component },
    origin: "log",
  };
}

export function observabilityRecordToLine(record: ObservabilityLogRecord): LogLine {
  const ts = record.observed_at ?? "";
  const identity = [
    ts,
    record.surface,
    record.stream ?? "",
    record.scope.replica_id ?? "",
    record.message,
  ].join(":");
  return {
    id: `observability:${identity}`,
    ts,
    tsMs: parseTsMs(ts),
    level: normalizeLevel(record.level),
    source: record.component || record.surface,
    surface: record.surface,
    message: record.message,
    fields: {
      ...(record.attributes ?? {}),
      ...record.scope,
      stream: record.stream,
      level: record.level,
      component: record.component,
    },
    origin: "log",
  };
}

/** Flatten every available surface's entries into normalized lines. */
export function surfacesToLines(surfaces: LogSurface[]): LogLine[] {
  const out: LogLine[] = [];
  for (const surface of surfaces) {
    if (!surface.entries) continue;
    surface.entries.forEach((entry, i) => out.push(entryToLine(entry, surface.surface, i)));
  }
  return out;
}

/** Normalize one live `OperationEvent` (from `useOperationEvents`). */
export function eventToLine(ev: OperationEvent): LogLine {
  const ts = ev.created_at ?? "";
  return {
    id: `event:${ev.seq}:${ev.id}`,
    ts,
    tsMs: parseTsMs(ts),
    level: normalizeLevel(ev.state),
    source: "operation",
    surface: "operation",
    message: ev.message && ev.message.trim() !== "" ? ev.message : ev.state,
    fields: { state: ev.state, seq: ev.seq },
    origin: "event",
    seq: ev.seq,
  };
}

// ── History query hook ──────────────────────────────────────────────

export interface OperationLogHistory {
  lines: LogLine[];
  surfaces: LogSurface[];
  /** Surfaces that are wired but returned no entries or are unavailable. */
  unavailable: LogSurface[];
  message: string;
}

function buildLogsPath(operationId: string, limit: number): string {
  const params = new URLSearchParams();
  params.set("limit", String(limit));
  return `/operations/${encodeURIComponent(operationId)}/logs?${params.toString()}`;
}

type QueryOpts<T> = Omit<UseQueryOptions<T, Error, T>, "queryKey" | "queryFn">;

/**
 * Point-in-time log history for an operation. Fetches all surfaces and
 * normalizes them; client-side filtering (see `filter.ts`) then narrows the
 * merged view, so we deliberately do not push the free-text query server-side.
 */
export function useOperationLogHistory(
  operationId: string | undefined,
  options: { limit?: number } = {},
  queryOpts?: QueryOpts<OperationLogHistory>,
) {
  const limit = options.limit ?? 500;
  return useQuery<OperationLogHistory>({
    queryKey: ["operation", operationId ?? "", "logs", limit],
    queryFn: async ({ signal }) => {
      const res = await apiFetch<OperationLogsResponse>(
        buildLogsPath(operationId as string, limit),
        { auth: "product", signal },
      );
      const surfaces = [...(res.app_logs ?? []), ...(res.platform_logs ?? [])];
      const lines = surfacesToLines(surfaces);
      const unavailable = surfaces.filter(
        (s) => !s.available || (s.entries?.length ?? 0) === 0,
      );
      return { lines, surfaces, unavailable, message: res.message };
    },
    enabled: Boolean(operationId),
    ...queryOpts,
  });
}

function observabilityPath(scope: ObservabilityLogScope, stream = false): string {
  const params = new URLSearchParams({ project_id: scope.projectId, limit: "1000" });
  if (scope.environmentId) params.set("environment_id", scope.environmentId);
  if (scope.serviceId) params.set("service_id", scope.serviceId);
  if (scope.deploymentId) params.set("deployment_id", scope.deploymentId);
  if (scope.operationId) params.set("operation_id", scope.operationId);
  if (scope.replicaId) params.set("replica_id", scope.replicaId);
  return `/observability/logs${stream ? "/stream" : ""}?${params.toString()}`;
}

export function useObservabilityLogHistory(scope: ObservabilityLogScope | undefined) {
  return useQuery<OperationLogHistory>({
    queryKey: ["observability", "logs", scope],
    queryFn: async ({ signal }) => {
      const response = await apiFetch<ObservabilityLogPage>(observabilityPath(scope as ObservabilityLogScope), {
        signal,
      });
      const lines = (response.records ?? []).map(observabilityRecordToLine);
      return {
        lines,
        surfaces: [],
        unavailable: [],
        message:
          response.dropped_at_ingest > 0
            ? `${response.dropped_at_ingest} records were dropped by the bounded ingestion rate limit.`
            : "",
      };
    },
    enabled: Boolean(scope?.projectId),
  });
}

export function useObservabilityLogStream(
  scope: ObservabilityLogScope | undefined,
  enabled: boolean,
): { lines: LogLine[]; connected: boolean; error: string | null } {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setLines([]);
    setError(null);
    if (!scope?.projectId || !enabled) {
      setConnected(false);
      return;
    }
    setConnected(true);
    const handle = connectSSE(observabilityPath(scope, true), {
      maxRetries: 20,
      reconnectOnClose: true,
      onOpen: () => {
        setConnected(true);
        setError(null);
      },
      onMessage: (message) => {
        if (message.event !== "log") return;
        try {
          const line = observabilityRecordToLine(JSON.parse(message.data) as ObservabilityLogRecord);
          setLines((previous) => {
            if (previous.some((item) => item.id === line.id)) return previous;
            const next = [...previous, line];
            return next.length > 5000 ? next.slice(next.length - 5000) : next;
          });
        } catch {
          // Ignore malformed frames; the opaque Last-Event-ID still resumes
          // from the last successfully parsed durable record.
        }
      },
      onError: (streamError) => {
        setConnected(false);
        setError(streamError.message);
      },
      onClose: () => setConnected(false),
    });
    return () => {
      handle.close();
      setConnected(false);
    };
  }, [
    enabled,
    scope?.projectId,
    scope?.environmentId,
    scope?.serviceId,
    scope?.deploymentId,
    scope?.operationId,
    scope?.replicaId,
  ]);

  return { lines, connected, error };
}
