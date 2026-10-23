// Pure display helpers for the deployments surface (panel-local — owned by
// the deployments feature, not the shared foundation).
//
// The REST `DeploymentResource` does not carry an explicit `trigger` field
// nor a commit SHA, so `deriveTrigger` / `deploySubject` are best-effort
// projections over the fields we do have (image, route_intent, last_event).
// See the report note: a first-class `trigger` + `commit` on the backend
// resource would let these become exact rather than heuristic.

import type { DeploymentResource } from "@/api/client";

export type TriggerKind = "github" | "cli" | "manual";

/**
 * Best-effort classification of what kicked off a deployment. GitHub is
 * detected from a source-repo signal in the last recorded event or image;
 * a digest-pinned image with no repo signal is treated as a CLI/API push;
 * everything else (e.g. a dashboard redeploy) falls back to manual.
 */
export function deriveTrigger(d: DeploymentResource): TriggerKind {
  const haystack = `${d.last_event ?? ""} ${d.image ?? ""} ${d.route_intent ?? ""}`.toLowerCase();
  if (/github|ghcr\.io|source_repo=\S|git@|\.git\b/.test(haystack)) return "github";
  if (d.image && d.image.trim() !== "") return "cli";
  return "manual";
}

export const TRIGGER_LABEL: Record<TriggerKind, string> = {
  github: "GitHub",
  cli: "CLI / API",
  manual: "Manual",
};

/** Inline trigger-source glyph. Decorative — labelled by its wrapper. */
export function TriggerIcon({ kind, className }: { kind: TriggerKind; className?: string }) {
  const cls = className ?? "h-3.5 w-3.5";
  switch (kind) {
    case "github":
      return (
        <svg viewBox="0 0 16 16" className={cls} fill="currentColor" aria-hidden="true">
          <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
        </svg>
      );
    case "cli":
      return (
        <svg viewBox="0 0 16 16" className={cls} fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="1.5" y="2.5" width="13" height="11" rx="1.5" />
          <path d="M4 6l2.2 2L4 10M8 10.5h4" />
        </svg>
      );
    default:
      return (
        <svg viewBox="0 0 16 16" className={cls} fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M8 1.5 9.9 5.4l4.3.63-3.1 3.02.73 4.28L8 11.34 4.17 13.33l.73-4.28L1.8 6.03l4.3-.63Z" />
        </svg>
      );
  }
}

/** A compact, human subject for a deployment: image ref or short id. */
export function deploySubject(d: DeploymentResource): string {
  const image = d.image?.trim();
  if (image) return shortImage(image);
  return shortId(d.deployment_id || d.id);
}

/** `registry/host/web@sha256:abcd…` -> `web@abcd1234`. */
export function shortImage(image: string): string {
  const [repoPart, digestPart] = image.split("@");
  const repo = repoPart.split("/").pop() || repoPart;
  const name = repo.split(":")[0] || repo;
  if (digestPart) {
    const hex = digestPart.replace(/^sha256:/, "");
    return `${name}@${hex.slice(0, 8)}`;
  }
  return repo;
}

export function shortId(id: string): string {
  if (!id) return "—";
  return id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
}

/** Relative time from an ISO/`time.Time` string, e.g. "5m ago". */
export function formatRelative(iso: string | undefined | null): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  const diff = Date.now() - then;
  if (diff < 0) return "just now";
  const s = Math.floor(diff / 1000);
  if (s < 45) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const days = Math.floor(h / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(iso).toLocaleDateString();
}

/** Duration between two ISO timestamps, e.g. "1m 23s". */
export function formatDuration(startIso?: string | null, endIso?: string | null): string | null {
  if (!startIso || !endIso) return null;
  const start = new Date(startIso).getTime();
  const end = new Date(endIso).getTime();
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return null;
  const totalSec = Math.round((end - start) / 1000);
  if (totalSec < 60) return `${totalSec}s`;
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  if (m < 60) return s ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}
