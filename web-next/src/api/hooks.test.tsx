import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SSEOptions } from "./sse";

const state = vi.hoisted(() => ({
  options: undefined as SSEOptions | undefined,
  close: vi.fn(),
}));

vi.mock("./sse", () => ({
  connectSSE: vi.fn((_path: string, options: SSEOptions) => {
    state.options = options;
    return {
      close: state.close,
      get closed() {
        return false;
      },
    };
  }),
}));

import { useOperationEvents } from "./hooks";

describe("useOperationEvents", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    state.options = undefined;
  });

  it("clears events when the selected operation is removed", () => {
    const initialProps: { operationId?: string } = { operationId: "op_1" };
    const { result, rerender } = renderHook(
      ({ operationId }: { operationId?: string }) =>
        useOperationEvents(operationId),
      { initialProps },
    );

    act(() => {
      state.options?.onMessage({
        event: "operation",
        data: JSON.stringify({ seq: 1, state: "RUNNING" }),
      });
    });
    expect(result.current.events).toHaveLength(1);

    rerender({ operationId: undefined });

    expect(state.close).toHaveBeenCalledTimes(1);
    expect(result.current).toEqual({
      events: [],
      connected: false,
      error: null,
    });
  });
});
