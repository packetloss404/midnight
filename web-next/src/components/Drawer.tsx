// Non-modal split-pane frame. On desktop this is an aside beside the resource
// workspace; CSS promotes the same markup to a full-width service page on
// narrow screens.

import {
  forwardRef,
  type PropsWithChildren,
  type ReactNode,
} from "react";

type DrawerProps = PropsWithChildren<{
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  subtitle?: ReactNode;
  trailing?: ReactNode;
}>;

export const Drawer = forwardRef<HTMLElement, DrawerProps>(function Drawer(
  {
    open,
    onOpenChange,
    title,
    subtitle,
    trailing,
    children,
  },
  ref,
) {
  if (!open) return null;

  return (
    <aside
      ref={ref}
      className="service-inspector"
      aria-labelledby="service-inspector-title"
    >
      <header className="flex items-start gap-3 border-b border-line px-5 py-4">
        <div className="min-w-0 flex-1">
          <h1
            id="service-inspector-title"
            tabIndex={-1}
            className="m-0 text-base font-semibold tracking-tight"
          >
            {title}
          </h1>
          {subtitle ? (
            <p className="m-0 mt-1 text-xs text-muted">{subtitle}</p>
          ) : null}
        </div>
        {trailing}
        <button
          type="button"
          aria-label="Back to resources"
          onClick={() => onOpenChange(false)}
          className="ml-1 rounded-[var(--r-sm)] border border-line bg-surface-2 px-2 py-1 text-sm text-muted hover:text-text hover:border-line-strong"
        >
          Back
        </button>
      </header>
      <div className="flex-1 overflow-auto">{children}</div>
    </aside>
  );
});
