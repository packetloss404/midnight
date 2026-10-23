// Shared Loading / Empty / Error state components.
//
// House rules encoded here:
//   - Empty states MUST offer an in-product action (a button/link), never a
//     "run this CLI command" dead-end. The `action` prop is required.
//   - Error states surface a bounded, safe message and offer a retry when a
//     handler is given.
//   - All three are theme-token styled and screen-reader friendly.

import type { ReactNode } from "react";
import { presentError } from "@/lib/errorPresentation";
import { actionLabel, type ProductActionID } from "@/lib/productActions";

// ── Loading ───────────────────────────────────────────────────────

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div
      role="status"
      aria-live="polite"
      className="flex items-center gap-2.5 px-1 py-6 text-sm text-muted"
    >
      <span
        aria-hidden="true"
        className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line-strong border-t-accent"
      />
      {label}
    </div>
  );
}

// ── Empty ─────────────────────────────────────────────────────────

export type EmptyProps = {
  title: string;
  detail?: string;
  /** Required in-product action — an <ActionButton> or a router <Link>. */
  action: ReactNode;
  icon?: ReactNode;
};

export function Empty({ title, detail, action, icon }: EmptyProps) {
  return (
    <div className="grid place-items-center gap-2 rounded-lg border border-dashed border-line bg-bg-2 px-6 py-10 text-center">
      {icon ? <div className="text-2xl text-subtle">{icon}</div> : null}
      <strong className="text-text">{title}</strong>
      {detail ? <span className="max-w-md text-sm text-muted">{detail}</span> : null}
      <div className="mt-2">{action}</div>
    </div>
  );
}

/** Primary in-product action button, used inside Empty and elsewhere. */
export function ActionButton({
  children,
  onClick,
  type = "button",
  actionId,
  disabledReason,
}: {
  children?: ReactNode;
  onClick?: () => void;
  type?: "button" | "submit";
  actionId?: ProductActionID;
  disabledReason?: string;
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={Boolean(disabledReason)}
      title={disabledReason}
      data-action-id={actionId}
      className="inline-flex items-center gap-2 rounded-[var(--r-sm)] border border-accent bg-accent-soft px-3 py-1.5 text-xs font-semibold text-accent transition-colors hover:bg-surface-3"
    >
      {children ?? (actionId ? actionLabel(actionId) : null)}
    </button>
  );
}

// ── Error ─────────────────────────────────────────────────────────

export type ErrorStateProps = {
  title?: string;
  error: unknown;
  onRetry?: () => void;
};

export function ErrorState({ title = "Something went wrong", error, onRetry }: ErrorStateProps) {
  const presentation = presentError(error);
  return (
    <div
      role="alert"
      className="grid gap-2 rounded-lg border border-danger/40 bg-danger-soft px-4 py-4 text-sm"
    >
      <strong className="text-danger">{title}</strong>
      <span className="text-muted">{presentation.message}</span>
      {presentation.recovery ? (
        <span className="text-xs text-subtle">{presentation.recovery}</span>
      ) : null}
      {presentation.status !== undefined || presentation.requestId ? (
        <span className="font-mono text-[11px] text-subtle">
          {presentation.status !== undefined ? `HTTP ${presentation.status}` : null}
          {presentation.status !== undefined && presentation.requestId ? " · " : null}
          {presentation.requestId ? `Request ${presentation.requestId}` : null}
        </span>
      ) : null}
      {onRetry ? (
        <div>
          <button
            type="button"
            onClick={onRetry}
            className="mt-1 inline-flex items-center gap-2 rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-3 py-1.5 text-xs font-semibold text-text hover:bg-surface-3"
          >
            Retry
          </button>
        </div>
      ) : null}
    </div>
  );
}
