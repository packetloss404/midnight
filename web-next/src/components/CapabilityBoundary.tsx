import type { ReactNode } from "react";
import {
  describeCapability,
  type CapabilityState,
} from "@/lib/capabilities";

type Props = {
  state: CapabilityState;
  title: string;
  detail: string;
  children?: ReactNode;
};

/**
 * Shared honesty boundary for partially wired product surfaces. Degraded
 * capabilities may render their evidence-backed subset; unavailable surfaces
 * never mount a query or mutation behind the boundary.
 */
export function CapabilityBoundary({
  state,
  title,
  detail,
  children,
}: Props) {
  const disposition = describeCapability(state);
  const renderChildren = disposition.canUse && children !== undefined;

  return (
    <section
      className="grid gap-3"
      data-capability-state={state}
      aria-label={`${title} capability`}
    >
      <div
        role="status"
        className={
          "grid gap-1 rounded-[var(--r-sm)] border px-3 py-2.5 text-xs " +
          (state === "available"
            ? "border-line bg-surface-2 text-muted"
            : state === "degraded" || state === "setup-required"
              ? "border-warn/40 bg-warn-soft text-muted"
              : "border-line bg-bg-2 text-muted")
        }
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <strong className="text-text">{title}</strong>
          <span className="rounded-full border border-line bg-surface px-2 py-0.5 font-mono text-[9px] uppercase tracking-wider text-muted">
            {disposition.label}
          </span>
        </div>
        <span>{detail}</span>
      </div>
      {renderChildren ? children : null}
    </section>
  );
}
