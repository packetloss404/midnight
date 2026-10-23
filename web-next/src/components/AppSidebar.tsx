// Global instance navigation. Portfolio/settings pages use the wider labeled
// workspace rail; project workspaces collapse it to icons so the topology
// canvas keeps the maximum useful area.

import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { DOCUMENTATION_LINKS } from "@/lib/documentation";
import { BrandMark } from "./BrandMark";

type RailItem = {
  to: string;
  label: string;
  icon: ReactNode;
  activeOptions?: { exact: boolean };
};

const ITEMS: RailItem[] = [
  {
    to: "/",
    label: "Projects",
    activeOptions: { exact: true },
    icon: <GridIcon />,
  },
  { to: "/usage", label: "Usage & limits", icon: <GaugeIcon /> },
  { to: "/settings", label: "Instance settings", icon: <GearIcon /> },
];

export const SIDEBAR_COMPACT_STORAGE_KEY = "midnight.sidebar.compact.v1";

function storedCompactPreference(): boolean | null {
  if (typeof window === "undefined") return null;
  try {
    const value = window.localStorage.getItem(SIDEBAR_COMPACT_STORAGE_KEY);
    if (value === "true") return true;
    if (value === "false") return false;
  } catch {
    // Storage can be disabled by browser policy. The route default remains usable.
  }
  return null;
}

export function AppSidebar({
  operatorFeaturesAvailable = true,
  compact = false,
}: {
  operatorFeaturesAvailable?: boolean;
  compact?: boolean;
}) {
  const [compactPreference, setCompactPreference] = useState<boolean | null>(
    storedCompactPreference,
  );
  const isCompact = compactPreference ?? compact;

  function toggleCompact() {
    const next = !isCompact;
    setCompactPreference(next);
    try {
      window.localStorage.setItem(SIDEBAR_COMPACT_STORAGE_KEY, String(next));
    } catch {
      // The in-memory preference still works for this session.
    }
  }

  return (
    <nav
      id="primary-sidebar-navigation"
      aria-label="Primary"
      data-compact={isCompact ? "true" : "false"}
      className={`app-sidebar fixed bottom-0 left-0 z-40 flex h-14 w-full flex-row items-center gap-1 border-t border-line bg-bg-2 px-3 transition-[width] duration-200 sm:sticky sm:top-0 sm:h-screen sm:flex-col sm:items-stretch sm:border-r sm:border-t-0 sm:px-2.5 sm:py-3 ${
        isCompact ? "sm:w-[var(--rail-width)]" : "sm:w-[var(--sidebar-wide)]"
      }`}
    >
      <Link
        to="/"
        aria-label="Midnight home"
        className={`mr-2 flex h-9 items-center rounded-[var(--r-sm)] bg-[var(--brand-soft)] font-semibold text-brand sm:mb-4 sm:mr-0 ${
          isCompact ? "w-9 justify-center" : "gap-2.5 px-2.5"
        }`}
      >
        <span className="grid h-6 w-6 shrink-0 place-items-center text-brand" aria-hidden="true">
          <BrandMark className="h-6 w-6" />
        </span>
        {!isCompact ? (
          <span className="hidden min-w-0 sm:block">
            <span className="block truncate text-sm leading-none text-text">Midnight</span>
            <span className="mt-1 block truncate font-mono text-[9px] uppercase tracking-[0.16em] text-muted">
              Control plane
            </span>
          </span>
        ) : null}
      </Link>
      <button
        type="button"
        onClick={toggleCompact}
        aria-label={isCompact ? "Expand sidebar" : "Collapse sidebar"}
        aria-controls="primary-sidebar-navigation"
        aria-expanded={!isCompact}
        title={isCompact ? "Expand sidebar" : "Collapse sidebar"}
        className={`mb-2 hidden h-8 items-center rounded-[var(--r-sm)] border border-transparent text-muted transition-colors hover:border-line hover:bg-surface-2 hover:text-text focus-visible:border-accent sm:flex ${
          isCompact ? "w-9 justify-center" : "gap-2 px-2.5"
        }`}
      >
        <SidebarToggleIcon compact={isCompact} />
        {!isCompact ? (
          <span className="truncate text-[11px] font-medium">Collapse</span>
        ) : null}
      </button>
      {!isCompact ? (
        <span className="mb-1 hidden px-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted sm:block">
          Workspace
        </span>
      ) : null}
      {ITEMS.filter(
        (item) => operatorFeaturesAvailable || item.to !== "/settings",
      ).map((item) => (
        <Link
          key={item.to}
          to={item.to}
          title={item.label}
          aria-label={item.label}
          activeOptions={item.activeOptions}
          className={`group flex h-9 items-center rounded-[var(--r-sm)] text-muted transition-colors hover:bg-surface-2 hover:text-text [&.active]:bg-accent-soft [&.active]:text-accent ${
            isCompact ? "w-9 justify-center" : "gap-3 px-2.5"
          }`}
        >
          <span className="grid h-5 w-5 shrink-0 place-items-center">{item.icon}</span>
          {!isCompact ? (
            <span className="hidden truncate text-[13px] font-medium sm:block">
              {item.label}
            </span>
          ) : null}
        </Link>
      ))}
      <section
        aria-label="Docs"
        className={`ml-auto flex items-center gap-1 sm:ml-0 sm:mt-auto sm:grid ${
          isCompact ? "" : "sm:gap-0.5"
        }`}
      >
        {!isCompact ? (
          <div className="mb-1 hidden items-center gap-2 px-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted sm:flex">
            <BookIcon />
            <span>DOCS</span>
          </div>
        ) : null}
        {(isCompact
          ? DOCUMENTATION_LINKS.slice(0, 1)
          : DOCUMENTATION_LINKS
        ).map((item, index) => (
          <Link
            key={item.section}
            to={item.to}
            title={item.label}
            aria-label={item.label}
            activeOptions={
              isCompact || index > 0 ? undefined : { exact: true }
            }
            className={`${
              isCompact
                ? "flex h-9 w-9 items-center justify-center"
                : index === 0
                  ? "flex h-9 items-center sm:px-2.5"
                  : "hidden h-8 items-center px-2.5 sm:flex"
            } rounded-[var(--r-sm)] text-muted transition-colors hover:bg-surface-2 hover:text-text [&.active]:bg-accent-soft [&.active]:text-accent`}
          >
            {isCompact || index === 0 ? (
              <span
                aria-hidden="true"
                className={
                  isCompact
                    ? "grid h-5 w-5 place-items-center"
                    : "grid h-5 w-5 place-items-center sm:hidden"
                }
              >
                <BookIcon />
              </span>
            ) : null}
            {!isCompact ? (
              <span
                className={`${index === 0 ? "hidden sm:inline" : ""} truncate text-xs font-medium`}
              >
                {item.label}
              </span>
            ) : null}
          </Link>
        ))}
      </section>
      {!isCompact ? (
        <div className="mt-2 hidden rounded-lg border border-line-soft bg-bg px-3 py-2.5 sm:block">
          <span className="flex items-center gap-2 text-[11px] font-medium text-muted">
            <span className="h-1.5 w-1.5 rounded-full bg-accent shadow-[0_0_10px_var(--accent)]" />
            Self-hosted
          </span>
          <span className="mt-1 block text-[10px] leading-relaxed text-muted">
            Your infrastructure, one control plane.
          </span>
        </div>
      ) : null}
    </nav>
  );
}

function SidebarToggleIcon({ compact }: { compact: boolean }) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
    >
      <rect
        x="3"
        y="4"
        width="18"
        height="16"
        rx="2"
        stroke="currentColor"
        strokeWidth="1.6"
      />
      <path d="M8 4v16" stroke="currentColor" strokeWidth="1.6" />
      <path
        d={compact ? "m13 9 3 3-3 3" : "m16 9-3 3 3 3"}
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function GridIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <rect x="3" y="3" width="7" height="7" rx="1.5" stroke="currentColor" strokeWidth="1.7" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" stroke="currentColor" strokeWidth="1.7" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" stroke="currentColor" strokeWidth="1.7" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" stroke="currentColor" strokeWidth="1.7" />
    </svg>
  );
}

function GearIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="3" stroke="currentColor" strokeWidth="1.7" />
      <path
        d="M12 2v3m0 14v3M4.2 4.2l2.1 2.1m11.4 11.4l2.1 2.1M2 12h3m14 0h3M4.2 19.8l2.1-2.1m11.4-11.4l2.1-2.1"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
      />
    </svg>
  );
}

function GaugeIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M4 19a8 8 0 1 1 16 0" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" />
      <path d="m12 15 4-5" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" />
      <circle cx="12" cy="15" r="1.5" fill="currentColor" />
    </svg>
  );
}

function BookIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
    >
      <path
        d="M4.5 5.5A2.5 2.5 0 0 1 7 3h5v16H7a2.5 2.5 0 0 0-2.5 2.5v-16ZM19.5 5.5A2.5 2.5 0 0 0 17 3h-5v16h5a2.5 2.5 0 0 1 2.5 2.5v-16Z"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinejoin="round"
      />
    </svg>
  );
}
