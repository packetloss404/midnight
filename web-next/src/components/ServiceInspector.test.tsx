import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CapabilityBoundary } from "./CapabilityBoundary";
import {
  normalizeConfigureSection,
  normalizeServiceSection,
  serviceDetailQueryPolicy,
} from "./ServiceDetailDrawer";
import { ServiceOverview } from "./panels/ServiceOverview";

describe("service inspector route state", () => {
  it("normalizes unknown sections without claiming unsupported surfaces", () => {
    expect(normalizeServiceSection("logs")).toBe("logs");
    expect(normalizeServiceSection("domains")).toBe("overview");
    expect(normalizeConfigureSection("networking")).toBe("networking");
    expect(normalizeConfigureSection("danger")).toBe("variables");
  });

  it("queries operator evidence only for the active section", () => {
    expect(serviceDetailQueryPolicy("overview", true)).toEqual({
      summary: true,
      deployments: false,
    });
    expect(serviceDetailQueryPolicy("deployments", true)).toEqual({
      summary: false,
      deployments: true,
    });
    expect(serviceDetailQueryPolicy("logs", true)).toEqual({
      summary: false,
      deployments: true,
    });
    expect(serviceDetailQueryPolicy("configure", true)).toEqual({
      summary: true,
      deployments: false,
    });
    expect(serviceDetailQueryPolicy("overview", false)).toEqual({
      summary: false,
      deployments: true,
    });
    expect(serviceDetailQueryPolicy("deployments", false)).toEqual({
      summary: false,
      deployments: true,
    });
    expect(serviceDetailQueryPolicy("logs", false)).toEqual({
      summary: false,
      deployments: true,
    });
  });

  it("does not mount children behind an unavailable capability", () => {
    render(
      <CapabilityBoundary
        state="unavailable"
        title="Domains and networking"
        detail="Not implemented."
      >
        <div data-testid="should-not-mount">fabricated domain</div>
      </CapabilityBoundary>,
    );

    expect(screen.getByText("Unavailable")).toBeInTheDocument();
    expect(
      screen.queryByTestId("should-not-mount"),
    ).not.toBeInTheDocument();
  });

  it("renders only canonical environment-summary evidence", () => {
    render(
      <ServiceOverview
        environmentName="Production"
        resource={{
          service: {
            id: "svc_api",
            project_id: "prj_midnight",
            slug: "api",
            display_name: "API",
          },
          latest_deployment: {
            id: "dep_api",
            status: "ACTIVE",
            active: true,
            image: "registry.test/api@sha256:abcd",
            source_type: "github",
            source_repository: "acme/api",
            source_ref: "main",
            commit_sha: "abc123",
            dockerfile_path: "deploy/api.Dockerfile",
            builder: "dockerfile",
            runtime_desired_state: "running",
            runtime_observed_state: "running",
            replicas: {
              desired: 1,
              ready: 1,
              running: 1,
              health: "healthy",
              evidence_at: "2026-07-27T12:00:00Z",
              truth_source: "deployment-instances",
            },
            operation: { id: "op_api", state: "SUCCEEDED" },
            truth_source: "canonical",
          },
          source: {
            type: "github",
            repository: "acme/api",
            ref: "main",
            revision: "abc123",
            watch_paths: ["services/api/**", "packages/shared/**"],
            root_directory: "services/api",
            config_path: "midnight.toml",
            wait_for_ci: true,
            required_checks: ["test", "lint"],
            auto_deploy: true,
            verified_at: "2026-07-27T11:00:00Z",
            truth_source: "canonical-github-link",
          },
          endpoint: {
            deployment_id: "dep_api",
            host: "api.midnight.test",
            url: "https://api.midnight.test",
            tls: true,
            evidence_at: "2026-07-27T12:00:00Z",
          },
          private_endpoint: {
            host: "api.midnight.internal",
            truth_source: "derived-service-dns",
          },
        }}
      />,
    );

    expect(screen.getByText("dep_api")).toBeInTheDocument();
    expect(
      screen.getByText(
        "dep_api is serving traffic with 1 of 1 replicas ready.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Finish setting up API/),
    ).not.toBeInTheDocument();
    expect(screen.getByText("Deployment record")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /api\.midnight\.test/ }),
    ).toHaveAttribute("href", "https://api.midnight.test");
    expect(screen.getByText("Connected source")).toBeInTheDocument();
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getAllByText("abc123")).toHaveLength(2);
    expect(
      screen.getByText("services/api/**, packages/shared/**"),
    ).toBeInTheDocument();
    expect(screen.getByText("midnight.toml")).toBeInTheDocument();
    expect(screen.getByText("Required: test, lint")).toBeInTheDocument();
    expect(screen.getByText("deploy/api.Dockerfile")).toBeInTheDocument();
    expect(
      screen.getByText(/1 of 1 replicas ready · 1 observed running/),
    ).toBeInTheDocument();
    expect(screen.getByText("api.midnight.internal")).toBeInTheDocument();
  });

  it("routes source and networking recommendations to their matching configure sections", async () => {
    const user = userEvent.setup();
    const configure = vi.fn();
    const { rerender } = render(
      <ServiceOverview
        resource={{
          service: {
            id: "svc_new",
            project_id: "prj_midnight",
            slug: "new",
            display_name: "New service",
          },
          private_endpoint: {
            host: "new.midnight.internal",
            truth_source: "derived-service-dns",
          },
        }}
        onConfigure={configure}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "Continue setup" }),
    );
    expect(configure).toHaveBeenLastCalledWith("source");

    rerender(
      <ServiceOverview
        resource={{
          service: {
            id: "svc_private",
            project_id: "prj_midnight",
            slug: "private",
            display_name: "Private service",
          },
          source: {
            type: "github",
            repository: "acme/private",
            ref: "main",
            truth_source: "canonical-github-link",
          },
          private_endpoint: {
            host: "private.midnight.internal",
            truth_source: "derived-service-dns",
          },
        }}
        onConfigure={configure}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "Continue setup" }),
    );
    expect(configure).toHaveBeenLastCalledWith("runtime");
  });

  it("keeps the serving release explicit when a newer rollout fails", () => {
    render(
      <ServiceOverview
        resource={{
          service: {
            id: "svc_api",
            project_id: "prj_midnight",
            slug: "api",
            display_name: "API",
          },
          source: {
            type: "github",
            repository: "acme/api",
            truth_source: "canonical-github-link",
          },
          active_deployment: {
            id: "dep_serving",
            status: "ACTIVE",
            active: true,
            replicas: {
              desired: 1,
              ready: 1,
              running: 1,
              health: "healthy",
              evidence_at: "2026-09-01T12:00:00Z",
              truth_source: "deployment-instances",
            },
            truth_source: "canonical",
          },
          latest_deployment: {
            id: "dep_failed",
            status: "FAILED",
            active: false,
            operation: {
              id: "op_failed",
              state: "FAILED",
              failure_reason: "Build failed.",
            },
            truth_source: "canonical",
          },
        }}
      />,
    );

    expect(
      screen.getByText(
        "dep_serving is serving traffic. dep_failed failed and did not replace it.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/New rollout \(not serving\)/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "The current release remains active; this failed rollout did not replace it.",
      ),
    ).toBeInTheDocument();
  });
});
