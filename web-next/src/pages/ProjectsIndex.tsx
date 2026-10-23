// Operational portfolio for every project visible to the current product
// principal. Inventory and deployment signals are composed only from the
// tenant-safe project/environment/service/deployment APIs.

import { useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useEnvironments, useProjects, useServices } from "@/api/hooks";
import type { DeploymentResource, Project } from "@/api/client";
import { usePortfolioDeploymentEvidence } from "./projects/portfolio-api";
import { actionLabel } from "@/lib/productActions";

const CREATE_CMD = "midnight project create";
type PortfolioView = "grid" | "list";
type PortfolioSort = "name" | "recent";

export function ProjectsIndex() {
  const { data, error, isPending, refetch } = useProjects();
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<PortfolioSort>("recent");
  const [view, setView] = useState<PortfolioView>("grid");
  const [expandedProjectId, setExpandedProjectId] = useState<string>();
  const [githubConnected] = useState(
    () => new URLSearchParams(window.location.search).get("github_setup") === "connected",
  );
  const projects = useMemo(
    () => filterAndSortProjects(data ?? [], query, sort),
    [data, query, sort],
  );

  useEffect(() => {
    if (!githubConnected) return;
    const current = new URL(window.location.href);
    current.searchParams.delete("github_setup");
    window.history.replaceState(window.history.state, "", current.pathname + current.search + current.hash);
  }, [githubConnected]);

  return (
    <section className="mx-auto flex w-full max-w-[1180px] flex-col gap-5 p-[var(--main-gutter)] lg:py-10">
      <header className="flex flex-wrap items-center justify-between gap-5">
        <div className="flex min-w-0 items-center gap-4">
          <div className="project-constellation-mark" aria-hidden="true">
            <span />
            <span />
            <span />
          </div>
          <div>
            <p className="m-0 font-mono text-[10px] uppercase tracking-[0.18em] text-accent">
              Control plane
            </p>
            <h1 className="m-0 mt-1 text-[28px] font-semibold tracking-[-0.035em]">
              Projects
            </h1>
            <p className="m-0 mt-1 text-sm text-muted">
              Deploy, observe, and operate every environment from one workspace.
            </p>
          </div>
        </div>
        <NewProjectLink />
      </header>

      {githubConnected ? (
        <div
          role="status"
          className="rounded-lg border border-accent/40 bg-accent-soft px-4 py-3 text-sm text-accent"
        >
          GitHub App connected. Server-verified repositories are now available
          in project and service source pickers.
        </div>
      ) : null}

      {isPending ? <PortfolioLoading /> : null}
      {error ? (
        <div role="alert" className="grid gap-2 rounded-lg border border-danger/40 bg-danger-soft p-4 text-sm">
          <strong className="text-danger">Failed to load projects</strong>
          <span className="text-muted">{error.message}</span>
          <button
            type="button"
            onClick={() => void refetch()}
            className="w-fit rounded border border-line-strong bg-surface-2 px-3 py-1.5 text-xs font-semibold text-text"
          >
            Retry
          </button>
        </div>
      ) : null}

      {data && data.length === 0 ? <EmptyProjects /> : null}

      {data && data.length > 0 ? (
        <>
          <div className="flex flex-wrap items-end gap-3 rounded-xl border border-line bg-surface/80 p-3 shadow-sm backdrop-blur">
            <div className="hidden min-w-[110px] border-r border-line pr-4 sm:grid">
              <strong className="text-sm text-text">{data.length} projects</strong>
              <span className="text-[11px] text-subtle">Visible to this account</span>
            </div>
            <label className="grid min-w-[min(100%,18rem)] flex-1 gap-1 text-xs text-muted">
              Search projects
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Name, slug, or project ID"
                className="field-input"
              />
            </label>
            <label className="grid gap-1 text-xs text-muted">
              Sort
              <select
                value={sort}
                onChange={(event) => setSort(event.target.value as PortfolioSort)}
                className="field-input min-w-40"
              >
                <option value="recent">Recently updated</option>
                <option value="name">Name</option>
              </select>
            </label>
            <div className="inline-flex rounded border border-line bg-surface-2 p-0.5" role="group" aria-label="Portfolio view">
              <ViewButton active={view === "grid"} onClick={() => setView("grid")}>Grid</ViewButton>
              <ViewButton active={view === "list"} onClick={() => setView("list")}>List</ViewButton>
            </div>
          </div>

          {projects.length === 0 ? (
            <div role="status" className="rounded-lg border border-dashed border-line bg-bg-2 p-8 text-center">
              <strong className="text-text">No projects match “{query.trim()}”.</strong>
              <p className="m-0 mt-2 text-sm text-muted">Clear the search to restore the complete tenant-visible portfolio.</p>
              <button type="button" onClick={() => setQuery("")} className="mt-3 rounded border border-line-strong bg-surface px-3 py-1.5 text-xs font-semibold text-text">
                Clear search
              </button>
            </div>
          ) : (
            <ul
              className={
                "m-0 grid list-none gap-4 p-0 " +
                (view === "grid"
                  ? "[grid-template-columns:repeat(auto-fill,minmax(min(100%,320px),1fr))]"
                  : "grid-cols-1")
              }
              data-view={view}
            >
              {projects.map((project) => (
                <ProjectPortfolioItem
                  key={project.id}
                  project={project}
                  view={view}
                  expanded={expandedProjectId === project.id}
                  onToggleEvidence={() =>
                    setExpandedProjectId((current) =>
                      current === project.id ? undefined : project.id,
                    )
                  }
                />
              ))}
            </ul>
          )}
        </>
      ) : null}
    </section>
  );
}

function ProjectPortfolioItem({
  project,
  view,
  expanded,
  onToggleEvidence,
}: {
  project: Project;
  view: PortfolioView;
  expanded: boolean;
  onToggleEvidence: () => void;
}) {
  const name = project.display_name || project.slug;
  return (
    <li className="project-portfolio-card overflow-hidden rounded-xl border border-line bg-surface transition-all hover:-translate-y-0.5 hover:border-line-strong hover:shadow-lg">
      <article className={view === "list" ? "grid min-h-[170px] md:grid-cols-[minmax(220px,0.72fr)_minmax(300px,1.28fr)]" : "grid h-full min-h-[268px] grid-rows-[auto_1fr_auto]"}>
        <header className="flex min-w-0 items-start justify-between gap-3 p-4">
          <div className="min-w-0">
          <Link
            to="/projects/$projectId"
            params={{ projectId: project.id }}
            search={{}}
            className="group inline-flex max-w-full items-center gap-2 text-[15px] font-semibold tracking-tight text-text"
          >
            <span className="truncate">{name}</span>
            <span aria-hidden className="text-muted transition-transform group-hover:translate-x-0.5">→</span>
          </Link>
          <p className="m-0 mt-1 truncate font-mono text-[10px] text-subtle" title={project.id}>
            {project.slug}
          </p>
        </div>
          <span className="shrink-0 rounded-full border border-line-soft bg-bg-2 px-2 py-1 text-[10px] text-subtle">
            {formatTimestamp(project.updated_at)}
          </span>
        </header>

        <div className="project-portfolio-preview relative min-h-[148px] border-y border-line-soft">
          {expanded ? (
            <ProjectPortfolioEvidence projectId={project.id} />
          ) : (
            <button
              type="button"
              aria-expanded={false}
              aria-label={`Show ${name} status`}
              onClick={onToggleEvidence}
              className="group absolute inset-0 grid place-items-center text-center"
            >
              <span>
                <span className="mx-auto grid h-11 w-11 place-items-center rounded-xl border border-line-strong bg-surface-2 font-mono text-lg font-semibold text-accent shadow-sm transition group-hover:border-accent group-hover:bg-accent-soft">
                  {projectGlyph(name)}
                </span>
                <span className="mt-2 block text-xs font-medium text-muted group-hover:text-text">
                  Load operational preview
                </span>
                <span className="mt-0.5 block text-[10px] text-subtle">
                  Details load on demand
                </span>
              </span>
            </button>
          )}
        </div>

        <footer className="flex min-w-0 items-center justify-between gap-3 px-4 py-3 text-[11px]">
          <span className="inline-flex min-w-0 items-center gap-2 text-muted">
            <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${expanded ? "bg-accent" : "bg-subtle"}`} />
            <span className="truncate">{expanded ? "Details loaded" : "Details available"}</span>
          </span>
          <button
            type="button"
            aria-expanded={expanded}
            onClick={onToggleEvidence}
            className="shrink-0 rounded px-2 py-1 font-semibold text-muted hover:bg-surface-2 hover:text-text"
          >
            {expanded ? "Hide" : "Preview"}
          </button>
        </footer>
      </article>
    </li>
  );
}

function ProjectPortfolioEvidence({ projectId }: { projectId: string }) {
  const environments = useEnvironments(projectId);
  const services = useServices(projectId);
  const deployments = usePortfolioDeploymentEvidence(services.data ?? []);
  const inventoryPending = environments.isPending || services.isPending;
  const inventoryError = environments.error ?? services.error;
  const signal = summarizeProjectDeployments(
    (services.data ?? [])
      .slice(0, deployments.sampledServiceCount)
      .map((service) => service.id),
    deployments.deployments,
  );

  if (inventoryPending) {
    return <span role="status" className="text-xs text-muted">Loading inventory…</span>;
  }
  if (inventoryError) {
    return (
      <button
        type="button"
        onClick={() => {
          void environments.refetch();
          void services.refetch();
        }}
        className="w-fit rounded border border-danger/40 bg-danger-soft px-2.5 py-1.5 text-left text-xs text-danger"
      >
        Inventory unavailable · retry
      </button>
    );
  }
  return (
    <div className="grid h-full min-h-[148px] grid-rows-[1fr_auto]">
      <div className="flex flex-wrap items-center justify-center gap-2 p-4">
        {(services.data ?? []).slice(0, 6).map((service) => (
          <span
            key={service.id}
            className="project-service-glyph"
            title={service.display_name || service.slug}
          >
            <span aria-hidden="true">›_</span>
            <span className="max-w-20 truncate">{service.display_name || service.slug}</span>
          </span>
        ))}
        {(services.data?.length ?? 0) > 6 ? (
          <span className="project-service-glyph text-subtle">+{(services.data?.length ?? 0) - 6}</span>
        ) : null}
        {(services.data?.length ?? 0) === 0 ? (
          <span className="text-xs text-subtle">No services in this project</span>
        ) : null}
      </div>
      <div className="grid gap-2 border-t border-line-soft bg-bg-2/75 px-3 py-2.5 backdrop-blur">
        <dl className="m-0 flex flex-wrap items-center gap-2 text-xs">
          <PortfolioStat label="Environments" value={environments.data?.length ?? 0} />
          <PortfolioStat label="Services" value={services.data?.length ?? 0} />
        </dl>
        <div className="grid gap-1">
        <span className="font-mono text-[10px] uppercase tracking-wider text-subtle">Deployment signals</span>
        {deployments.isPending ? (
          <span role="status" className="text-xs text-muted">Checking bounded latest deployments…</span>
        ) : deployments.error ? (
          <button type="button" onClick={deployments.refetchAll} className="w-fit rounded border border-danger/40 bg-danger-soft px-2.5 py-1.5 text-xs text-danger">
            Status unavailable · retry
          </button>
        ) : (
          <>
            <DeploymentSignalSummary signal={signal} />
            {deployments.truncated ? (
              <span className="text-[11px] text-warn">
                Latest deployment sampled for {deployments.sampledServiceCount} of {deployments.totalServiceCount} services. Open the project for complete history.
              </span>
            ) : null}
          </>
        )}
        </div>
      </div>
    </div>
  );
}

export type ProjectDeploymentSignal = {
  successful: number;
  inFlight: number;
  failed: number;
  other: number;
  notReported: number;
};

export function summarizeProjectDeployments(
  serviceIds: string[],
  deployments: DeploymentResource[],
): ProjectDeploymentSignal {
  const latestByService = new Map<string, DeploymentResource>();
  for (const deployment of deployments) {
    if (!latestByService.has(deployment.service_id)) {
      latestByService.set(deployment.service_id, deployment);
    }
  }
  const signal: ProjectDeploymentSignal = {
    successful: 0,
    inFlight: 0,
    failed: 0,
    other: 0,
    notReported: 0,
  };
  for (const serviceId of serviceIds) {
    const deployment = latestByService.get(serviceId);
    if (!deployment) {
      signal.notReported++;
      continue;
    }
    const state = `${deployment.operation_state || ""} ${deployment.status || ""}`.toLowerCase();
    if (/failed|failure/.test(state)) signal.failed++;
    else if (/queued|running|building|deploying|pending/.test(state)) signal.inFlight++;
    else if (deployment.active || /succeeded|success|active/.test(state)) signal.successful++;
    else signal.other++;
  }
  return signal;
}

export function filterAndSortProjects(
  projects: Project[],
  query: string,
  sort: PortfolioSort,
): Project[] {
  const needle = query.trim().toLocaleLowerCase();
  return projects
    .filter((project) =>
      !needle || [project.display_name, project.slug, project.id].some((value) => value.toLocaleLowerCase().includes(needle)),
    )
    .slice()
    .sort((left, right) => {
      if (sort === "name") {
        return (left.display_name || left.slug).localeCompare(right.display_name || right.slug);
      }
      const time = Date.parse(right.updated_at) - Date.parse(left.updated_at);
      return Number.isFinite(time) && time !== 0 ? time : left.id.localeCompare(right.id);
    });
}

function DeploymentSignalSummary({ signal }: { signal: ProjectDeploymentSignal }) {
  const values = [
    signal.failed ? `${signal.failed} failed` : "",
    signal.inFlight ? `${signal.inFlight} in flight` : "",
    signal.successful ? `${signal.successful} active/succeeded` : "",
    signal.other ? `${signal.other} other reported` : "",
    signal.notReported ? `${signal.notReported} no deployment reported` : "",
  ].filter(Boolean);
  return (
    <span className={"text-xs " + (signal.failed ? "text-danger" : signal.inFlight ? "text-info" : "text-muted")}>
      {values.length ? values.join(" · ") : "No services reported"}
    </span>
  );
}

function PortfolioStat({ label, value }: { label: string; value: number }) {
  return (
    <div className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-2 py-1">
      <dt className="text-[10px] text-subtle">{label.toLowerCase()}</dt>
      <dd className="m-0 font-mono text-[11px] font-semibold text-text">{value}</dd>
    </div>
  );
}

function projectGlyph(value: string): string {
  return value.trim().slice(0, 2).toUpperCase() || "M";
}

function PortfolioLoading() {
  return (
    <div role="status" aria-live="polite" className="grid gap-3">
      <span className="text-sm text-muted">Loading project portfolio…</span>
      <div className="grid gap-3 sm:grid-cols-2">
        {[0, 1].map((item) => <span key={item} aria-hidden className="h-36 animate-pulse rounded-lg border border-line bg-surface" />)}
      </div>
    </div>
  );
}

function ViewButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: string }) {
  return <button type="button" aria-pressed={active} onClick={onClick} className={"rounded px-2.5 py-1.5 text-xs font-semibold " + (active ? "bg-surface-3 text-text" : "text-muted hover:text-text")}>{children}</button>;
}

function formatTimestamp(value: string): string {
  const time = Date.parse(value);
  return Number.isFinite(time)
    ? new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(time)
    : "time not reported";
}

function NewProjectLink() {
  return (
    <Link to="/new" data-action-id="project.create" className="inline-flex h-9 items-center gap-2 rounded-[var(--r-sm)] border border-accent bg-[var(--accent)] px-3 text-sm font-semibold text-accent-ink shadow-sm hover:brightness-105">
      <span aria-hidden="true">+</span>
      {actionLabel("project.create")}
    </Link>
  );
}

function EmptyProjects() {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(CREATE_CMD);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className="grid gap-3 rounded border border-dashed border-line bg-bg-2 p-8 text-center">
      <strong className="text-text">No projects yet.</strong>
      <span className="text-sm text-muted">Create a project to start an environment and service portfolio.</span>
      <div><NewProjectLink /></div>
      <span className="text-muted">CLI fallback:</span>
      <div className="mx-auto inline-flex items-center gap-2 rounded border border-line bg-surface px-3 py-2">
        <code className="font-mono text-sm text-text" data-testid="cli-snippet">{CREATE_CMD}</code>
        <button type="button" onClick={onCopy} className="rounded border border-line bg-surface-2 px-2 py-0.5 text-[11px] font-semibold text-muted" aria-label="Copy command to clipboard">{copied ? "Copied" : "Copy"}</button>
      </div>
    </div>
  );
}
