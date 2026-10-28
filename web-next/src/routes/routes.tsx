// TanStack Router route tree, declared by hand. Information architecture:
//
//   /                                       projects index
//   /new                                    new-project flow
//   /projects/$projectId                    resource workspace
//   /projects/$projectId/services/$serviceId/$section
//                                           split service inspector / mobile page
//   /projects/$projectId/deployments        activity feed
//   /projects/$projectId/observability      metrics/logs
//   /projects/$projectId/settings           project settings
//   /settings                               instance settings
//   /usage                                  workspace usage and limits
//   /docs[/agent-setup|/api|/cli]            product documentation
//
// `?env=<environmentId>` is a shareable search param on every project-scoped
// route (see `lib/env-scope.ts`). RootLayout owns all router wiring and hands
// the presentational AppShell its sidebar, top bar, and agent route context —
// so there is no `history.pushState` monkey-patch anymore.

import {
  Link,
  Outlet,
  RootRoute,
  Route,
  Router,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import { useEffect, type ReactNode } from "react";
import { useAuth } from "@/auth/AuthProvider";
import { AppShell } from "@/components/AppShell";
import { AppSidebar } from "@/components/AppSidebar";
import { TopNav } from "@/components/TopNav";
import { ProjectNav } from "@/components/ProjectNav";
import { InvitationAcceptance } from "@/components/panels/invitations/InvitationAcceptance";
import { CommandPalette } from "@/components/CommandPalette";
import type { AgentRouteContext } from "@/components/AgentPanel";
import { useEnvironments, useIdentityWorkspaces, useProjects } from "@/api/hooks";
import type { Project } from "@/api/client";
import {
  COMPARE_ENVIRONMENTS_SCOPE,
  concreteEnvironmentId,
  isCompareEnvironmentScope,
  parseEnvSearch,
  selectDefaultEnvironmentId,
  type EnvSearch,
} from "@/lib/env-scope";
import { ProjectsIndex } from "@/pages/ProjectsIndex";
import { ProjectCanvas } from "@/pages/ProjectCanvas";
import { NewProject } from "@/pages/NewProject";
import { ProjectDeployments } from "@/pages/ProjectDeployments";
import { ProjectObservability } from "@/pages/ProjectObservability";
import { ProjectSettings } from "@/pages/ProjectSettings";
import { InstanceSettings } from "@/pages/InstanceSettings";
import { UsageLimits } from "@/pages/UsageLimits";
import { Documentation } from "@/pages/Documentation";
import { APIReferenceDocs } from "@/pages/docs/APIReferenceDocs";
import { AgentSetupDocs } from "@/pages/docs/AgentSetupDocs";
import { CLIReferenceDocs } from "@/pages/docs/CLIReferenceDocs";
import { DocsHome } from "@/pages/docs/DocsHome";
import { Login } from "@/pages/Login";
import { Setup } from "@/pages/Setup";
import { presentError } from "@/lib/errorPresentation";
import {
  normalizeConfigureSection,
  normalizeServiceSection,
  type ConfigureSection,
} from "@/components/ServiceDetailDrawer";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";

// ── Root layout (router-aware shell wiring) ───────────────────────

// /login and /setup render outside the authenticated shell: both run before a
// human session exists, and /setup runs before the installation has any account
// at all.
const UNAUTHENTICATED_ROUTES = new Set(["/login", "/setup"]);

function RouterRoot() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  return UNAUTHENTICATED_ROUTES.has(pathname) ? <Outlet /> : <AuthenticatedApp />;
}

function AuthenticatedApp() {
  const auth = useAuth();
  const navigate = useNavigate();

  useEffect(() => {
    if (auth.state.status !== "anonymous") return;
    const redirect = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    void navigate({
      to: "/login",
      search: { redirect },
      replace: true,
    } as never);
  }, [auth.state.status, navigate]);

  if (auth.state.status === "checking") {
    return <AuthStatus title="Checking your session…" />;
  }
  if (auth.state.status === "unavailable") {
    return (
      <AuthStatus
        title="Identity service unavailable"
        detail="Your saved session was not discarded. Retry when the control plane is reachable."
        action={
          <button
            type="button"
            onClick={() => void auth.retryBootstrap()}
            className="rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs font-semibold text-text"
          >
            Retry
          </button>
        }
      />
    );
  }
  if (auth.state.status === "anonymous") {
    return <AuthStatus title="Opening sign in…" />;
  }
  return <RootLayout />;
}

function AuthStatus({
  title,
  detail,
  action,
}: {
  title: string;
  detail?: string;
  action?: ReactNode;
}) {
  return (
    <main className="grid min-h-[100dvh] place-items-center px-4">
      <section
        role="status"
        className="grid max-w-md gap-3 rounded-lg border border-line bg-surface p-5 text-center"
      >
        <h1 className="m-0 text-lg font-semibold text-text">{title}</h1>
        {detail ? <p className="m-0 text-sm text-muted">{detail}</p> : null}
        {action}
      </section>
    </main>
  );
}

function NotFoundState() {
  return (
    <section className="mx-auto grid w-full max-w-2xl gap-3 p-[var(--main-gutter)]">
      <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-muted">
        Not found
      </p>
      <h1 className="m-0 text-xl font-semibold text-text">Page not found</h1>
      <p className="m-0 text-sm text-muted">
        This route does not exist. Check the address or return to the project overview.
      </p>
      <div>
        <Link
          to="/"
          className="inline-flex rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs font-semibold text-text"
        >
          Back to projects
        </Link>
      </div>
    </section>
  );
}

function RootLayout() {
  const auth = useAuth();
  const navigate = useNavigate();
  const location = useRouterState({ select: (s) => s.location });
  const pathname = location.pathname;
  const search = location.search as EnvSearch & { service?: string };

  const projectId = pathname.match(/^\/projects\/([^/]+)/)?.[1];
  const environmentScope = search.env;
  const comparingEnvironments = isCompareEnvironmentScope(environmentScope);
  const environmentId = concreteEnvironmentId(environmentScope);
  const servicePathMatch = pathname.match(
    /^\/projects\/[^/]+\/services\/([^/]+)(?:\/|$)/,
  );
  const serviceId = servicePathMatch
    ? decodeURIComponent(servicePathMatch[1])
    : search.service;

  const projectsQ = useProjects();
  const environmentsQ = useEnvironments(projectId);
  const defaultEnvironmentId = selectDefaultEnvironmentId(environmentsQ.data);

  useEffect(() => {
    if (!projectId || environmentScope || !defaultEnvironmentId) return;
    void navigate({
      to: pathname,
      replace: true,
      search: (previous: Record<string, unknown>) => ({
        ...previous,
        env: defaultEnvironmentId,
      }),
    } as never);
  }, [
    defaultEnvironmentId,
    environmentScope,
    navigate,
    pathname,
    projectId,
  ]);

  const routeContext: AgentRouteContext = {
    routeId: pathname,
    projectId: projectId ?? null,
    serviceId: serviceId ?? null,
    environmentId: environmentId ?? null,
    contextLabel: buildContextLabel(projectId, serviceId, environmentId),
  };
  const humanIdentity =
    auth.state.status === "human"
      ? {
          displayName: auth.state.me.principal.display_name,
          email: auth.state.me.principal.email,
        }
      : undefined;
  const authMode =
    auth.state.status === "human"
      ? "human"
      : auth.state.status === "operator"
        ? "operator"
        : undefined;
  const humanProjectRouteAdmitted = isHumanProjectRouteAdmitted(pathname);
  const requiresEnvironment = projectRouteRequiresEnvironment(pathname);

  async function logout() {
    let completed = false;
    if (auth.state.status === "human") {
      completed = await auth.logoutHuman();
    } else if (auth.state.status === "operator") {
      completed = await auth.clearOperatorRecovery();
    }
    if (!completed) return;
    await navigate({ to: "/login", search: {}, replace: true } as never);
  }

  async function logoutLocally() {
    const completed = await auth.logoutHumanLocally();
    if (!completed) return;
    await navigate({ to: "/login", search: {}, replace: true } as never);
  }

  return (
    <AppShell
      sidebar={
        <AppSidebar
          operatorFeaturesAvailable={auth.operatorFeaturesAvailable}
          compact={Boolean(projectId)}
        />
      }
      routeContext={routeContext}
      agentEnabled={auth.operatorFeaturesAvailable}
      topBar={
        <>
          <TopNav
            pathname={pathname}
            projectId={projectId}
            projectLabel={
              projectsQ.data?.find((project) => project.id === projectId)
                ?.display_name ||
              projectsQ.data?.find((project) => project.id === projectId)?.slug
            }
            environmentId={environmentScope}
            projects={projectsQ.data}
            environments={environmentsQ.data}
            identity={humanIdentity}
            authMode={authMode}
            operatorFeaturesAvailable={auth.operatorFeaturesAvailable}
            onLogout={authMode ? () => void logout() : undefined}
            logoutError={
              auth.sessionActionError
                ? presentError(auth.sessionActionError).message
                : undefined
            }
            onLocalLogout={
              auth.state.status === "human"
                ? () => void logoutLocally()
                : undefined
            }
            onSelectProject={(id) =>
              navigate({ to: "/projects/$projectId", params: { projectId: id }, search: {} })
            }
            onSelectEnvironment={(env) => {
              // Compare is an explicit read-only scope. A nested service route
              // is closed instead of merely hidden in search state.
              if (env === COMPARE_ENVIRONMENTS_SCOPE && projectId) {
                return navigate({
                  to: "/projects/$projectId",
                  params: { projectId },
                  search: { env },
                });
              }
              return navigate({
                to: pathname,
                search: (prev: Record<string, unknown>) => ({
                  ...prev,
                  env,
                }),
              } as never);
            }}
          />
          {projectId ? (
            <ProjectNav
              projectId={projectId}
              environmentId={environmentScope}
              observabilityAvailable={auth.operatorFeaturesAvailable}
            />
          ) : null}
        </>
      }
    >
      {auth.state.status === "human" &&
      !auth.operatorFeaturesAvailable &&
      !humanProjectRouteAdmitted ? (
        <HumanTenantSurface
          pathname={pathname}
          projects={projectsQ.data}
        />
      ) : projectId && requiresEnvironment && !environmentScope ? (
        <EnvironmentResolutionState
          isPending={environmentsQ.isPending || Boolean(defaultEnvironmentId)}
          hasError={Boolean(environmentsQ.error)}
          projectId={projectId}
          humanSession={auth.state.status === "human"}
        />
      ) : projectId && comparingEnvironments ? (
        <EnvironmentCompareState environments={environmentsQ.data ?? []} />
      ) : (
        <Outlet />
      )}
      {auth.operatorFeaturesAvailable ? (
        <CommandPalette
          navigate={navigate}
          context={{
            projectId: comparingEnvironments ? undefined : projectId,
            serviceId: comparingEnvironments ? undefined : serviceId,
            environmentId,
          }}
        />
      ) : null}
    </AppShell>
  );
}

/**
 * Human sessions can use tenant-scoped project administration. Observability
 * remains recovery-only until every widget has a project-authorized transport.
 */
export function isHumanProjectRouteAdmitted(pathname: string): boolean {
  return (
    pathname === "/new" ||
    pathname === "/usage" ||
    (pathname === "/docs" || pathname.startsWith("/docs/")) ||
    /^\/projects\/[^/]+\/?$/.test(pathname) ||
    /^\/projects\/[^/]+\/services\/[^/]+\/[^/]+\/?$/.test(pathname) ||
    /^\/projects\/[^/]+\/deployments\/?$/.test(pathname) ||
    /^\/projects\/[^/]+\/settings\/?$/.test(pathname)
  );
}

/** Routes whose actions are ambiguous without one concrete environment. */
export function projectRouteRequiresEnvironment(pathname: string): boolean {
  return (
    /^\/projects\/[^/]+\/?$/.test(pathname) ||
    /^\/projects\/[^/]+\/services\/[^/]+\/[^/]+\/?$/.test(pathname) ||
    /^\/projects\/[^/]+\/deployments\/?$/.test(pathname)
  );
}

function HumanTenantSurface({
  pathname,
  projects,
}: {
  pathname: string;
  projects?: Project[];
}) {
  if (pathname === "/") {
    return <HumanProjectsIndex projects={projects} />;
  }
  return <OperatorCapabilityState />;
}

function HumanProjectsIndex({ projects }: { projects?: Project[] }) {
  const workspaces = useIdentityWorkspaces();
  const canCreateProject = (workspaces.data ?? []).some(
    (workspace) => workspace.role === "admin" || workspace.role === "member",
  );
  return (
    <section className="mx-auto flex w-full max-w-5xl flex-col gap-6 p-[var(--main-gutter)]">
      <header>
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
              Tenant overview
            </p>
            <h1 className="m-0 mt-1 text-2xl font-semibold tracking-tight">
              Your projects
            </h1>
          </div>
          {canCreateProject ? (
            <Link
              to="/new"
              className="inline-flex h-9 items-center rounded-[var(--r-sm)] border border-accent bg-accent px-3 text-sm font-semibold text-accent-ink"
            >
              + New project
            </Link>
          ) : null}
        </div>
        <p className="m-0 mt-1 text-sm text-muted">
          Human sessions show only projects granted through workspace or direct
          project membership. Open a project to view the environments, services,
          deployments, configuration, and logs allowed by your role.
        </p>
      </header>
      <InvitationAcceptance />
      {!projects ? (
        <p className="m-0 text-sm text-muted">Loading projects…</p>
      ) : null}
      {projects?.length === 0 ? (
        <div className="rounded-lg border border-dashed border-line bg-bg-2 p-6 text-sm text-muted">
          No projects are visible to this account.
        </div>
      ) : null}
      {projects && projects.length > 0 ? (
        <ul className="m-0 grid list-none gap-3 p-0 [grid-template-columns:repeat(auto-fill,minmax(min(100%,360px),1fr))]">
          {projects.map((project) => (
            <li key={project.id} className="rounded-lg border border-line bg-surface">
              <Link
                to="/projects/$projectId"
                params={{ projectId: project.id }}
                search={{}}
                className="flex items-center justify-between gap-4 px-4 py-3"
              >
                <span className="min-w-0">
                  <strong className="block truncate text-sm text-text">
                    {project.display_name || project.slug}
                  </strong>
                  <span className="block truncate font-mono text-[11px] text-muted">
                    {project.slug}
                  </span>
                </span>
                <span className="text-muted" aria-hidden="true">
                  →
                </span>
              </Link>
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

function OperatorCapabilityState() {
  return (
    <section className="mx-auto grid w-full max-w-2xl gap-3 p-[var(--main-gutter)]">
      <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-warn">
        Installation capability
      </p>
      <h1 className="m-0 text-xl font-semibold text-text">
        Installation administrator required
      </h1>
      <p className="m-0 text-sm text-muted">
        This route can deploy, configure, or inspect installation-wide runtime
        data. Sign in with an installation-administrator account to use it.
        Emergency recovery remains available if normal administrator sign-in
        is unavailable.
      </p>
      <div>
        <Link
          to="/login"
          search={{
            redirect: `${window.location.pathname}${window.location.search}`,
          }}
          className="inline-flex rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs font-semibold text-text"
        >
          Sign in as administrator
        </Link>
      </div>
    </section>
  );
}

function EnvironmentResolutionState({
  isPending,
  hasError,
  projectId,
  humanSession,
}: {
  isPending: boolean;
  hasError: boolean;
  projectId: string;
  humanSession: boolean;
}) {
  const access = useProjectAccess(projectId, undefined, humanSession);
  const canCreateEnvironment =
    humanSession &&
    !access.isPending &&
    !access.error &&
    access.data?.project_id === projectId &&
    access.data.environment_id === undefined &&
    access.data.permissions.includes("environment.write");
  return (
    <section
      role={hasError ? "alert" : "status"}
      className="mx-auto grid w-full max-w-3xl gap-2 p-[var(--main-gutter)]"
    >
      <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
        Environment
      </p>
      <h1 className="m-0 text-xl font-semibold tracking-tight">
        {hasError
          ? "Could not load environments"
          : isPending
            ? "Opening the default environment…"
            : "No environment is available"}
      </h1>
      <p className="m-0 text-sm text-muted">
        {hasError
          ? "Reload the project before configuring or deploying services."
          : isPending
            ? "Midnight is resolving Production so every project action has a concrete scope."
            : "Create an environment before configuring or deploying services in this project."}
      </p>
      {!hasError && !isPending && canCreateEnvironment ? (
        <div>
          <Link
            to="/projects/$projectId/settings"
            params={{ projectId }}
            search={{}}
            className="inline-flex rounded-[var(--r-sm)] border border-accent bg-accent px-3 py-2 text-xs font-semibold text-accent-ink"
          >
            Create an environment
          </Link>
        </div>
      ) : null}
    </section>
  );
}

function EnvironmentCompareState({
  environments,
}: {
  environments: Array<{ id: string; name: string; slug: string; tier: string }>;
}) {
  return (
    <section className="mx-auto grid w-full max-w-4xl gap-4 p-[var(--main-gutter)]">
      <header className="grid gap-1">
        <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
          Read-only comparison
        </p>
        <h1 className="m-0 text-xl font-semibold tracking-tight">
          Compare environments
        </h1>
        <p className="m-0 text-sm text-muted">
          Select a concrete environment above to configure, deploy, or run service actions.
        </p>
      </header>
      <ul className="m-0 grid list-none gap-3 p-0 sm:grid-cols-2 lg:grid-cols-3">
        {environments.map((environment) => (
          <li
            key={environment.id}
            className="grid gap-1 rounded-lg border border-line bg-surface px-4 py-3"
          >
            <strong className="text-sm text-text">
              {environment.name || environment.slug}
            </strong>
            <span className="font-mono text-[11px] text-muted">
              {environment.slug}
            </span>
            <span className="text-[10px] uppercase tracking-wider text-muted">
              {environment.tier || "environment"}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function buildContextLabel(
  projectId?: string,
  serviceId?: string,
  environmentId?: string,
): string | null {
  const env = environmentId ? ` · ${environmentId}` : "";
  if (serviceId) return `service ${serviceId}${env}`;
  if (projectId) return `project ${projectId}${env}`;
  return null;
}

const rootRoute = new RootRoute({
  component: RouterRoot,
  notFoundComponent: NotFoundState,
});

// ── Routes ────────────────────────────────────────────────────────

const loginRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "login",
  validateSearch: (search: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof search.redirect === "string" ? search.redirect : undefined,
  }),
  component: Login,
});

const setupRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "setup",
  component: Setup,
});

const indexRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "/",
  component: ProjectsIndex,
});

const newProjectRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "new",
  component: NewProject,
});

const settingsRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "settings",
  component: InstanceSettings,
});

const usageRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "usage",
  component: UsageLimits,
});

const docsHomeRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "docs",
  component: () => (
    <Documentation section="home">
      <DocsHome />
    </Documentation>
  ),
});

const agentSetupDocsRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "docs/agent-setup",
  component: () => (
    <Documentation section="agent-setup">
      <AgentSetupDocs />
    </Documentation>
  ),
});

const apiReferenceDocsRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "docs/api",
  component: () => (
    <Documentation section="api">
      <APIReferenceDocs />
    </Documentation>
  ),
});

const cliReferenceDocsRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "docs/cli",
  component: () => (
    <Documentation section="cli">
      <CLIReferenceDocs />
    </Documentation>
  ),
});

type CanvasSearch = EnvSearch & {
  service?: string;
  tab?: string;
  view?: "grid" | "map";
  deployment?: string;
  configure?: string;
  resource?: "github" | "image" | "empty" | "database" | "template" | "volume";
};

const RESOURCE_KINDS = new Set([
  "github",
  "image",
  "empty",
  "database",
  "template",
  "volume",
]);

const projectRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "projects/$projectId",
  validateSearch: (search: Record<string, unknown>): CanvasSearch => ({
    ...parseEnvSearch(search),
    service: typeof search.service === "string" ? search.service : undefined,
    tab: typeof search.tab === "string" ? search.tab : undefined,
    view: search.view === "map" ? "map" : undefined,
    deployment:
      typeof search.deployment === "string" ? search.deployment : undefined,
    configure:
      typeof search.configure === "string" ? search.configure : undefined,
    resource:
      typeof search.resource === "string" && RESOURCE_KINDS.has(search.resource)
        ? (search.resource as CanvasSearch["resource"])
        : undefined,
  }),
  component: ProjectRouteComponent,
});

function ProjectRouteComponent() {
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const { projectId } = projectRoute.useParams();
  const { env, service, tab, view, deployment, configure, resource } =
    projectRoute.useSearch();
  const nested = pathname.match(
    /^\/projects\/[^/]+\/services\/([^/]+)\/([^/]+)\/?$/,
  );
  const nestedServiceId = nested ? decodeURIComponent(nested[1]) : undefined;
  const nestedSection = nested ? decodeURIComponent(nested[2]) : undefined;
  const legacySection = legacyServiceSection(tab);
  const selectedServiceId = nestedServiceId || service;
  const selectedSection = nestedSection || legacySection.section;
  const selectedConfigure = resolveRequestedConfigureSection(
    configure,
    legacySection.configure,
  );

  useEffect(() => {
    if (!service || nestedServiceId || tab === "add-service") return;
    void navigate({
      to: "/projects/$projectId/services/$serviceId/$section",
      params: {
        projectId,
        serviceId: service,
        section: legacySection.section,
      },
      search: {
        env,
        view,
        deployment,
        configure: selectedConfigure,
      },
      replace: true,
    } as never);
  }, [
    deployment,
    env,
    legacySection.section,
    navigate,
    nestedServiceId,
    projectId,
    selectedConfigure,
    service,
    tab,
    view,
  ]);

  useEffect(() => {
    if (!nestedServiceId || !nestedSection) return;
    const normalized = normalizeServiceSection(nestedSection);
    const normalizedConfigure = normalizeConfigureSection(configure);
    const configureIsCanonical =
      !configure || configure === normalizedConfigure;
    if (normalized === nestedSection && configureIsCanonical) return;
    void navigate({
      to: "/projects/$projectId/services/$serviceId/$section",
      params: {
        projectId,
        serviceId: nestedServiceId,
        section: normalized,
      },
      search: {
        env,
        view,
        configure:
          normalized === "configure" ? normalizedConfigure : undefined,
      },
      replace: true,
    } as never);
  }, [
    configure,
    env,
    navigate,
    nestedSection,
    nestedServiceId,
    projectId,
    view,
  ]);

  return (
    <>
      <ProjectCanvas
        projectId={projectId}
        environmentId={env}
        serviceId={selectedServiceId}
        tab={tab}
        view={view}
        section={selectedSection}
        deploymentId={deployment}
        configureSection={selectedConfigure}
        resourceKind={resource}
      />
      <Outlet />
    </>
  );
}

function legacyServiceSection(tab?: string): {
  section: "overview" | "deployments" | "logs" | "metrics" | "configure";
  configure?: "variables" | "networking" | "runtime";
} {
  if (tab === "deployments" || tab === "logs" || tab === "metrics") {
    return { section: tab };
  }
  if (tab === "variables") {
    return { section: "configure", configure: "variables" };
  }
  if (tab === "domains") {
    return { section: "configure", configure: "networking" };
  }
  if (tab === "settings") {
    return { section: "configure", configure: "runtime" };
  }
  return { section: "overview" };
}

/**
 * Preserve an absent Configure selection so the inspector can choose Source,
 * a remembered section, or Variables. Explicit and legacy deep links remain
 * canonicalized before reaching the inspector.
 */
export function resolveRequestedConfigureSection(
  configure?: string,
  legacyConfigure?: ConfigureSection,
): ConfigureSection | undefined {
  const requested = configure || legacyConfigure;
  return requested === undefined
    ? undefined
    : normalizeConfigureSection(requested);
}

const projectServiceRoute = new Route({
  getParentRoute: () => projectRoute,
  path: "services/$serviceId/$section",
  validateSearch: (search: Record<string, unknown>): CanvasSearch => ({
    ...parseEnvSearch(search),
    view: search.view === "map" ? "map" : undefined,
    deployment:
      typeof search.deployment === "string" ? search.deployment : undefined,
    configure:
      typeof search.configure === "string" ? search.configure : undefined,
  }),
  component: () => null,
});

const projectDeploymentsRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "projects/$projectId/deployments",
  validateSearch: (search: Record<string, unknown>): EnvSearch & { service?: string } => ({
    ...parseEnvSearch(search),
    service: typeof search.service === "string" ? search.service : undefined,
  }),
  component: () => {
    const { projectId } = projectDeploymentsRoute.useParams();
    const { env, service } = projectDeploymentsRoute.useSearch();
    return <ProjectDeployments projectId={projectId} environmentId={env} serviceId={service} />;
  },
});

const projectObservabilityRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "projects/$projectId/observability",
  validateSearch: (search: Record<string, unknown>): EnvSearch & { service?: string } => ({
    ...parseEnvSearch(search),
    service: typeof search.service === "string" ? search.service : undefined,
  }),
  component: () => {
    const { projectId } = projectObservabilityRoute.useParams();
    const { env, service } = projectObservabilityRoute.useSearch();
    return <ProjectObservability projectId={projectId} environmentId={env} serviceId={service} />;
  },
});

const projectSettingsRoute = new Route({
  getParentRoute: () => rootRoute,
  path: "projects/$projectId/settings",
  validateSearch: (search: Record<string, unknown>): EnvSearch => parseEnvSearch(search),
  component: () => {
    const { projectId } = projectSettingsRoute.useParams();
    const { env } = projectSettingsRoute.useSearch();
    return <ProjectSettings projectId={projectId} environmentId={env} />;
  },
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  setupRoute,
  indexRoute,
  newProjectRoute,
  settingsRoute,
  usageRoute,
  docsHomeRoute,
  agentSetupDocsRoute,
  apiReferenceDocsRoute,
  cliReferenceDocsRoute,
  projectRoute.addChildren([projectServiceRoute]),
  projectDeploymentsRoute,
  projectObservabilityRoute,
  projectSettingsRoute,
]);

export const router = new Router({
  routeTree,
  defaultPreload: "intent",
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
