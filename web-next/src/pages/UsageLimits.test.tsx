import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  authStatus: "human" as "human" | "operator",
  summary: {
    workspace_id: "wrk_1",
    policy_profile: "self_hosted",
    window_start: "2026-08-01T00:00:00Z",
    window_end: "2026-08-13T12:00:00Z",
    as_of: "2026-08-13T12:00:00Z",
    replay_after: 4,
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
      {
        dimension: "cpu_seconds",
        used: 900,
        enforceable: false,
        unit: "seconds",
        coverage: {
          status: "partial",
          source: "runtime_metrics",
          missing_intervals: 2,
          note: "derived only across observed intervals",
        },
      },
      {
        dimension: "build_seconds",
        used: 0,
        enforceable: false,
        unit: "seconds",
        coverage: {
          status: "unavailable",
          source: "unavailable",
          missing_intervals: 0,
          note: "no trustworthy producer is wired",
        },
      },
    ],
    disclosures: ["Contributions never change limits."],
  },
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({ state: { status: state.authStatus } }),
}));

vi.mock("@/api/hooks", () => ({
  useIdentityWorkspaces: () => ({
    data: [{ id: "wrk_1", slug: "main", display_name: "Main", role: "admin" }],
    isPending: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

vi.mock("@/components/panels/usage/api", async () => {
  const actual = await vi.importActual<typeof import("@/components/panels/usage/api")>(
    "@/components/panels/usage/api",
  );
  return {
    ...actual,
    useUsageSummary: () => ({
      data: state.summary,
      isPending: false,
      error: null,
      refetch: vi.fn(),
    }),
  };
});

vi.mock("@/components/SupportProjectCard", () => ({
  SupportProjectCard: () => <a href="https://ko-fi.com/ianwalmsley">Support Midnight</a>,
}));

import { UsageLimits, usageLimitsTestHelpers } from "./UsageLimits";

beforeEach(() => {
  state.authStatus = "human";
});

describe("UsageLimits", () => {
  it("distinguishes enforced, incomplete, and unavailable evidence", async () => {
    render(<UsageLimits />);

    expect(await screen.findByRole("heading", { name: "Usage & limits" })).toBeInTheDocument();
    expect(screen.getByText("Projects")).toBeInTheDocument();
    expect(screen.getByText("Enforced")).toBeInTheDocument();
    expect(screen.getByText("Incomplete")).toBeInTheDocument();
    expect(screen.getByText("Unavailable")).toBeInTheDocument();
    expect(screen.getByText("Not measured")).toBeInTheDocument();
    expect(screen.getByText(/2 gaps in measurement/)).toBeInTheDocument();
    expect(screen.queryByText(/upgrade/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/\$/)).not.toBeInTheDocument();
  });

  it("does not treat emergency recovery as workspace identity", () => {
    state.authStatus = "operator";
    render(<UsageLimits />);

    expect(screen.getByText("Workspace sign-in required")).toBeInTheDocument();
    expect(screen.getByText(/Emergency recovery does not grant/)).toBeInTheDocument();
    expect(screen.queryByText(/installation-administrator/i)).not.toBeInTheDocument();
  });

  it("formats evidence without inventing monetary values", () => {
    expect(usageLimitsTestHelpers.formatValue(1_073_741_824, "bytes")).toBe("1.0 GiB");
    expect(usageLimitsTestHelpers.formatValue(3_600, "seconds")).toBe("1.0 h");
    expect(usageLimitsTestHelpers.formatValue(3_865_470_566_400, "byte-seconds")).toBe("1.0 GiB·h");
  });

  it("presents future dimensions and invalid timestamps without crashing", () => {
    expect(
      usageLimitsTestHelpers.usageDimensionDefinition("gpu_seconds").label,
    ).toBe("Gpu seconds");
    expect(usageLimitsTestHelpers.formatTimestamp("not-a-date")).toBe(
      "Date unavailable",
    );
  });
});
