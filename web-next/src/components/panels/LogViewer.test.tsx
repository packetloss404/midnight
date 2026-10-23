import { render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LogLine, ObservabilityLogScope } from "./logs/api";

const state = vi.hoisted(() => ({
  operationHistoryCalls: [] as Array<string | undefined>,
  runtimeHistoryCalls: [] as Array<ObservabilityLogScope | undefined>,
  runtimeError: null as Error | null,
}));

const operationLine: LogLine = {
  id: "operation-history",
  ts: "2026-07-28T12:00:00Z",
  tsMs: Date.parse("2026-07-28T12:00:00Z"),
  level: "info",
  source: "build",
  surface: "build",
  message: "image build completed",
  fields: {},
  origin: "log",
};

const runtimeLine: LogLine = {
  id: "runtime-history",
  ts: "2026-07-28T12:01:00Z",
  tsMs: Date.parse("2026-07-28T12:01:00Z"),
  level: "info",
  source: "app",
  surface: "runtime",
  message: "server listening",
  fields: {},
  origin: "log",
};

vi.mock("@/api/hooks", () => ({
  useOperationEvents: () => ({
    events: [],
    connected: false,
    error: null,
  }),
}));

vi.mock("./logs/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./logs/api")>();
  return {
    ...actual,
    useOperationLogHistory: (operationId: string | undefined) => {
      state.operationHistoryCalls.push(operationId);
      return {
        data: {
          lines: operationId ? [operationLine] : [],
          surfaces: [],
          unavailable: [],
          message: "",
        },
        isPending: false,
        error: null,
        refetch: vi.fn(),
      };
    },
    useObservabilityLogHistory: (
      scope: ObservabilityLogScope | undefined,
    ) => {
      state.runtimeHistoryCalls.push(scope);
      return {
        data: {
          lines: scope && !state.runtimeError ? [runtimeLine] : [],
          surfaces: [],
          unavailable: [],
          message: "",
        },
        isPending: false,
        error: scope ? state.runtimeError : null,
        refetch: vi.fn(),
      };
    },
    useObservabilityLogStream: () => ({
      lines: [],
      connected: false,
      error: null,
    }),
  };
});

import { LogViewer } from "./LogViewer";

const scope: ObservabilityLogScope = {
  projectId: "prj_1",
  environmentId: "env_1",
  serviceId: "svc_1",
  deploymentId: "dpl_1",
  operationId: "op_1",
};

beforeEach(() => {
  vi.clearAllMocks();
  state.operationHistoryCalls = [];
  state.runtimeHistoryCalls = [];
  state.runtimeError = null;
});

describe("LogViewer source boundaries", () => {
  it("keeps operation evidence when a runtime scope is also provided", () => {
    render(
      <LogViewer
        operationId="op_1"
        scope={scope}
        follow={false}
        height={600}
      />,
    );

    const operation = screen
      .getByText("Build & deploy logs")
      .closest("[data-log-source='operation']");
    const runtime = screen
      .getByText("Runtime logs")
      .closest("[data-log-source='runtime']");

    expect(operation).not.toBeNull();
    expect(runtime).not.toBeNull();
    expect(within(operation as HTMLElement).getByText("image build completed"))
      .toBeInTheDocument();
    expect(within(runtime as HTMLElement).getByText("server listening"))
      .toBeInTheDocument();
    expect(state.operationHistoryCalls).toContain("op_1");
    expect(state.runtimeHistoryCalls).toContainEqual(scope);
  });

  it("reports runtime unavailability without hiding build evidence", () => {
    state.runtimeError = new Error("observability route unavailable");
    render(
      <LogViewer
        operationId="op_1"
        scope={scope}
        follow={false}
        height={600}
      />,
    );

    expect(screen.getByText("image build completed")).toBeInTheDocument();
    expect(screen.getByText("Failed to load logs")).toBeInTheDocument();
    expect(screen.getByText("observability route unavailable"))
      .toBeInTheDocument();
  });
});
