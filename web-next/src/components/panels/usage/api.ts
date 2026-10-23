import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type KnownUsageDimension =
  | "source_artifact_bytes"
  | "project_count"
  | "service_count"
  | "environment_count"
  | "volume_count"
  | "build_seconds"
  | "cpu_seconds"
  | "memory_byte_seconds"
  | "public_http_requests"
  | "public_http_response_bytes";

// The server may add a showback dimension before an older console has learned
// its presentation copy. Keep the wire value open while retaining autocomplete
// for the dimensions this console knows how to describe.
export type UsageDimension = KnownUsageDimension | (string & {});

export type UsageCoverage = {
  status: "complete" | "partial" | "unavailable";
  source: string;
  last_observed_at?: string;
  missing_intervals: number;
  note?: string;
};

export type UsageLine = {
  dimension: UsageDimension;
  used: number;
  limit?: number;
  enforceable: boolean;
  unit: "bytes" | "seconds" | "byte-seconds" | "count";
  coverage: UsageCoverage;
};

export type UsageSummary = {
  workspace_id: string;
  policy_profile: string;
  window_start: string;
  window_end: string;
  as_of: string;
  replay_after: number;
  usage: UsageLine[];
  disclosures: string[];
};

export type UsageWindow = {
  since: string;
  until: string;
};

export function useUsageSummary(
  workspaceId: string | undefined,
  window: UsageWindow,
) {
  return useQuery<UsageSummary>({
    queryKey: ["usage", "summary", workspaceId ?? null, window.since, window.until],
    queryFn: async ({ signal }) => {
      const query = new URLSearchParams({
        workspace_id: workspaceId as string,
        since: window.since,
        until: window.until,
      });
      const value = await apiFetch<unknown>(`/usage/summary?${query.toString()}`, {
        auth: "human",
        signal,
      });
      return parseUsageSummary(value, workspaceId as string);
    },
    enabled: Boolean(workspaceId),
  });
}

export function parseUsageSummary(
  value: unknown,
  expectedWorkspaceId: string,
): UsageSummary {
  if (!isRecord(value) || value.workspace_id !== expectedWorkspaceId) {
    throw invalidUsageSummary();
  }

  const windowStart = readTimestamp(value.window_start);
  const windowEnd = readTimestamp(value.window_end);
  const asOf = readTimestamp(value.as_of);
  if (
    !windowStart ||
    !windowEnd ||
    !asOf ||
    Date.parse(windowStart) > Date.parse(windowEnd) ||
    !isBoundedString(value.policy_profile) ||
    value.policy_profile.trim().length === 0 ||
    !isUnsignedInteger(value.replay_after) ||
    !Array.isArray(value.usage) ||
    value.usage.length > 256 ||
    !Array.isArray(value.disclosures) ||
    value.disclosures.length > 256 ||
    !value.disclosures.every(isBoundedString)
  ) {
    throw invalidUsageSummary();
  }

  const usage = value.usage.map(parseUsageLine);
  if (new Set(usage.map((line) => line.dimension)).size !== usage.length) {
    throw invalidUsageSummary();
  }

  return {
    workspace_id: value.workspace_id,
    policy_profile: value.policy_profile,
    window_start: windowStart,
    window_end: windowEnd,
    as_of: asOf,
    replay_after: value.replay_after,
    usage,
    disclosures: value.disclosures,
  };
}

function parseUsageLine(value: unknown): UsageLine {
  if (
    !isRecord(value) ||
    typeof value.dimension !== "string" ||
    value.dimension.trim().length === 0 ||
    value.dimension.length > 128 ||
    !isUnsignedInteger(value.used) ||
    (value.limit !== undefined &&
      (!isUnsignedInteger(value.limit) || value.limit < 1)) ||
    typeof value.enforceable !== "boolean" ||
    !isUsageUnit(value.unit) ||
    !isRecord(value.coverage) ||
    !isCoverageStatus(value.coverage.status) ||
    !isBoundedString(value.coverage.source) ||
    !isUnsignedSafeInteger(value.coverage.missing_intervals) ||
    (value.coverage.last_observed_at !== undefined &&
      !readTimestamp(value.coverage.last_observed_at)) ||
    (value.coverage.note !== undefined && !isBoundedString(value.coverage.note))
  ) {
    throw invalidUsageSummary();
  }

  const lastObservedAt = readTimestamp(value.coverage.last_observed_at);

  return {
    dimension: value.dimension,
    used: value.used,
    ...(value.limit === undefined ? {} : { limit: value.limit }),
    enforceable: value.enforceable,
    unit: value.unit,
    coverage: {
      status: value.coverage.status,
      source: value.coverage.source,
      missing_intervals: value.coverage.missing_intervals,
      ...(lastObservedAt === undefined
        ? {}
        : { last_observed_at: lastObservedAt }),
      ...(value.coverage.note === undefined
        ? {}
        : { note: value.coverage.note }),
    },
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isUnsignedSafeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0;
}

// Go int64 counters can legitimately exceed JavaScript's exact-integer range
// (memory byte-seconds does so at ordinary fleet sizes). The UI renders those
// units at coarse precision and never uses them for mutations or authority.
function isUnsignedInteger(value: unknown): value is number {
  return Number.isFinite(value) && Number.isInteger(value) && (value as number) >= 0;
}

function isBoundedString(value: unknown): value is string {
  return typeof value === "string" && value.length <= 2_048;
}

function readTimestamp(value: unknown): string | undefined {
  return typeof value === "string" && value.length <= 64 && Number.isFinite(Date.parse(value))
    ? value
    : undefined;
}

function isCoverageStatus(value: unknown): value is UsageCoverage["status"] {
  return value === "complete" || value === "partial" || value === "unavailable";
}

function isUsageUnit(value: unknown): value is UsageLine["unit"] {
  return value === "bytes" || value === "seconds" || value === "byte-seconds" || value === "count";
}

function invalidUsageSummary(): Error {
  return new Error("The usage service returned an invalid summary.");
}
