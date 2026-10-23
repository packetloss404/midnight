import { Card } from "./panels/form-kit";

export const MIDNIGHT_SUPPORT_URL = "https://ko-fi.com/ianwalmsley";

/** Optional project support. Contributions are deliberately disconnected
 * from authorization, policies, quotas, and feature availability. */
export function SupportProjectCard() {
  return (
    <Card
      title="Support Midnight"
      description="Midnight is self-hosted software. If it is useful to you, you can support its continued development."
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="m-0 max-w-xl text-xs text-muted">
          Contributions are optional and never change usage limits, access,
          support priority, or product capabilities.
        </p>
        <a
          href={MIDNIGHT_SUPPORT_URL}
          target="_blank"
          rel="noopener noreferrer"
          aria-label="Support on Ko-fi (opens in a new tab)"
          className="inline-flex h-9 shrink-0 items-center rounded-[var(--r-sm)] border border-[var(--brand)] bg-[var(--brand-soft)] px-3 text-xs font-semibold text-brand hover:brightness-110"
        >
          Support on Ko-fi <span aria-hidden="true">↗</span>
        </a>
      </div>
    </Card>
  );
}
