import { afterEach, describe, expect, it } from "vitest";
import {
  __resetStagedChangesForTests,
  clearStagedChanges,
  getStagedForService,
  stageChange,
  type StagedChange,
} from "./stagedChanges";

afterEach(() => {
  __resetStagedChangesForTests();
});

describe("clearStagedChanges", () => {
  it("does not discard a newer edit that replaced an in-flight save", () => {
    const submitted: StagedChange = {
      serviceId: "service-1",
      environmentId: "env-1",
      name: "API_TOKEN",
      op: "edit",
      scope: "sealed",
      value: "submitted-value",
    };
    stageChange(submitted);

    const newer = { ...submitted, value: "newer-tab-draft" };
    stageChange(newer);
    clearStagedChanges([submitted]);

    expect(getStagedForService("service-1", "env-1")).toEqual([newer]);

    clearStagedChanges([newer]);
    expect(getStagedForService("service-1", "env-1")).toEqual([]);
  });
});
