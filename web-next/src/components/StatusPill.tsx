// Status pill rendered with a coloured dot + label. Tone always routes
// through the single `lib/deploymentStatus` mapper so a status looks the
// same on the canvas, in the drawer, and in the deployment list.

import clsx from "clsx";
import {
  deploymentStatus,
  statusTone,
  type DeploymentSignal,
  type StatusTone,
} from "@/lib/deploymentStatus";

export type { StatusTone };
export { statusTone };

type Props = {
  /** Raw status string (convenience). */
  status?: string | undefined | null;
  /** Full backend signal (status + operation_state + active). Wins over `status`. */
  signal?: DeploymentSignal;
  className?: string;
};

export function StatusPill({ status, signal, className }: Props) {
  const view = deploymentStatus(signal ?? { status });
  return (
    <span
      className={clsx("status-pill", view.pending && "status-pill--pending", className)}
      data-tone={view.tone}
      role="status"
    >
      {view.label}
    </span>
  );
}
