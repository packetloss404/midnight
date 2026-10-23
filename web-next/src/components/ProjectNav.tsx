import { Link } from "@tanstack/react-router";

type Props = {
  projectId: string;
  environmentId?: string;
  observabilityAvailable?: boolean;
};

const ITEMS = [
  { to: "/projects/$projectId", label: "Topology", exact: true },
  { to: "/projects/$projectId/deployments", label: "Deployments", exact: false },
  { to: "/projects/$projectId/observability", label: "Observability", exact: false },
  { to: "/projects/$projectId/settings", label: "Settings", exact: false },
] as const;

/** Persistent, project-scoped navigation for every implemented project view. */
export function ProjectNav({
  projectId,
  environmentId,
  observabilityAvailable = true,
}: Props) {
  const items = observabilityAvailable
    ? ITEMS
    : ITEMS.filter((item) => item.to !== "/projects/$projectId/observability");
  return (
    <nav
      aria-label="Project"
      className="sticky top-[var(--top-nav-height)] z-20 flex h-[var(--project-nav-height)] shrink-0 items-center gap-1 overflow-x-auto border-b border-line bg-bg px-2 sm:px-4"
    >
      {items.map((item) => (
        <Link
          key={item.to}
          to={item.to}
          params={{ projectId }}
          search={environmentId ? { env: environmentId } : {}}
          activeOptions={item.exact ? { exact: true } : undefined}
          className="whitespace-nowrap rounded-[var(--r-sm)] px-3 py-1.5 text-xs font-medium text-muted transition-colors hover:bg-surface-2 hover:text-text [&.active]:bg-accent-soft [&.active]:text-accent"
        >
          {item.label}
        </Link>
      ))}
    </nav>
  );
}
