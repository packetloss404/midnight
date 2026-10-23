import { describe, expect, it } from "vitest";
import {
  CAPABILITY_STATES,
  describeCapability,
  isCapabilityState,
} from "./capabilities";

describe("capability state", () => {
  it.each([
    ["available", true, false, "Available"],
    ["setup-required", false, true, "Setup required"],
    ["forbidden", false, false, "Access required"],
    ["unavailable", false, false, "Unavailable"],
    ["degraded", true, false, "Available with limitations"],
  ] as const)(
    "describes %s without collapsing distinct availability states",
    (state, canUse, canConfigure, label) => {
      expect(describeCapability(state)).toEqual({ canUse, canConfigure, label });
    },
  );

  it("validates values at runtime", () => {
    for (const state of CAPABILITY_STATES) {
      expect(isCapabilityState(state)).toBe(true);
    }
    expect(isCapabilityState("planned")).toBe(false);
    expect(isCapabilityState(null)).toBe(false);
  });
});
