import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ErrorState } from "./states";

describe("ErrorState", () => {
  it("renders the safe presentation, correlation metadata, and recovery action", () => {
    const retry = vi.fn();

    render(
      <ErrorState
        error={{
          status: 503,
          message: "<html><body>private proxy diagnostics</body></html>",
          details: { request_id: "req-ui-9" },
        }}
        onRetry={retry}
      />,
    );

    expect(screen.getByText("The Midnight API returned an unexpected response.")).toBeVisible();
    expect(screen.getByText("Retry in a moment.")).toBeVisible();
    expect(screen.getByText("HTTP 503 · Request req-ui-9")).toBeVisible();
    expect(screen.queryByText(/private proxy diagnostics/)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledOnce();
  });
});
