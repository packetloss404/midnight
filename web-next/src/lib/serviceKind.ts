// Service "kind" derivation + canvas edge derivation.
//
// Previously the node kind was guessed from the service slug alone. That is
// brittle (a service named "orders-api" that runs Postgres would show as an
// API). Here we derive kind from real BACKEND METADATA first — the running
// image and any declared source type on the service/deployment — and only
// fall back to slug heuristics when no structural signal exists.
//
// This module is intentionally framework-free (pure functions) so both the
// canvas (edges) and the individual nodes (badge) route through one mapper
// and never disagree about what a service is.

import type { DeploymentResource, Service } from "@/api/client";

export type NodeKind = "web" | "api" | "worker" | "cron" | "db" | "cache";

export const KIND_ACCENT: Record<NodeKind, string> = {
  web: "var(--info)",
  api: "var(--accent)",
  worker: "oklch(0.72 0.18 200)",
  cron: "var(--warn)",
  db: "var(--branch)",
  cache: "var(--danger)",
};

export const KIND_LABEL: Record<NodeKind, string> = {
  web: "Web",
  api: "API",
  worker: "Worker",
  cron: "Cron",
  db: "Postgres",
  cache: "Redis",
};

export function isDatastore(kind: NodeKind): boolean {
  return kind === "db" || kind === "cache";
}

// The REST `Service`/`DeploymentResource` types are intentionally narrow, but
// the backend may enrich them (source type, declared kind) ahead of the
// generated types. Read those optional signals defensively rather than
// widening the shared type.
type MetaBag = {
  source_type?: string;
  sourceType?: string;
  kind?: string;
  type?: string;
  image?: string;
};

function meta(o: unknown): MetaBag {
  return (o ?? {}) as MetaBag;
}

const IMAGE_DB = /(postgres|pgvector|timescale|mysql|mariadb|mongo|cockroach|clickhouse)/;
const IMAGE_CACHE = /(redis|valkey|keydb|memcached)/;

/** Map a free-form source/image/type token to a kind, or null if unknown. */
function kindFromToken(token: string | undefined): NodeKind | null {
  if (!token) return null;
  const t = token.toLowerCase();
  if (IMAGE_DB.test(t)) return "db";
  if (IMAGE_CACHE.test(t)) return "cache";
  if (/(worker|queue|consumer|job)/.test(t)) return "worker";
  if (/(cron|schedul)/.test(t)) return "cron";
  if (/(web|frontend|static|nginx|caddy|ui)/.test(t)) return "web";
  if (/(api|service|server|backend)/.test(t)) return "api";
  return null;
}

/** Last-resort slug heuristic (kept for services with no structural signal). */
function kindFromSlug(slug: string): NodeKind {
  const s = slug.toLowerCase();
  if (s.includes("worker") || s.includes("queue")) return "worker";
  if (s.includes("cron") || s.includes("scheduler")) return "cron";
  if (s.includes("db") || s.includes("postgres") || s.includes("sql")) return "db";
  if (s.includes("cache") || s.includes("redis")) return "cache";
  if (s.includes("web") || s.includes("frontend") || s.includes("ui")) return "web";
  return "api";
}

/**
 * Resolve a service's kind. Precedence (most reliable → least):
 *   1. Declared source type / kind on the service or its deployment.
 *   2. The running deployment image (postgres/redis/… → db/cache).
 *   3. A ready, reachable promoted route → web (it serves traffic).
 *   4. Slug heuristics.
 */
export function serviceKind(
  service: Service,
  deployment?: DeploymentResource | null,
): NodeKind {
  const sm = meta(service);
  const dm = meta(deployment);

  // 1. Declared type on either the service or the active deployment.
  const declared =
    kindFromToken(sm.source_type ?? sm.sourceType ?? sm.kind ?? sm.type) ??
    kindFromToken(dm.source_type ?? dm.sourceType ?? dm.kind ?? dm.type);
  if (declared) return declared;

  // 2. Running image.
  const fromImage = kindFromToken(deployment?.image ?? dm.image);
  if (fromImage) return fromImage;

  // 3. Serves HTTP traffic → treat as web (unless slug says otherwise below).
  if (
    deployment?.route &&
    deployment.route_apply_state === "ready" &&
    deployment.route_reachable === true
  ) {
    const slugKind = kindFromSlug(service.slug ?? "");
    // A datastore/worker never becomes "web" just because a route exists.
    if (slugKind === "api" || slugKind === "web") return "web";
  }

  // 4. Slug fallback.
  return kindFromSlug(service.slug ?? "");
}

// ── Edge derivation ───────────────────────────────────────────────

export type ServiceEdge = {
  from: string;
  to: string;
  /** "ref" = explicit declared reference; "attach" = datastore attachment. */
  relation: "ref" | "attach";
};

/** Read explicit references declared on a service, if the backend sends them. */
function explicitRefs(service: Service): string[] {
  const bag = service as unknown as {
    depends_on?: unknown;
    dependsOn?: unknown;
    references?: unknown;
    connected_service_ids?: unknown;
  };
  const spec = (service.spec ?? {}) as {
    depends_on?: unknown;
    dependsOn?: unknown;
    references?: unknown;
    connected_service_ids?: unknown;
  };
  const raw =
    bag.depends_on ??
    bag.dependsOn ??
    bag.references ??
    bag.connected_service_ids ??
    spec.depends_on ??
    spec.dependsOn ??
    spec.references ??
    spec.connected_service_ids;
  if (!Array.isArray(raw)) return [];
  return raw.filter((v): v is string => typeof v === "string");
}

/** Read only attachment IDs emitted by the backend relationship inventory. */
function explicitAttachments(service: Service): string[] {
  const bag = service as unknown as {
    attached_service_ids?: unknown;
    attachment_service_ids?: unknown;
  };
  const spec = (service.spec ?? {}) as {
    attached_service_ids?: unknown;
    attachment_service_ids?: unknown;
  };
  const raw =
    bag.attached_service_ids ??
    bag.attachment_service_ids ??
    spec.attached_service_ids ??
    spec.attachment_service_ids;
  if (!Array.isArray(raw)) return [];
  return raw.filter((value): value is string => typeof value === "string");
}

/**
 * Derive canvas edges from real relationships:
 *   - If a service declares explicit references, draw those.
 *   - Explicit attachment IDs draw dashed attachment edges.
 *   - If neither verified signal exists, NO edge is drawn. Resource kind or
 *     proximity is never treated as proof of a relationship.
 */
export function deriveEdges(
  services: Service[],
  _kindOf: (serviceId: string) => NodeKind,
): ServiceEdge[] {
  const byId = new Map(services.map((s) => [s.id, s]));
  const bySlug = new Map(services.map((s) => [s.slug, s]));

  const edges: ServiceEdge[] = [];
  const seen = new Set<string>();
  const push = (from: string, to: string, relation: ServiceEdge["relation"]) => {
    if (from === to) return;
    const key = from < to ? `${from}|${to}` : `${to}|${from}`;
    if (seen.has(key)) return;
    seen.add(key);
    edges.push({ from, to, relation });
  };

  for (const s of services) {
    const refs = explicitRefs(s);
    for (const reference of refs) {
      const target = byId.get(reference) ?? bySlug.get(reference);
      if (target) push(s.id, target.id, "ref");
    }
    for (const attachment of explicitAttachments(s)) {
      const target = byId.get(attachment) ?? bySlug.get(attachment);
      if (target) push(s.id, target.id, "attach");
    }
  }

  return edges;
}
