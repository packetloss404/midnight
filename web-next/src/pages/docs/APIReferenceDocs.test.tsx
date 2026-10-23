import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { APIReferenceDocs, FEATURED_ENDPOINT_GROUPS } from "./APIReferenceDocs";

const canonicalOpenAPI = readFileSync(
  resolve(process.cwd(), "../api/openapi/v1alpha/midnight.yaml"),
  "utf8",
);

describe("APIReferenceDocs", () => {
  it("teaches the canonical reliability and authority rules", () => {
    render(<APIReferenceDocs />);

    expect(screen.getByRole("heading", { level: 1, name: "Practical API reference" })).toBeInTheDocument();
    expect(screen.getByText("/api/v1alpha", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByRole("rowheader", { name: "Human session" })).toBeInTheDocument();
    expect(screen.getByRole("rowheader", { name: "Project automation" })).toBeInTheDocument();
    expect(screen.getByText(/202 Accepted is not deployment success/)).toBeInTheDocument();
    expect(screen.getByText(/missing validator returns 428/)).toBeInTheDocument();
    expect(screen.getByText(/stable v1 projection omits the nested envelope/)).toBeInTheDocument();
    expect(screen.getByText(/bound to the authenticated actor, tenant, collection/)).toBeInTheDocument();
    expect(screen.getByText("stream=sse", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("totp_code", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("umask 077", { exact: false })).toBeInTheDocument();
  });

  it("keeps the endpoint map curated and backed by canonical operation IDs", () => {
    const endpoints = FEATURED_ENDPOINT_GROUPS.flatMap((group) => group.endpoints);
    expect(endpoints.length).toBeGreaterThanOrEqual(20);
    expect(endpoints.length).toBeLessThan(40);
    expect(new Set(endpoints.map((endpoint) => endpoint.operationId)).size).toBe(endpoints.length);

    for (const endpoint of endpoints) {
      expect(canonicalOpenAPI).toContain(`operationId: ${endpoint.operationId}`);
    }

    render(<APIReferenceDocs />);
    const map = screen.getByRole("heading", { name: "The operations most integrations need" }).parentElement?.parentElement;
    expect(map).not.toBeNull();
    expect(within(map as HTMLElement).getAllByText("/services/{id}/deployments")).toHaveLength(2);
    expect(within(map as HTMLElement).getByText("/operations/{id}/events")).toBeInTheDocument();
  });

  it("tracks canonical media types, security schemes, and problem types", () => {
    expect(canonicalOpenAPI).toContain("url: /api/v1alpha");
    expect(canonicalOpenAPI).toContain("HumanSession:");
    expect(canonicalOpenAPI).toContain("ProjectAutomationToken:");
    expect(canonicalOpenAPI).toContain("AgentCredential:");
    expect(canonicalOpenAPI).toContain("application/problem+json:");
    expect(canonicalOpenAPI).toContain("https://midnight.dev/docs/errors#PRECONDITION_FAILED");
  });
});
