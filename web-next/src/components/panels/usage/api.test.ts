import { describe, expect, it } from "vitest";
import { parseUsageSummary } from "./api";

function validSummary() {
  return {
    workspace_id: "wrk_one",
    policy_profile: "self_hosted",
    window_start: "2026-08-01T00:00:00Z",
    window_end: "2026-08-13T12:00:00Z",
    as_of: "2026-08-13T12:00:00Z",
    replay_after: 9,
    usage: [
      {
        dimension: "project_count",
        used: 4,
        limit: 100,
        enforceable: true,
        unit: "count",
        coverage: {
          status: "complete",
          source: "projects",
          missing_intervals: 0,
        },
      },
    ],
    disclosures: ["No monetary estimate is calculated."],
  };
}

describe("usage summary response validation", () => {
  it("accepts an unknown future dimension without weakening the rest of the contract", () => {
    const value = validSummary();
    value.usage[0].dimension = "gpu_seconds";

    expect(parseUsageSummary(value, "wrk_one").usage[0].dimension).toBe(
      "gpu_seconds",
    );
  });

  it("accepts derived int64 counters above JavaScript's exact-integer range", () => {
    const value = validSummary();
    value.usage[0].dimension = "memory_byte_seconds";
    value.usage[0].unit = "byte-seconds";
    value.usage[0].used = 11_133_000_000_000_000;

    expect(parseUsageSummary(value, "wrk_one").usage[0].used).toBe(
      11_133_000_000_000_000,
    );
  });

  it("rejects cross-workspace, non-integer, duplicate, and malformed evidence", () => {
    const duplicate = validSummary();
    duplicate.usage.push({ ...duplicate.usage[0] });

    for (const value of [
      { ...validSummary(), workspace_id: "wrk_other" },
      { ...validSummary(), replay_after: Number.POSITIVE_INFINITY },
      duplicate,
      { ...validSummary(), as_of: "not-a-date" },
      {
        ...validSummary(),
        usage: [{ ...validSummary().usage[0], used: -1 }],
      },
    ]) {
      expect(() => parseUsageSummary(value, "wrk_one")).toThrow(
        "invalid summary",
      );
    }
  });
});
