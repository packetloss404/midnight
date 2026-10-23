import { renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetch = vi.hoisted(() => vi.fn());

vi.mock("./client", () => ({ apiFetch }));
vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({ state: { status: "human" } }),
}));

import { useDeploymentActions } from "./deployActions";

describe("canvas deployment action authority", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiFetch.mockResolvedValue({
      operation: { id: "op_1", state: "queued" },
    });
  });

  it("uses explicit human authority without operator fallback", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useDeploymentActions("svc/1"), {
      wrapper,
    });

    await result.current.redeploy.mutateAsync("dep/1");

    expect(apiFetch).toHaveBeenCalledWith(
      "/services/svc%2F1/deployments/dep%2F1/actions/redeploy",
      expect.objectContaining({ auth: "human", method: "POST" }),
    );
  });
});
