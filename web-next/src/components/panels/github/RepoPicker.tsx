// RepoPicker — installation selector + repo search. Shared by GithubConnect
// (link an existing service) and the New-Project GitHub source flow (choose a
// repo before the service exists). Read paths use the shared
// `useGithubInstallations` / `useGithubRepos` hooks.
//
// The GitHub App is optional in a self-hosted install; when it is not
// configured the endpoints answer 501 and we render a calm setup affordance
// instead of an error (see `isGithubUnconfigured`).

import { useMemo, useState } from "react";
import type { GithubRepo } from "@/api/client";
import { useGithubInstallations, useGithubRepos } from "@/api/hooks";
import { Loading, Empty, ErrorState, ActionButton } from "@/components/states";
import { TextInput } from "@/components/panels/form-kit";
import { isGithubUnconfigured } from "./api";

export type RepoSelection = { installationId: string; repo: GithubRepo };

export function GithubUnconfigured() {
  return (
    <Empty
      title="GitHub App not configured"
      detail="This Midnight instance has no GitHub App connected, so repositories can't be listed. Connect the App from Instance settings to enable push-to-deploy — you can still deploy from a container image or template in the meantime."
      icon={<span aria-hidden>🔌</span>}
      action={
        <ActionButton
          onClick={() =>
            window.open(
              "https://docs.github.com/apps/creating-github-apps",
              "_blank",
              "noopener,noreferrer",
            )
          }
        >
          How to connect the GitHub App
        </ActionButton>
      }
    />
  );
}

export function RepoPicker({
  onSelect,
  busyRepo = null,
  ctaLabel = "Select",
}: {
  onSelect: (selection: RepoSelection) => void;
  /** full_name of a repo currently being acted on (shows a spinner label). */
  busyRepo?: string | null;
  ctaLabel?: string;
}) {
  const installations = useGithubInstallations();
  const [installationId, setInstallationId] = useState<string | undefined>();
  const [query, setQuery] = useState("");

  const activeInstallation =
    installationId ?? installations.data?.[0]?.id ?? undefined;
  const repos = useGithubRepos(activeInstallation);

  const filtered = useMemo(() => {
    const list = repos.data ?? [];
    const q = query.trim().toLowerCase();
    if (!q) return list;
    return list.filter((r) => r.full_name.toLowerCase().includes(q));
  }, [repos.data, query]);

  if (installations.isPending) return <Loading label="Loading GitHub installations…" />;
  if (installations.error) {
    if (isGithubUnconfigured(installations.error)) return <GithubUnconfigured />;
    return (
      <ErrorState
        title="Failed to load installations"
        error={installations.error}
        onRetry={() => installations.refetch()}
      />
    );
  }
  if (!installations.data || installations.data.length === 0) {
    return (
      <Empty
        title="No GitHub installations"
        detail="Install the Midnight GitHub App on an account or org to list its repositories."
        icon={<span aria-hidden>🐙</span>}
        action={
          <ActionButton
            onClick={() =>
              window.open("https://github.com/apps", "_blank", "noopener,noreferrer")
            }
          >
            Install GitHub App
          </ActionButton>
        }
      />
    );
  }

  return (
    <div className="grid gap-3">
      {installations.data.length > 1 ? (
        <div className="flex flex-wrap gap-1.5">
          {installations.data.map((inst) => {
            const active = inst.id === activeInstallation;
            return (
              <button
                key={inst.id}
                type="button"
                onClick={() => setInstallationId(inst.id)}
                className={
                  "rounded-full border px-3 py-1 text-xs font-medium transition-colors " +
                  (active
                    ? "border-accent bg-accent-soft text-accent"
                    : "border-line bg-surface-2 text-muted hover:border-line-strong")
                }
              >
                {inst.account || inst.account_login || inst.id}
              </button>
            );
          })}
        </div>
      ) : null}

      <TextInput
        type="search"
        placeholder="Search repositories…"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        aria-label="Search repositories"
      />

      {repos.isPending ? (
        <Loading label="Loading repositories…" />
      ) : repos.error ? (
        isGithubUnconfigured(repos.error) ? (
          <GithubUnconfigured />
        ) : (
          <ErrorState
            title="Failed to load repositories"
            error={repos.error}
            onRetry={() => repos.refetch()}
          />
        )
      ) : filtered.length === 0 ? (
        <p className="px-1 py-4 text-sm text-muted">
          {query ? "No repositories match your search." : "No repositories in this installation."}
        </p>
      ) : (
        <ul className="m-0 grid max-h-80 list-none gap-1.5 overflow-y-auto p-0">
          {filtered.map((repo) => {
            const busy = busyRepo === repo.full_name;
            return (
              <li
                key={repo.id || repo.full_name}
                className="flex items-center justify-between gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2.5 text-sm"
              >
                <div className="min-w-0">
                  <div className="truncate font-mono text-text">{repo.full_name}</div>
                  <div className="mt-0.5 flex items-center gap-2 text-[11px] text-subtle">
                    {repo.private ? <span>private</span> : <span>public</span>}
                    {repo.default_branch ? <span>· {repo.default_branch}</span> : null}
                  </div>
                </div>
                <ActionButton
                  onClick={() =>
                    onSelect({ installationId: activeInstallation as string, repo })
                  }
                >
                  {busy ? "Working…" : ctaLabel}
                </ActionButton>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
