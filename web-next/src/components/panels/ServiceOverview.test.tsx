import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { EnvironmentServiceResource } from "@/api/client";
import { ServiceOverview } from "./ServiceOverview";

function resourceWithFailure(
  failure_code: string,
  failure_reason: string,
): EnvironmentServiceResource {
  return {
    service: {
      id: "svc_cache",
      project_id: "prj_midnight",
      slug: "cache",
      display_name: "Cache",
    },
    latest_deployment: {
      id: "dep_cache",
      status: "FAILED",
      active: false,
      image: "redis@sha256:aa",
      operation: {
        id: "op_cache",
        state: "FAILED",
        failure_code,
        failure_reason,
      },
      truth_source: "canonical",
    },
  };
}

describe("ServiceOverview rollout reason", () => {
  it("leads with plain language and keeps the container's own output", () => {
    render(
      <ServiceOverview
        resource={resourceWithFailure(
          "DEPLOY_CANDIDATE_EXITED",
          "the container stopped before it was ready; container exited with code 1; last log lines: FATAL: bad config",
        )}
        environmentName="production"
      />,
    );

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(
      "The container stopped before it was ready.",
    );
    expect(alert).toHaveTextContent("FATAL: bad config");
  });

  it("explains a restart loop rather than showing its code", () => {
    render(
      <ServiceOverview
        resource={resourceWithFailure(
          "DEPLOY_CANDIDATE_RESTART_LOOP",
          "the container keeps restarting (4 restarts) and never became ready",
        )}
      />,
    );

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(
      "The container keeps starting and crashing.",
    );
    expect(alert).not.toHaveTextContent("DEPLOY_CANDIDATE_RESTART_LOOP");
  });

  it("falls back to the worker's reason for an unrecognized code", () => {
    render(
      <ServiceOverview
        resource={resourceWithFailure(
          "SOME_NEW_CODE",
          "Registry rejected the image.",
        )}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Registry rejected the image.",
    );
  });

  it("shows no alert when the last rollout carries no failure", () => {
    render(
      <ServiceOverview
        resource={{
          service: {
            id: "svc_cache",
            project_id: "prj_midnight",
            slug: "cache",
            display_name: "Cache",
          },
          latest_deployment: {
            id: "dep_cache",
            status: "SUCCEEDED",
            active: true,
            operation: { id: "op_cache", state: "SUCCEEDED" },
            truth_source: "canonical",
          },
        }}
      />,
    );

    expect(screen.queryByRole("alert")).toBeNull();
  });
});
