import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CronOccurrenceHistory } from "./CronOccurrenceHistory";

const renderHistory = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><CronOccurrenceHistory serviceId="svc_1" environmentId="env_1" enabled /></QueryClientProvider>);

afterEach(() => vi.unstubAllGlobals());

describe("CronOccurrenceHistory", () => {
  it("renders terminal and retry evidence without action controls", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify([{ id: "cron_1", due_at: "2026-07-27T12:00:00Z", state: "retry_wait", attempt: 1, max_attempts: 2, retry_at: "2026-07-27T12:05:00Z", exit_code: 3, failure_code: "PREDEPLOY_FAILED", failure_message: "exit 3" }]), { status: 200, headers: { "Content-Type": "application/json" } })));
    renderHistory();
    await waitFor(() => expect(screen.getByText("retry wait")).toBeInTheDocument());
    expect(screen.getByText(/PREDEPLOY_FAILED: exit 3/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /run|cancel/i })).not.toBeInTheDocument();
  });
  it("renders empty and error states", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200, headers: { "Content-Type": "application/json" } })));
    const { unmount } = renderHistory();
    await waitFor(() => expect(screen.getByText(/No runs yet/)).toBeInTheDocument());
    unmount();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { message: "offline" } }), { status: 500, headers: { "Content-Type": "application/json" } })));
    renderHistory();
    await waitFor(() => expect(screen.getByText("Failed to load cron history")).toBeInTheDocument());
  });
});
