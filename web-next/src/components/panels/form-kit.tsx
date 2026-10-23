// Small form primitives shared by create, GitHub, and settings surfaces.

import type { InputHTMLAttributes, ReactNode } from "react";
import clsx from "clsx";
import { presentError } from "@/lib/errorPresentation";

export function Field({
  label,
  hint,
  htmlFor,
  labelId,
  hintId,
  children,
}: {
  label: string;
  hint?: string;
  htmlFor?: string;
  labelId?: string;
  hintId?: string;
  children: ReactNode;
}) {
  return (
    <label htmlFor={htmlFor} className="grid gap-1.5">
      <span id={labelId} className="text-xs font-semibold text-text">{label}</span>
      {children}
      {hint ? <span id={hintId} className="text-[11px] text-muted">{hint}</span> : null}
    </label>
  );
}

export function TextInput({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...rest}
      className={clsx(
        "w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text",
        "placeholder:text-subtle focus:border-accent focus:outline-none disabled:opacity-60",
        className,
      )}
    />
  );
}

export function Button({
  children,
  onClick,
  type = "button",
  variant = "primary",
  disabled,
  className,
}: {
  children: ReactNode;
  onClick?: () => void;
  type?: "button" | "submit";
  variant?: "primary" | "secondary" | "danger";
  disabled?: boolean;
  className?: string;
}) {
  const tones: Record<string, string> = {
    primary: "border-accent bg-accent-soft text-accent hover:bg-surface-3",
    secondary: "border-line-strong bg-surface-2 text-text hover:bg-surface-3",
    danger: "border-danger/50 bg-danger-soft text-danger hover:bg-danger/10",
  };
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={clsx(
        "inline-flex items-center justify-center gap-2 rounded-[var(--r-sm)] border px-3.5 py-2 text-xs font-semibold transition-colors",
        "disabled:cursor-not-allowed disabled:opacity-50",
        tones[variant],
        className,
      )}
    >
      {children}
    </button>
  );
}

/** Inline error/notice strip (non-blocking, sits under a form). */
export function InlineError({ error }: { error: unknown }) {
  const message = presentError(error).message;
  return (
    <div
      role="alert"
      className="rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger"
    >
      {message}
    </div>
  );
}

/** Section card wrapper used across settings surfaces. */
export function Card({
  title,
  description,
  action,
  children,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section className="grid gap-3 rounded-lg border border-line bg-surface px-4 py-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <h2 className="m-0 text-sm font-semibold text-text">{title}</h2>
          {description ? <p className="m-0 text-xs text-muted">{description}</p> : null}
        </div>
        {action}
      </header>
      {children}
    </section>
  );
}
