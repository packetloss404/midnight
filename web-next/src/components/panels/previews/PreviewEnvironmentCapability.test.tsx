import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("./api", () => ({
  usePreviewEnvironments: () => ({
    isPending: false,
    error: null,
    refetch: vi.fn(),
    data: {
      service_id: "svc_one",
      project_id: "prj_one",
      items: [{
        id: "prv_one",
        pull_request: 42,
        environment_id: "env_preview",
        deployment_id: "dep_preview",
        state: "ready",
        url: "https://preview.apps.example.com",
        head_sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        from_fork: true,
        expires_at: "2026-09-06T12:00:00Z",
        updated_at: "2026-08-30T12:00:00Z",
      }],
      capability: {
        state: "enabled",
        automatic_lifecycle: true,
        received_event: "pull_request",
        current_disposition: "routed",
        requested_actions: ["opened:create", "reopened:update", "synchronize:update", "closed:delete"],
        blockers: [],
      },
    },
  }),
}));

import { PreviewEnvironmentCapability } from "./PreviewEnvironmentCapability";

describe("PreviewEnvironmentCapability", () => {
  it("shows active isolated previews and their generated URL", () => {
    render(<PreviewEnvironmentCapability serviceId="svc_one" />);

    expect(screen.getByText("Automatic")).toBeInTheDocument();
    expect(screen.getByText("PR #42")).toBeInTheDocument();
    expect(screen.getByText("fork · isolated")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open preview" })).toHaveAttribute("href", "https://preview.apps.example.com");
  });
});
