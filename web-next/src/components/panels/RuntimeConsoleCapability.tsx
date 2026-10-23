import { CapabilityBoundary } from "@/components/CapabilityBoundary";

export function RuntimeConsoleCapability() {
  return (
    <CapabilityBoundary
      state="unavailable"
      title="Runtime console"
      detail="This server has no authenticated, project-scoped runtime exec API. Midnight will not open a browser shell or simulate command execution until that audited backend boundary exists."
    />
  );
}
