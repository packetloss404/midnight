// Environment-scope search-param helpers. The `?env=<environmentId>` URL
// search param is the single, shareable source of truth for which
// environment the operator is viewing across every project-scoped route.
//
// Keeping the parse/serialise here (rather than inline in each route) means
// the query-key threading in `hooks.ts` and the switcher in the top bar stay
// consistent, and a shared URL always reproduces the same env selection.

export type EnvSearch = {
  /**
   * A concrete environment id, the explicit read-only compare scope, or
   * undefined while a project route resolves its default environment.
   */
  env?: string;
};

export const COMPARE_ENVIRONMENTS_SCOPE = "all";

type EnvironmentChoice = {
  id: string;
  slug?: string;
  name?: string;
  tier?: string;
};

/** Validate raw router search into a typed `EnvSearch` (used by routes). */
export function parseEnvSearch(search: Record<string, unknown>): EnvSearch {
  return typeof search.env === "string" && search.env
    ? { env: search.env }
    : {};
}

/** Whether the URL intentionally requests the read-only aggregate view. */
export function isCompareEnvironmentScope(scope: string | undefined): boolean {
  return scope === COMPARE_ENVIRONMENTS_SCOPE;
}

/**
 * Return an environment id only when the URL is concretely scoped.
 * Mutating UI must use this value, never the raw compare sentinel.
 */
export function concreteEnvironmentId(scope: string | undefined): string | undefined {
  return scope && !isCompareEnvironmentScope(scope) ? scope : undefined;
}

/**
 * Pick the environment operators most likely mean when opening a project.
 * Production wins, then an explicitly named default, then the first stable
 * API result. The returned value is always a concrete environment id.
 */
export function selectDefaultEnvironmentId(
  environments: EnvironmentChoice[] | undefined,
): string | undefined {
  if (!environments?.length) return undefined;

  const normalized = environments.map((environment) => ({
    environment,
    slug: environment.slug?.trim().toLowerCase(),
    name: environment.name?.trim().toLowerCase(),
    tier: environment.tier?.trim().toLowerCase(),
  }));
  const preferred =
    normalized.find(({ tier }) => tier === "prod" || tier === "production") ??
    normalized.find(({ slug, name }) => slug === "production" || name === "production") ??
    normalized.find(({ slug, name }) => slug === "default" || name === "default") ??
    normalized[0];

  return preferred.environment.id;
}
