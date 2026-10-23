import { describe, expect, it } from "vitest";
import { OBSERVABILITY_SCOPE_LABELS } from "./ProjectObservability";

describe("ProjectObservability scope labels", () => {
  it("distinguishes instance-wide data from project-filtered data", () => {
    expect(OBSERVABILITY_SCOPE_LABELS.description).toContain("instance-wide");
    expect(OBSERVABILITY_SCOPE_LABELS.fleetSummary).toBe(
      "instance-wide · all projects",
    );
    expect(OBSERVABILITY_SCOPE_LABELS.runtimeHealth).toBe(
      "project-scoped · fleet snapshot source",
    );
  });

  it("does not imply every widget honors the environment filter", () => {
    expect(OBSERVABILITY_SCOPE_LABELS.environmentNote).toBe(
      "Environment filters apply only to supported widgets.",
    );
  });
});
