import { describe, expect, it } from "vitest";
import type { EnvironmentServiceResource } from "@/api/client";
import {
  describeServiceOperation,
  serviceSetupJourney,
} from "./serviceOperations";

const service = {
  id: "svc_api",
  project_id: "prj_midnight",
  slug: "api",
  display_name: "API",
};

describe("service operational presentation", () => {
  it("distinguishes a serving release from a failed candidate", () => {
    const resource: EnvironmentServiceResource = {
      service,
      active_deployment: {
        id: "dep_serving",
        status: "ACTIVE",
        active: true,
        truth_source: "canonical",
      },
      latest_deployment: {
        id: "dep_candidate",
        status: "FAILED",
        active: false,
        operation: { id: "op_candidate", state: "FAILED" },
        truth_source: "canonical",
      },
    };

    expect(describeServiceOperation(resource)).toBe(
      "dep_serving is serving traffic. dep_candidate failed and did not replace it.",
    );
  });

  it("does not convert missing evidence into a deployment claim", () => {
    expect(describeServiceOperation({ service }, false)).toBe(
      "Deployment state is unavailable for this session.",
    );
  });
});

describe("service setup journey", () => {
  it("resumes source setup at the first incomplete actionable step", () => {
    const journey = serviceSetupJourney({ service });

    expect(journey.complete).toBe(false);
    expect(journey.steps.map((step) => step.complete)).toEqual([
      true,
      false,
      false,
      false,
      false,
    ]);
    expect(journey.action).toEqual({
      kind: "configure",
      section: "source",
      label: "Continue setup",
    });
  });

  it("is complete only with a healthy active release", () => {
    const journey = serviceSetupJourney({
      service,
      source: {
        type: "github",
        repository: "acme/api",
        truth_source: "canonical-github-link",
      },
      latest_deployment: {
        id: "dep_api",
        status: "ACTIVE",
        active: true,
        replicas: {
          desired: 2,
          ready: 2,
          running: 2,
          health: "healthy",
          evidence_at: "2026-09-01T12:00:00Z",
          truth_source: "deployment-instances",
        },
        truth_source: "canonical",
      },
    });

    expect(journey.complete).toBe(true);
    expect(journey.steps.every((step) => step.complete)).toBe(true);
    expect(journey.action).toBeUndefined();
  });
});
