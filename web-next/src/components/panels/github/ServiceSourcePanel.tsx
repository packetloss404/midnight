import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import { ApiError } from "@/api/client";
import { deployServiceNow } from "@/api/deployActions";
import { ErrorState, Loading } from "@/components/states";
import { Button, Card, InlineError, TextInput } from "@/components/panels/form-kit";
import {
  deleteHumanServiceGithubLink,
  getHumanServiceGithubLink,
  listProjectGithubBranches,
  listProjectGithubRepositories,
  putHumanServiceGithubLink,
  type HumanGithubLinkInput,
  type HumanGithubLinkView,
  type ProjectGithubRepository,
} from "./api";

export type ServiceSourcePanelProps = {
  projectId: string;
  serviceId: string;
  /**
   * Environment the first deployment should target. Linking a source starts
   * nothing on purpose, so without an environment scope "Deploy now" has no
   * place to deploy to and is offered as unavailable rather than hidden.
   */
  environmentId?: string;
};

export const serviceSourceKeys = {
  link: (serviceId: string) =>
    ["service-source", "github", serviceId] as const,
  repositories: (projectId: string) =>
    ["project-source", "github", projectId, "repositories"] as const,
  branches: (projectId: string, repositoryId?: number) =>
    [
      "project-source",
      "github",
      projectId,
      "repositories",
      repositoryId ?? null,
      "branches",
    ] as const,
};

type PendingConnectIntent = HumanGithubLinkInput & {
  key: string;
};

export function ServiceSourcePanel({
  projectId,
  serviceId,
  environmentId,
}: ServiceSourcePanelProps) {
  const queryClient = useQueryClient();
  const [picking, setPicking] = useState(false);
  const [search, setSearch] = useState("");
  const [selectedRepository, setSelectedRepository] =
    useState<ProjectGithubRepository | null>(null);
  const [branch, setBranch] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [busy, setBusy] = useState<"connect" | "disconnect" | "deploy" | null>(
    null,
  );
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [deployNotice, setDeployNotice] = useState<string | null>(null);
  const [deployKey, setDeployKey] = useState<string | null>(null);
  const [pendingConnect, setPendingConnect] =
    useState<PendingConnectIntent | null>(null);
  const [disconnectKey, setDisconnectKey] = useState<string | null>(null);

  const link = useQuery({
    queryKey: serviceSourceKeys.link(serviceId),
    queryFn: ({ signal }) => getHumanServiceGithubLink(serviceId, signal),
  });
  const repositories = useQuery({
    queryKey: serviceSourceKeys.repositories(projectId),
    queryFn: ({ signal }) =>
      listProjectGithubRepositories(projectId, signal),
    enabled: picking,
  });
  const branches = useQuery({
    queryKey: serviceSourceKeys.branches(
      projectId,
      selectedRepository?.repository_id,
    ),
    queryFn: ({ signal }) =>
      listProjectGithubBranches(
        projectId,
        selectedRepository!.repository_id,
        signal,
      ),
    enabled: picking && Boolean(selectedRepository),
  });

  useEffect(() => {
    if (!selectedRepository || !branches.data) return;
    const available = new Set(branches.data.map((item) => item.name));
    if (branch && available.has(branch)) return;
    setBranch(
      available.has(selectedRepository.default_branch)
        ? selectedRepository.default_branch
        : branches.data[0]?.name ?? "",
    );
  }, [
    branch,
    branches.data,
    selectedRepository,
    selectedRepository?.default_branch,
    selectedRepository?.repository_id,
  ]);

  const filteredRepositories = useMemo(() => {
    const value = search.trim().toLowerCase();
    if (!value) return repositories.data ?? [];
    return (repositories.data ?? []).filter((repository) =>
      repository.repository_full_name.toLowerCase().includes(value),
    );
  }, [repositories.data, search]);

  const current = link.data;

  function beginPicker() {
    setPicking(true);
    setSelectedRepository(null);
    setBranch("");
    setConfirming(false);
    setDisconnecting(false);
    setError(null);
    setNotice(null);
  }

  function cancelPicker() {
    setPicking(false);
    setSelectedRepository(null);
    setBranch("");
    setConfirming(false);
    setError(null);
  }

  function selectRepository(repository: ProjectGithubRepository) {
    setSelectedRepository(repository);
    setBranch("");
    setConfirming(false);
    setError(null);
  }

  async function connect() {
    if (!selectedRepository || !branch || busy) return;
    const input: HumanGithubLinkInput = {
      installationId: selectedRepository.installation_id,
      repositoryId: selectedRepository.repository_id,
      branch,
    };
    const intent =
      pendingConnect &&
      pendingConnect.installationId === input.installationId &&
      pendingConnect.repositoryId === input.repositoryId &&
      pendingConnect.branch === input.branch
        ? pendingConnect
        : { ...input, key: newMutationKey("github-link") };
    setPendingConnect(intent);
    setBusy("connect");
    setError(null);
    setNotice(null);
    try {
      const saved = await putHumanServiceGithubLink(
        serviceId,
        input,
        intent.key,
      );
      queryClient.setQueryData(serviceSourceKeys.link(serviceId), saved);
      setPendingConnect(null);
      setPicking(false);
      setSelectedRepository(null);
      setBranch("");
      setConfirming(false);
      setNotice("Source linked. No deployment was started.");
    } catch (connectError) {
      setError(connectError);
    } finally {
      setBusy(null);
    }
  }

  // Start the service's first (or next) deployment from the saved source.
  // Linking never deploys, so this is the only console path from "source
  // linked" to "deployment exists" that does not require waiting for a push.
  // The idempotency key is held across retries so a failed response cannot
  // turn one intent into two deployments.
  async function startDeployNow() {
    if (busy || !environmentId) return;
    const key = deployKey ?? newMutationKey("deploy-now");
    setDeployKey(key);
    setBusy("deploy");
    setError(null);
    setNotice(null);
    setDeployNotice(null);
    try {
      const result = await deployServiceNow(
        serviceId,
        environmentId,
        "operator",
        key,
      );
      setDeployKey(null);
      setDeployNotice(
        `Deploying from the linked source. Follow progress in Activity (${result.operationId}) — currently ${result.status}.`,
      );
      queryClient.invalidateQueries({ queryKey: ["deployments", serviceId] });
      queryClient.invalidateQueries({ queryKey: ["environment-resources"] });
    } catch (deployError) {
      setError(deployError);
    } finally {
      setBusy(null);
    }
  }

  async function disconnect() {
    if (busy) return;
    const key = disconnectKey ?? newMutationKey("github-unlink");
    setDisconnectKey(key);
    setBusy("disconnect");
    setError(null);
    setNotice(null);
    try {
      await deleteHumanServiceGithubLink(serviceId, key);
      queryClient.setQueryData(serviceSourceKeys.link(serviceId), null);
      setDisconnectKey(null);
      setDisconnecting(false);
      setNotice(
        "Source disconnected. Existing deployments and their history were not removed.",
      );
    } catch (disconnectError) {
      setError(disconnectError);
    } finally {
      setBusy(null);
    }
  }

  if (link.isPending) {
    return <Loading label="Checking service source…" />;
  }
  if (link.error) {
    return (
      <ErrorState
        title="Failed to load the service source"
        error={link.error}
        onRetry={() => link.refetch()}
      />
    );
  }

  return (
    <Card
      title="GitHub source"
      description="Linking does not deploy immediately. Deploy now builds the linked branch on demand; later pushes to it deploy automatically."
    >
      {notice ? (
        <>
          <p
            role="status"
            className="m-0 rounded-[var(--r-sm)] border border-accent/40 bg-accent-soft px-3 py-2 text-xs text-accent"
          >
            {notice}
          </p>
          <p className="m-0 text-xs text-muted">
            Use Deploy now to build this branch straight away, or wait for the
            next push.
          </p>
        </>
      ) : null}

      {deployNotice ? (
        <p
          role="status"
          className="m-0 rounded-[var(--r-sm)] border border-accent/40 bg-accent-soft px-3 py-2 text-xs text-accent"
        >
          {deployNotice}
        </p>
      ) : null}

      {current && !picking ? (
        <CurrentBinding binding={current} />
      ) : !picking ? (
        <div className="rounded-[var(--r-sm)] border border-dashed border-line bg-bg-2 px-3 py-4">
          <strong className="block text-sm text-text">
            No GitHub source is linked
          </strong>
          <span className="mt-1 block text-xs text-muted">
            Existing image and local-source deployments are unchanged.
          </span>
        </div>
      ) : null}

      {!picking && !disconnecting ? (
        <div className="flex flex-wrap gap-2">
          {current ? (
            <Button
              disabled={busy !== null || !environmentId}
              onClick={() => void startDeployNow()}
            >
              {busy === "deploy" ? "Requesting…" : "Deploy now"}
            </Button>
          ) : null}
          <Button variant={current ? "secondary" : undefined} onClick={beginPicker}>
            {current ? "Change source" : "Connect source"}
          </Button>
          {current ? (
            <Button
              variant="danger"
              onClick={() => {
                setDisconnecting(true);
                setError(null);
                setNotice(null);
              }}
            >
              Disconnect
            </Button>
          ) : null}
        </div>
      ) : null}

      {current && !picking && !disconnecting && !environmentId ? (
        <p className="m-0 text-xs text-muted">
          Deploy now needs an environment to deploy into. Open this service
          from an environment to build the linked branch on demand.
        </p>
      ) : null}

      {disconnecting ? (
        <div className="grid gap-3 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-3 py-3">
          <div className="grid gap-1 text-xs">
            <strong className="text-danger">Disconnect GitHub source?</strong>
            <span className="text-muted">
              Midnight will remove this saved source intent. Existing
              deployments and history remain.
            </span>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="danger"
              disabled={busy !== null}
              onClick={() => void disconnect()}
            >
              {busy === "disconnect" ? "Disconnecting…" : "Confirm disconnect"}
            </Button>
            <Button
              variant="secondary"
              disabled={busy !== null}
              onClick={() => {
                setDisconnecting(false);
                setError(null);
              }}
            >
              Keep source
            </Button>
          </div>
        </div>
      ) : null}

      {picking ? (
        <SourcePicker
          current={current}
          repositories={repositories}
          branches={branches}
          filteredRepositories={filteredRepositories}
          search={search}
          onSearch={setSearch}
          selectedRepository={selectedRepository}
          branch={branch}
          onSelectRepository={selectRepository}
          onBranch={setBranch}
          confirming={confirming}
          onConfirming={setConfirming}
          onConnect={() => void connect()}
          onCancel={cancelPicker}
          busy={busy === "connect"}
        />
      ) : null}

      {error ? <InlineError error={error} /> : null}
    </Card>
  );
}

function CurrentBinding({ binding }: { binding: HumanGithubLinkView }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3">
      <div className="min-w-0">
        <div className="truncate font-mono text-sm text-text">
          {binding.repository_full_name}
        </div>
        <div className="mt-1 text-[11px] text-subtle">
          Branch <span className="font-mono">{binding.branch}</span>
        </div>
      </div>
      <span className="rounded-full border border-accent/50 bg-accent-soft px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-accent">
        {binding.binding_state === "legacy_unverified"
          ? "Reconnect required"
          : "Saved binding"}
      </span>
    </div>
  );
}

function SourcePicker({
  current,
  repositories,
  branches,
  filteredRepositories,
  search,
  onSearch,
  selectedRepository,
  branch,
  onSelectRepository,
  onBranch,
  confirming,
  onConfirming,
  onConnect,
  onCancel,
  busy,
}: {
  current: HumanGithubLinkView | null | undefined;
  repositories: UseQueryResult<ProjectGithubRepository[], Error>;
  branches: UseQueryResult<{ name: string }[], Error>;
  filteredRepositories: ProjectGithubRepository[];
  search: string;
  onSearch: (value: string) => void;
  selectedRepository: ProjectGithubRepository | null;
  branch: string;
  onSelectRepository: (repository: ProjectGithubRepository) => void;
  onBranch: (branch: string) => void;
  confirming: boolean;
  onConfirming: (value: boolean) => void;
  onConnect: () => void;
  onCancel: () => void;
  busy: boolean;
}) {
  if (repositories.isPending) {
    return <Loading label="Loading granted repositories…" />;
  }
  if (repositories.error) {
    return (
      <ScopedGithubError
        error={repositories.error}
        retry={() => repositories.refetch()}
        cancel={onCancel}
      />
    );
  }
  if (!repositories.data || repositories.data.length === 0) {
    return (
      <ScopeNotice
        title="No repositories granted"
        detail="No GitHub repositories are granted to this project’s workspace. Ask an instance operator to grant one, then retry."
        action={
          <div className="flex flex-wrap gap-2">
            <Button
              variant="secondary"
              onClick={() => void repositories.refetch()}
            >
              Retry
            </Button>
            <Button variant="secondary" onClick={onCancel}>
              Cancel
            </Button>
          </div>
        }
      />
    );
  }

  return (
    <div className="grid gap-3">
      <TextInput
        type="search"
        value={search}
        onChange={(event) => onSearch(event.target.value)}
        placeholder="Search granted repositories…"
        aria-label="Search granted repositories"
      />

      <div className="grid max-h-56 gap-1.5 overflow-y-auto">
        {filteredRepositories.length === 0 ? (
          <p className="m-0 px-1 py-3 text-xs text-muted">
            No granted repositories match this search.
          </p>
        ) : (
          filteredRepositories.map((repository) => {
            const selected =
              selectedRepository?.repository_id === repository.repository_id &&
              selectedRepository.installation_id ===
                repository.installation_id;
            return (
              <button
                key={`${repository.installation_id}:${repository.repository_id}`}
                type="button"
                aria-pressed={selected}
                onClick={() => onSelectRepository(repository)}
                className={
                  "flex items-center justify-between gap-3 rounded-[var(--r-sm)] border px-3 py-2.5 text-left " +
                  (selected
                    ? "border-accent bg-accent-soft"
                    : "border-line bg-surface-2 hover:border-line-strong")
                }
              >
                <span className="min-w-0">
                  <span className="block truncate font-mono text-xs text-text">
                    {repository.repository_full_name}
                  </span>
                  <span className="mt-0.5 block text-[10px] text-subtle">
                    {repository.account_login
                      ? `${repository.account_login} · `
                      : ""}
                    {repository.private ? "private" : "public"}
                  </span>
                </span>
                <span className="text-[10px] font-semibold uppercase tracking-wider text-muted">
                  {selected ? "Selected" : "Choose"}
                </span>
              </button>
            );
          })
        )}
      </div>

      {selectedRepository ? (
        branches.isPending ? (
          <Loading label="Loading branches…" />
        ) : branches.error ? (
          <ScopedGithubError
            error={branches.error}
            retry={() => branches.refetch()}
          />
        ) : !branches.data || branches.data.length === 0 ? (
          <ScopeNotice
            title="No branches available"
            detail="Midnight could not find a branch available through this workspace grant."
          />
        ) : (
          <label className="grid gap-1.5">
            <span className="text-xs font-semibold text-text">Branch</span>
            <select
              value={branch}
              onChange={(event) => {
                onBranch(event.target.value);
                onConfirming(false);
              }}
              className="field-input"
            >
              {branches.data.map((item) => (
                <option key={item.name} value={item.name}>
                  {item.name}
                </option>
              ))}
            </select>
          </label>
        )
      ) : null}

      {selectedRepository && branch && confirming ? (
        <div className="grid gap-3 rounded-[var(--r-sm)] border border-accent/40 bg-accent-soft px-3 py-3">
          <div className="grid gap-1 text-xs">
            <strong className="text-text">Confirm source connection</strong>
            <span className="text-muted">
              Bind{" "}
              <span className="font-mono">
                {selectedRepository.repository_full_name}@{branch}
              </span>{" "}
              to this service? This saves source intent only; no deployment
              starts.
            </span>
            {current ? (
              <span className="text-subtle">
                This replaces the current saved source binding.
              </span>
            ) : null}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button disabled={busy} onClick={onConnect}>
              {busy ? "Connecting…" : "Confirm connection"}
            </Button>
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => onConfirming(false)}
            >
              Back
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={!selectedRepository || !branch}
            onClick={() => onConfirming(true)}
          >
            Review connection
          </Button>
          <Button variant="secondary" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      )}
    </div>
  );
}

function ScopedGithubError({
  error,
  retry,
  cancel,
}: {
  error: Error;
  retry: () => unknown;
  cancel?: () => void;
}) {
  if (error instanceof ApiError && error.status === 501) {
    return (
      <ScopeNotice
        title="GitHub source is not configured"
        detail="This Midnight instance has no tenant GitHub source integration configured. No global installation lookup was attempted."
        action={
          cancel ? (
            <Button variant="secondary" onClick={cancel}>
              Cancel
            </Button>
          ) : undefined
        }
      />
    );
  }
  if (error instanceof ApiError && error.status === 403) {
    return (
      <ScopeNotice
        title="Source access required"
        detail="Your current project role cannot browse or bind GitHub sources."
        action={
          cancel ? (
            <Button variant="secondary" onClick={cancel}>
              Cancel
            </Button>
          ) : undefined
        }
      />
    );
  }
  return (
    <div className="grid gap-2">
      <ErrorState
        title="Failed to load GitHub source choices"
        error={error}
        onRetry={() => void retry()}
      />
      {cancel ? (
        <Button variant="secondary" onClick={cancel}>
          Cancel
        </Button>
      ) : null}
    </div>
  );
}

function ScopeNotice({
  title,
  detail,
  action,
}: {
  title: string;
  detail: string;
  action?: ReactNode;
}) {
  return (
    <div className="grid gap-2 rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-3">
      <strong className="text-sm text-text">{title}</strong>
      <span className="text-xs text-muted">{detail}</span>
      {action}
    </div>
  );
}

function newMutationKey(prefix: string): string {
  try {
    return `${prefix}-${crypto.randomUUID()}`;
  } catch {
    return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}
