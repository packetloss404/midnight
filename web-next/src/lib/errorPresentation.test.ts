import { describe, expect, it } from "vitest";
import { presentError } from "./errorPresentation";

describe("presentError", () => {
  it("replaces an HTML gateway response with bounded recovery copy", () => {
    const result = presentError({
      status: 502,
      message: "<!doctype html><html><body>nginx upstream 10.0.0.4 failed</body></html>",
      requestId: "req-safe-42",
    });

    expect(result).toEqual({
      status: 502,
      message: "The Midnight API returned an unexpected response.",
      requestId: "req-safe-42",
      recovery: "Retry in a moment.",
    });
    expect(result.message).not.toContain("nginx");
    expect(result.message).not.toContain("<html>");
  });

  it("keeps a safe validation message and extracts a nested request id", () => {
    const result = presentError({
      status: 422,
      error: {
        message: "Service name must start with a letter.",
        request_id: "req-validation_7",
      },
    });

    expect(result.status).toBe(422);
    expect(result.message).toBe("Service name must start with a letter.");
    expect(result.requestId).toBe("req-validation_7");
  });

  it("omits unsafe correlation ids and normalizes control characters", () => {
    const result = presentError({
      status: 400,
      message: "Invalid\u0000 input\n\n  supplied",
      details: { request_id: "<script>alert(1)</script>" },
    });

    expect(result.message).toBe("Invalid input supplied");
    expect(result.requestId).toBeUndefined();
  });

  it("presents network failures with a concrete recovery", () => {
    const result = presentError(new TypeError("Failed to fetch"));

    expect(result).toEqual({
      message: "Could not reach the Midnight API.",
      recovery: "Check the API connection, then retry.",
    });
  });

  it("does not expose server-provided authorization detail", () => {
    const result = presentError({
      status: 403,
      message: "internal policy principal=admin-only",
    });

    expect(result.message).toBe("You do not have permission to perform this action.");
    expect(result.recovery).toBe("Ask a project administrator for access.");
  });

  it("treats a failed revision precondition as a recoverable conflict", () => {
    expect(
      presentError({ status: 412, message: "expected revision 7" }),
    ).toEqual({
      status: 412,
      message: "The resource changed while you were working.",
      recovery: "Refresh the latest state, then retry.",
    });
  });
});
