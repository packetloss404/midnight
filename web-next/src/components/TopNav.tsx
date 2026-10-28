// Top bar rendered above the route outlet. Left → right:
//   1. Breadcrumb (Midnight / <project>).
//   2. Project switcher (jump between projects).
//   3. Active environment switcher (writes `?env=` — shareable via URL).
//   4. "✦ Open agent" affordance, shown only when the dock is collapsed.
//
// Presentational: switcher data + navigation callbacks are injected by
// RootLayout, which owns the router. `breadcrumbFor` stays exported for the
// unit test and the sidebar.

import type { Environment, Project } from "@/api/client";
import { COMPARE_ENVIRONMENTS_SCOPE } from "@/lib/env-scope";
import {
  openAgentDock,
  useAgentDockCollapsed,
} from "./AgentPanel/agent-dock-state";

export type TopNavProps = {
  /** Active route path — drives the breadcrumb. */
  pathname: string;
  projectId?: string;
  projectLabel?: string;
  /** Selected environment id, compare sentinel, or undefined while resolving. */
  environmentId?: string;
  projects?: Project[];
  environments?: Environment[];
  onSelectProject?: (projectId: string) => void;
  onSelectEnvironment?: (environmentScope: string) => void;
  identity?: {
    displayName: string;
    email: string;
  };
  authMode?: "human" | "operator";
  operatorFeaturesAvailable?: boolean;
  agentAvailable?: boolean;
  onLogout?: () => void;
  logoutError?: string;
  onLocalLogout?: () => void;
};

export function TopNav({
  pathname,
  projectId,
  projectLabel,
  environmentId,
  projects,
  environments,
  onSelectProject,
  onSelectEnvironment,
  identity,
  authMode,
  operatorFeaturesAvailable = true,
  agentAvailable = operatorFeaturesAvailable,
  onLogout,
  logoutError,
  onLocalLogout,
}: TopNavProps) {
  const collapsed = useAgentDockCollapsed();
  const crumbs = breadcrumbFor(pathname);
  if (projectLabel && crumbs.length > 1) crumbs[1] = projectLabel;

  return (
    <header
      data-testid="top-nav"
      className="sticky top-0 z-30 flex h-12 flex-shrink-0 items-center gap-2 border-b border-line bg-bg-2 px-2 sm:gap-3 sm:px-4"
      style={{ height: "var(--top-nav-height)" }}
    >
      <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-[13px]">
        {crumbs.map((crumb, i) => (
          <span key={`${crumb}-${i}`} className="inline-flex min-w-0 items-center gap-1.5">
            {i > 0 && (
              <span aria-hidden="true" className="text-subtle">
                /
              </span>
            )}
            <span
              data-testid={i === 0 ? "crumb-root" : `crumb-${i}`}
              className={
                i === crumbs.length - 1
                  ? "truncate font-semibold text-text"
                  : "truncate font-medium text-muted"
              }
              style={i > 0 ? { fontFamily: "var(--font-mono)" } : undefined}
            >
              {crumb}
            </span>
          </span>
        ))}
      </nav>

      <div className="flex-1" />

      {projects && projects.length > 0 && onSelectProject ? (
        <span className="hidden md:inline-flex">
          <SelectControl
            testId="project-switcher"
            ariaLabel="Switch project"
            value={projectId ?? ""}
            onChange={(v) => v && onSelectProject(v)}
            placeholder="Select project"
            options={projects.map((p) => ({
              value: p.id,
              label: p.display_name || p.slug,
            }))}
          />
        </span>
      ) : null}

      {projectId && onSelectEnvironment ? (
        <SelectControl
          testId="env-switcher"
          ariaLabel="Switch environment"
          dot
          value={environmentId ?? ""}
          onChange={onSelectEnvironment}
          placeholder="Select environment"
          options={[
            {
              value: COMPARE_ENVIRONMENTS_SCOPE,
              label: "Compare environments",
            },
            ...(environments ?? []).map((e) => ({
              value: e.id,
              label: e.name || e.slug,
            })),
          ]}
        />
      ) : null}

      {collapsed && agentAvailable && (
        <button
          type="button"
          onClick={openAgentDock}
          data-testid="open-agent-button"
          aria-label="Open agent panel"
          className="inline-flex h-7 items-center gap-1.5 rounded-[var(--r-sm)] border border-line bg-surface-2 px-2.5 text-xs font-semibold text-accent transition-colors hover:bg-surface-3"
        >
          <span aria-hidden="true">✦</span>
          <span className="hidden sm:inline">Open agent</span>
        </button>
      )}

      {authMode ? (
        <details className="relative">
          <summary
            className="grid h-8 w-8 cursor-pointer list-none place-items-center rounded-full border border-line bg-surface-2 text-[11px] font-semibold uppercase text-accent"
            aria-label="Open account menu"
          >
            {identity
              ? initials(identity.displayName || identity.email)
              : "OP"}
          </summary>
          <div className="absolute right-0 top-10 z-50 grid w-64 gap-3 rounded-lg border border-line bg-surface p-3 shadow-lg">
            <div className="grid gap-0.5">
              <strong className="truncate text-xs text-text">
                {identity?.displayName || "Operator recovery mode"}
              </strong>
              <span className="truncate text-[11px] text-muted">
                {identity?.email ||
                  "Installation-wide privileged credential"}
              </span>
            </div>
            {authMode === "human" && !operatorFeaturesAvailable ? (
              <p className="m-0 text-[11px] text-muted">
                Project-scoped access is active; available controls follow your
                current role. Instance settings, agents, and fleet telemetry
                require an installation-administrator account.
              </p>
            ) : null}
            {logoutError ? (
              <div className="grid gap-2 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft p-2">
                <p role="alert" className="m-0 text-[11px] text-danger">
                  {logoutError} The credential is still present in this tab.
                </p>
                {onLocalLogout ? (
                  <button
                    type="button"
                    onClick={onLocalLogout}
                    className="rounded-[var(--r-sm)] border border-danger/40 px-2 py-1.5 text-left text-[11px] font-semibold text-danger"
                  >
                    Sign out locally without revoking
                  </button>
                ) : null}
              </div>
            ) : null}
            {onLogout ? (
              <button
                type="button"
                onClick={onLogout}
                className="rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-left text-xs font-semibold text-muted hover:bg-surface-3"
              >
                {authMode === "human"
                  ? "Sign out of this session"
                  : "Clear recovery access"}
              </button>
            ) : null}
          </div>
        </details>
      ) : null}
    </header>
  );
}

type Option = { value: string; label: string };

/**
 * Compact, accessible dropdown built on a native <select> (keyboard + SR
 * friendly out of the box) styled to match the console chrome.
 */
function SelectControl({
  testId,
  ariaLabel,
  value,
  onChange,
  options,
  placeholder,
  dot = false,
}: {
  testId: string;
  ariaLabel: string;
  value: string;
  onChange: (value: string) => void;
  options: Option[];
  placeholder?: string;
  dot?: boolean;
}) {
  return (
    <label className="relative inline-flex items-center">
      {dot && (
        <span
          aria-hidden="true"
          className="pointer-events-none absolute left-2.5 h-1.5 w-1.5 rounded-full bg-accent"
        />
      )}
      <select
        data-testid={testId}
        aria-label={ariaLabel}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={`h-7 max-w-[180px] appearance-none truncate rounded-[var(--r-sm)] border border-line bg-surface ${
          dot ? "pl-5" : "pl-2.5"
        } pr-6 text-xs text-text transition-colors hover:border-line-strong focus-visible:outline-none`}
        style={{ fontFamily: "var(--font-mono)" }}
      >
        {placeholder && !value ? (
          <option value="" disabled>
            {placeholder}
          </option>
        ) : null}
        {options.map((o) => (
          <option key={o.value || "__all__"} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <span
        aria-hidden="true"
        className="pointer-events-none absolute right-2 text-[10px] text-subtle"
      >
        ▾
      </span>
    </label>
  );
}

/** Build a breadcrumb trail from a pathname. */
export function breadcrumbFor(pathname: string): string[] {
  const root = "Midnight";
  const projectMatch = pathname.match(/^\/projects\/([^/]+)/);
  if (projectMatch) {
    const section = pathname.match(/^\/projects\/[^/]+\/([^/]+)/)?.[1];
    return [
      root,
      projectMatch[1],
      ...(section ? [section.charAt(0).toUpperCase() + section.slice(1)] : []),
    ];
  }
  const docsSection = pathname.match(/^\/docs\/([^/]+)\/?$/)?.[1];
  if (pathname === "/docs" || pathname === "/docs/") {
    return [root, "Docs"];
  }
  if (pathname.startsWith("/docs/")) {
    const sectionLabel =
      docsSection === "agent-setup"
        ? "Agent Setup"
        : docsSection === "api"
          ? "API reference"
          : docsSection === "cli"
            ? "CLI reference"
            : undefined;
    return [root, "Docs", ...(sectionLabel ? [sectionLabel] : [])];
  }
  if (pathname === "/settings") return [root, "Instance settings"];
  if (pathname === "/new") return [root, "New project"];
  return [root];
}

function initials(value: string): string {
  const parts = value.trim().split(/\s+/).filter(Boolean);
  if (parts.length > 1) {
    return `${parts[0]?.[0] ?? ""}${parts[parts.length - 1]?.[0] ?? ""}`.toUpperCase();
  }
  return value.trim().slice(0, 2).toUpperCase() || "ME";
}
