// GithubConnect — link a service to a GitHub repo for push-to-deploy.
//
// PROP CONTRACT (stable):
//   serviceId   required
//   onLinked?   () => void
//
// Read path: `useGithubInstallations` / `useGithubRepos` (shared hooks) via
// the reusable RepoPicker. Link mutation: `PUT /services/{id}/github`
// (see ./github/api.ts — mirrors GraphQL `serviceGithubLink`). The
// "no GitHub App configured" (501) case renders a calm setup affordance
// instead of an error.

import { useState } from "react";
import type { GithubRepo } from "@/api/client";
import { Button, InlineError } from "@/components/panels/form-kit";
import { RepoPicker, type RepoSelection } from "./github/RepoPicker";
import { linkServiceGithub } from "./github/api";

export type GithubConnectProps = {
  serviceId: string;
  onLinked?: () => void;
};

export function GithubConnect({ serviceId, onLinked }: GithubConnectProps) {
  const [linking, setLinking] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [linked, setLinked] = useState<{ repo: string; branch: string } | null>(null);

  async function handleSelect({ installationId, repo }: RepoSelection) {
    setError(null);
    setLinking(repo.full_name);
    try {
      const branch = repo.default_branch || "main";
      await linkServiceGithub(serviceId, {
        installationId,
        repo: repo.full_name,
        branch,
        cloneUrl: cloneUrlFor(repo),
      });
      setLinked({ repo: repo.full_name, branch });
      onLinked?.();
    } catch (err) {
      setError(err);
    } finally {
      setLinking(null);
    }
  }

  if (linked) {
    return (
      <div className="grid gap-3">
        <div className="flex items-center justify-between gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3 text-sm">
          <div className="min-w-0">
            <div className="truncate font-mono text-text">{linked.repo}</div>
            <div className="mt-0.5 text-[11px] text-subtle">tracking {linked.branch}</div>
          </div>
          <span
            className="rounded-full border border-accent bg-accent-soft px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-accent"
            role="status"
          >
            Linked
          </span>
        </div>
        <div>
          <Button variant="secondary" onClick={() => setLinked(null)}>
            Link a different repo
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="grid gap-3">
      <RepoPicker onSelect={handleSelect} busyRepo={linking} ctaLabel="Link" />
      {error ? <InlineError error={error} /> : null}
    </div>
  );
}

function cloneUrlFor(repo: GithubRepo): string | undefined {
  return repo.full_name ? `https://github.com/${repo.full_name}.git` : undefined;
}
