import { useState, type ReactNode } from "react";

export function DocsPage({ children }: { children: ReactNode }) {
  return (
    <article className="mx-auto flex w-full max-w-5xl flex-col gap-5 p-[var(--main-gutter)]">
      {children}
    </article>
  );
}

export function DocsHero({
  eyebrow,
  title,
  description,
  badges,
}: {
  eyebrow: string;
  title: string;
  description: string;
  badges: string[];
}) {
  return (
    <header className="grid gap-3 border-b border-line pb-5">
      <div className="grid gap-1">
        <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
          {eyebrow}
        </p>
        <h1 className="m-0 text-2xl font-semibold tracking-tight text-text">{title}</h1>
        <p className="m-0 max-w-3xl text-sm leading-relaxed text-muted">{description}</p>
      </div>
      <ul aria-label="Page summary" className="m-0 flex list-none flex-wrap gap-2 p-0">
        {badges.map((badge) => (
          <li
            key={badge}
            className="rounded-full border border-line bg-surface-2 px-2.5 py-1 font-mono text-[10px] text-muted"
          >
            {badge}
          </li>
        ))}
      </ul>
    </header>
  );
}

export function DocsCard({
  title,
  description,
  children,
  label,
  headingLevel = 2,
}: {
  title: string;
  description?: string;
  children: ReactNode;
  label?: string;
  headingLevel?: 2 | 3;
}) {
  const Heading = headingLevel === 3 ? "h3" : "h2";
  return (
    <section className="grid gap-3 rounded-lg border border-line bg-surface px-4 py-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <Heading className="m-0 text-sm font-semibold text-text">{title}</Heading>
          {description ? <p className="m-0 text-xs text-muted">{description}</p> : null}
        </div>
        {label ? (
          <span className="rounded-full border border-line-strong px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide text-muted">
            {label}
          </span>
        ) : null}
      </header>
      {children}
    </section>
  );
}

export function Callout({
  title,
  children,
  tone = "info",
  headingLevel = 2,
}: {
  title: string;
  children: ReactNode;
  tone?: "info" | "warning";
  headingLevel?: 2 | 3;
}) {
  const Heading = headingLevel === 3 ? "h3" : "h2";
  return (
    <aside
      className={
        tone === "warning"
          ? "grid gap-1 rounded-lg border border-warn/40 bg-warn-soft px-4 py-3"
          : "grid gap-1 rounded-lg border border-accent/30 bg-accent-soft px-4 py-3"
      }
    >
      <Heading
        className={
          tone === "warning"
            ? "m-0 text-xs font-semibold text-warn"
            : "m-0 text-xs font-semibold text-accent"
        }
      >
        {title}
      </Heading>
      <div className="text-xs leading-relaxed text-muted">{children}</div>
    </aside>
  );
}

export function CommandBlock({
  code,
  label,
}: {
  code: string;
  label: string;
}) {
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">("idle");

  async function copy() {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(code);
      setCopyState("copied");
    } catch {
      setCopyState("error");
    }
  }

  return (
    <figure className="m-0 overflow-hidden rounded-lg border border-line bg-surface-2">
      <figcaption className="flex items-center justify-between gap-3 border-b border-line px-3 py-2">
        <span className="font-mono text-[10px] uppercase tracking-wide text-subtle">{label}</span>
        <button
          type="button"
          onClick={() => void copy()}
          className="rounded border border-line-strong bg-surface px-2 py-1 text-[10px] font-semibold text-muted hover:bg-surface-3"
          aria-label={`Copy ${label}`}
        >
          {copyState === "copied" ? "Copied" : copyState === "error" ? "Copy failed" : "Copy"}
        </button>
        <span className="sr-only" role="status" aria-live="polite">
          {copyState === "copied"
            ? `${label} copied to clipboard`
            : copyState === "error"
              ? `${label} could not be copied`
              : ""}
        </span>
      </figcaption>
      <pre className="m-0 overflow-x-auto p-3 text-xs leading-relaxed text-text" tabIndex={0}>
        <code>{code}</code>
      </pre>
    </figure>
  );
}

export function DefinitionGrid({
  items,
}: {
  items: Array<{ term: string; detail: ReactNode; code?: boolean }>;
}) {
  return (
    <dl className="m-0 grid gap-px overflow-hidden rounded-lg border border-line bg-line sm:grid-cols-2">
      {items.map((item) => (
        <div key={item.term} className="grid content-start gap-1 bg-surface-2 p-3">
          <dt className={item.code ? "font-mono text-[11px] font-semibold text-text" : "text-xs font-semibold text-text"}>
            {item.term}
          </dt>
          <dd className="m-0 text-xs leading-relaxed text-muted">{item.detail}</dd>
        </div>
      ))}
    </dl>
  );
}
