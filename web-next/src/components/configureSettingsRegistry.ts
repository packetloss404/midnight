import type { ConfigureSection } from "./ServiceDetailDrawer";

export type ConfigureSetting = {
  id: string;
  label: string;
  description: string;
  section: ConfigureSection;
  aliases: readonly string[];
  /** Visible labels or accessible names for controls that represent this setting. */
  controlLabels?: readonly string[];
};

/**
 * Configure's searchable vocabulary. It describes controls already owned by
 * the section panels; it does not grant capabilities or duplicate settings.
 */
export const CONFIGURE_SETTINGS = [
  setting("source.repository", "GitHub repository", "Connect or change the service source.", "source", ["source", "repo", "git", "connect"], ["Connect source", "Change source", "Search granted repositories"]),
  setting("source.branch", "Source branch", "Choose the branch that triggers deployments.", "source", ["git", "ref", "deploy branch"], ["Branch"]),
  setting("variables.values", "Variables and secrets", "Add, replace, or remove service environment values.", "variables", ["env", "environment", "secret", "config"], ["Variable name", "New variable name"]),
  setting("networking.domain", "Public domain", "Generate or manage the service's public HTTP address.", "networking", ["url", "hostname", "http", "https", "expose"], ["Stage domain"]),
  setting("networking.tcp", "TCP proxy", "Publish a service port over TCP.", "networking", ["port", "proxy", "network", "l4"], ["TCP backend port"]),
  setting("storage.volume", "Persistent volume", "Create and manage storage that survives redeploys.", "storage", ["disk", "data", "persistent"], ["Name", "Stage volume intent"]),
  setting("storage.mount", "Mount path", "Choose where a persistent volume is mounted.", "storage", ["directory", "filesystem", "path"], ["Mount path"]),
  setting("storage.backup", "Backups and restore", "Schedule backups or restore a volume.", "storage", ["recovery", "snapshot", "schedule"]),
  setting("runtime.builder", "Builder", "Choose how source is turned into an image.", "runtime", ["buildpack", "dockerfile", "nixpacks"], ["Builder"]),
  setting("runtime.root", "Root directory", "Set the source directory used for builds.", "runtime", ["root", "working directory", "monorepo"], ["Root directory"]),
  setting("runtime.dockerfile", "Dockerfile path", "Point builds to a Dockerfile in the repository.", "runtime", ["docker", "container", "build file"], ["Dockerfile path"]),
  setting("runtime.build", "Build command", "Override the command used to build the service.", "runtime", ["compile", "builder"], ["Build command"]),
  setting("runtime.start", "Start command", "Override the command that starts the service.", "runtime", ["run", "entrypoint", "launch"], ["Start command"]),
  setting("runtime.predeploy", "Pre-deploy command", "Run a command before a new release starts.", "runtime", ["migration", "release command"], ["Pre-deploy command"]),
  setting("runtime.kind", "Runtime kind", "Run as a service, worker, or scheduled job.", "runtime", ["worker", "service", "cron", "job"], ["Runtime kind"]),
  setting("runtime.port", "Target port", "Set the application port Midnight routes to.", "runtime", ["listen", "backend", "network"], ["Target port"]),
  setting("runtime.replicas", "Replicas", "Choose how many service instances should run.", "runtime", ["scale", "instances", "horizontal"], ["Replicas"]),
  setting("runtime.cron", "Cron schedule", "Schedule recurring one-off runs in UTC.", "runtime", ["scheduled", "job", "timer"], ["Schedule (UTC)"]),
  setting("runtime.health", "Health check", "Configure how Midnight decides an instance is ready.", "runtime", ["probe", "readiness", "path", "tcp"], ["Health check"]),
  setting("runtime.restart", "Restart policy", "Control automatic restarts after a process exits.", "runtime", ["retry", "failure", "recovery"], ["Restart policy"]),
] as const satisfies readonly ConfigureSetting[];

export function searchConfigureSettings(query: string): ConfigureSetting[] {
  const terms = query
    .trim()
    .toLocaleLowerCase()
    .split(/\s+/)
    .filter(Boolean);
  if (terms.length === 0) return [];
  return CONFIGURE_SETTINGS.filter((candidate) => {
    const haystack = [
      candidate.label,
      candidate.description,
      candidate.section,
      ...candidate.aliases,
    ]
      .join(" ")
      .toLocaleLowerCase();
    return terms.every((term) => haystack.includes(term));
  });
}

function setting(
  id: string,
  label: string,
  description: string,
  section: ConfigureSection,
  aliases: readonly string[],
  controlLabels?: readonly string[],
): ConfigureSetting {
  return { id, label, description, section, aliases, controlLabels };
}
