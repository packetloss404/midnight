import type { ReactNode } from "react";
import { AgentDock, type AgentRouteContext } from "./AgentPanel";
import {
  useAgentDockCollapsed,
  useAgentDockPinned,
} from "./AgentPanel/agent-dock-state";

// Top-level shell. Layout, left-to-right:
//
//   [ 56px left rail ] [ top bar + full-width content ]  ( + floating dock )
//
// The content area is FULL WIDTH — there is no permanent right-rail padding.
// The AgentDock floats OVER the content (overlay) by default and only
// reserves a docked column when the user explicitly PINS it (opt-in). The
// dock is mounted exactly once here so an in-flight SSE stream survives
// navigation.
//
// This component is presentational: all router wiring (breadcrumb, env +
// project switchers, sidebar links, and the derived agent route context) is
// injected by `RootLayout` in `routes/routes.tsx` as `topBar` / `sidebar` /
// `routeContext`. That keeps the shell unit-testable without a router and
// removed the old `history.pushState` monkey-patch.

const DEFAULT_ROUTE_CONTEXT: AgentRouteContext = {
  routeId: "/",
  projectId: null,
  serviceId: null,
  environmentId: null,
  contextLabel: null,
};

export type AppShellProps = {
  children?: ReactNode;
  /** Left icon rail (router-aware). Omitted in bare unit tests. */
  sidebar?: ReactNode;
  /** Top bar node (breadcrumb + switchers). Omitted in bare unit tests. */
  topBar?: ReactNode;
  /** Context handed to the agent dock; derived from the active route. */
  routeContext?: AgentRouteContext;
  /** Whether the authenticated principal may use the privileged agent surface. */
  agentEnabled?: boolean;
};

export function AppShell({
  children,
  sidebar,
  topBar,
  routeContext = DEFAULT_ROUTE_CONTEXT,
  agentEnabled = true,
}: AppShellProps) {
  const collapsed = useAgentDockCollapsed();
  const pinned = useAgentDockPinned();

  // Reserve a docked column only when the panel is pinned AND open.
  const reserveDock = agentEnabled && pinned && !collapsed;

  return (
    <div data-testid="app-shell" className="flex min-h-[100dvh] w-full bg-bg">
      <a className="skip-link" href="#main-outlet">
        Skip to main content
      </a>
      {sidebar}
      <div
        className={`app-shell-content flex min-h-[100dvh] min-w-0 flex-1 flex-col transition-[padding] duration-200 ${
          sidebar ? "pb-14 sm:pb-0" : ""
        }`}
        data-dock-reserved={reserveDock ? "true" : "false"}
      >
        {topBar}
        <main id="main-outlet" className="relative flex min-h-0 flex-1 flex-col">
          {children}
        </main>
      </div>
      {agentEnabled ? <AgentDock context={routeContext} /> : null}
    </div>
  );
}
