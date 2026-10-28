import { describe, expect, it } from "vitest";
import {
  isHumanProjectRouteAdmitted,
  isAgentRouteAvailable,
  projectRouteRequiresEnvironment,
  resolveRequestedConfigureSection,
} from "./routes";

describe("human project route admission", () => {
  it("shows tenant chat only in a resolved authorized project while preserving trusted global access", () => {
    expect(isAgentRouteAvailable(false, true, "prj_1", [{ id: "prj_1" }])).toBe(true);
    expect(isAgentRouteAvailable(false, true, "prj_other", [{ id: "prj_1" }])).toBe(false);
    expect(isAgentRouteAvailable(false, true, "prj_1", undefined)).toBe(false);
    expect(isAgentRouteAvailable(false, true, undefined, [{ id: "prj_1" }])).toBe(false);
    expect(isAgentRouteAvailable(false, false, "prj_1", [{ id: "prj_1" }])).toBe(false);
    expect(isAgentRouteAvailable(true, false, undefined, undefined)).toBe(true);
  });
  it("admits tenant project administration and the service inspector", () => {
    expect(isHumanProjectRouteAdmitted("/new")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/usage")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/docs")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/docs/agent-setup")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/docs/api")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/docs/cli")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/docs/api/")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/projects/prj_1")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/projects/prj_1/")).toBe(true);
    expect(
      isHumanProjectRouteAdmitted(
        "/projects/prj_1/services/svc_1/overview",
      ),
    ).toBe(true);
    expect(
      isHumanProjectRouteAdmitted(
        "/projects/prj_1/services/svc_1/deployments/",
      ),
    ).toBe(true);
    expect(isHumanProjectRouteAdmitted("/projects/prj_1/deployments")).toBe(
      true,
    );
    expect(isHumanProjectRouteAdmitted("/projects/prj_1/settings/")).toBe(
      true,
    );
  });

  it("lets the router render docs typos as not found while protecting installation routes", () => {
    expect(isHumanProjectRouteAdmitted("/docs/internal")).toBe(true);
    expect(isHumanProjectRouteAdmitted("/docs/api/extra")).toBe(true);

    for (const pathname of [
      "/projects/prj_1/observability",
      "/projects/prj_1/services",
      "/projects/prj_1/services/svc_1",
      "/projects/prj_1/services/svc_1/logs/extra",
    ]) {
      expect(isHumanProjectRouteAdmitted(pathname), pathname).toBe(false);
    }
  });
});

describe("project environment resolution", () => {
  it("requires a concrete environment for resources, service detail, and deployments", () => {
    expect(projectRouteRequiresEnvironment("/projects/prj_1")).toBe(true);
    expect(
      projectRouteRequiresEnvironment(
        "/projects/prj_1/services/svc_1/configure",
      ),
    ).toBe(true);
    expect(
      projectRouteRequiresEnvironment("/projects/prj_1/deployments"),
    ).toBe(true);
  });

  it("lets project settings render without an existing environment", () => {
    expect(projectRouteRequiresEnvironment("/projects/prj_1/settings")).toBe(
      false,
    );
    expect(
      projectRouteRequiresEnvironment("/projects/prj_1/observability"),
    ).toBe(false);
  });
});

describe("service Configure route state", () => {
  it("preserves an absent section for service-aware defaults", () => {
    expect(resolveRequestedConfigureSection()).toBeUndefined();
  });

  it("keeps explicit and legacy Configure links canonical", () => {
    expect(resolveRequestedConfigureSection("networking")).toBe("networking");
    expect(resolveRequestedConfigureSection(undefined, "runtime")).toBe("runtime");
    expect(resolveRequestedConfigureSection("unsupported")).toBe("variables");
  });
});
