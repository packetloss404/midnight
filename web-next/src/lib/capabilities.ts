export const CAPABILITY_STATES = [
  "available",
  "setup-required",
  "forbidden",
  "unavailable",
  "degraded",
] as const;

export type CapabilityState = (typeof CAPABILITY_STATES)[number];

export type CapabilityDisposition = {
  canUse: boolean;
  canConfigure: boolean;
  label: string;
};

/**
 * A small shared contract for features whose availability depends on setup,
 * authorization, backend support, or current health.
 */
export function describeCapability(state: CapabilityState): CapabilityDisposition {
  switch (state) {
    case "available":
      return { canUse: true, canConfigure: false, label: "Available" };
    case "setup-required":
      return { canUse: false, canConfigure: true, label: "Setup required" };
    case "forbidden":
      return { canUse: false, canConfigure: false, label: "Access required" };
    case "unavailable":
      return { canUse: false, canConfigure: false, label: "Unavailable" };
    case "degraded":
      return { canUse: true, canConfigure: false, label: "Available with limitations" };
  }
}

export function isCapabilityState(value: unknown): value is CapabilityState {
  return typeof value === "string" &&
    (CAPABILITY_STATES as readonly string[]).includes(value);
}
