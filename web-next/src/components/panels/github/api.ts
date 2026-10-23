// Panel-local GitHub link surface for GithubConnect and the New-Project /
// Add-Service GitHub source pickers. Read paths (installations, repos) stay on
// the shared `useGithubInstallations` / `useGithubRepos` hooks; only the link
// mutation lives here.
//
// Endpoint: PUT /services/{id}/github  (REST — no Idempotency-Key required by
// the handler; it is a spec upsert, naturally idempotent). Mirrors the
// GraphQL `serviceGithubLink` mutation.

import { ApiError, apiFetch } from "@/api/client";

export type ProjectGithubRepository = {
  installation_id: number;
  repository_id: number;
  repository_full_name: string;
  account_login?: string;
  private: boolean;
  default_branch: string;
};

export type ProjectGithubBranch = {
  name: string;
};

export type HumanGithubLinkInput = {
  installationId: number;
  repositoryId: number;
  branch: string;
};

export type HumanGithubLinkView = {
  installation_id: number;
  repository_id: number;
  repository_full_name: string;
  branch: string;
  binding_state?: "bound" | "legacy_unverified";
  repository_access_verified_at?: string;
  updated_at?: string;
};

export type GithubLinkInput = {
  installationId: number | string;
  repo: string;
  branch: string;
  cloneUrl?: string;
  watchPaths?: string[];
  rootDirectory?: string;
  configPath?: string;
  waitForCi?: boolean;
};

export type GithubLinkView = {
  installation_id: number;
  repo: string;
  branch: string;
  watch_paths?: string[];
  root_directory?: string;
  config_path?: string;
  wait_for_ci?: boolean;
};

export async function linkServiceGithub(
  serviceId: string,
  input: GithubLinkInput,
): Promise<GithubLinkView> {
  return apiFetch<GithubLinkView>(`/services/${encodeURIComponent(serviceId)}/github`, {
    method: "PUT",
    body: {
      installation_id: Number(input.installationId),
      repo: input.repo,
      branch: input.branch,
      clone_url: input.cloneUrl,
      watch_paths: input.watchPaths,
      root_directory: input.rootDirectory,
      config_path: input.configPath,
      wait_for_ci: input.waitForCi,
    },
  });
}

/**
 * Human-session source discovery is deliberately project-scoped. These
 * functions never call or fall back to the recovery-only global installation
 * routes.
 */
export async function listProjectGithubRepositories(
  projectId: string,
  signal?: AbortSignal,
): Promise<ProjectGithubRepository[]> {
  requireResourceId(projectId, "project");
  const raw = await apiFetch<unknown>(
    `/projects/${encodeURIComponent(projectId)}/github/repositories`,
    { auth: "human", signal },
  );
  return decodeCollection(raw, decodeRepository, "repository");
}

export async function listProjectGithubBranches(
  projectId: string,
  repositoryId: number,
  signal?: AbortSignal,
): Promise<ProjectGithubBranch[]> {
  requireResourceId(projectId, "project");
  requirePositiveInteger(repositoryId, "repository");
  const raw = await apiFetch<unknown>(
    `/projects/${encodeURIComponent(projectId)}/github/repositories/${encodeURIComponent(repositoryId)}/branches`,
    { auth: "human", signal },
  );
  return decodeCollection(raw, decodeBranch, "branch");
}

export async function getHumanServiceGithubLink(
  serviceId: string,
  signal?: AbortSignal,
): Promise<HumanGithubLinkView | null> {
  requireResourceId(serviceId, "service");
  try {
    const raw = await apiFetch<unknown>(
      `/services/${encodeURIComponent(serviceId)}/github`,
      { auth: "human", signal },
    );
    return decodeLink(raw);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
}

export async function putHumanServiceGithubLink(
  serviceId: string,
  input: HumanGithubLinkInput,
  idempotencyKey: string,
): Promise<HumanGithubLinkView> {
  requireResourceId(serviceId, "service");
  requirePositiveInteger(input.installationId, "installation");
  requirePositiveInteger(input.repositoryId, "repository");
  const branch = requireBranch(input.branch);
  const key = requireIdempotencyKey(idempotencyKey);
  const raw = await apiFetch<unknown>(
    `/services/${encodeURIComponent(serviceId)}/github`,
    {
      auth: "human",
      method: "PUT",
      headers: { "Idempotency-Key": key },
      body: {
        installation_id: input.installationId,
        repository_id: input.repositoryId,
        branch,
      },
    },
  );
  return decodeLink(raw);
}

export async function deleteHumanServiceGithubLink(
  serviceId: string,
  idempotencyKey: string,
): Promise<void> {
  requireResourceId(serviceId, "service");
  const key = requireIdempotencyKey(idempotencyKey);
  await apiFetch<void>(`/services/${encodeURIComponent(serviceId)}/github`, {
    auth: "human",
    method: "DELETE",
    headers: { "Idempotency-Key": key },
  });
}

/**
 * The GitHub App is optional in a self-hosted Midnight. When no App is
 * configured the installations/repos endpoints answer `501 Not Implemented`.
 * Detect that so the UI can show a calm "connect the App" affordance instead
 * of a red error.
 */
export function isGithubUnconfigured(err: unknown): boolean {
  return err instanceof ApiError && err.status === 501;
}

function requireResourceId(value: string, kind: string): string {
  const normalized = value.trim();
  if (!normalized || normalized !== value) {
    throw new Error(`A valid ${kind} id is required.`);
  }
  return normalized;
}

function requirePositiveInteger(value: number, kind: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error(`A valid ${kind} id is required.`);
  }
  return value;
}

function requireBranch(value: string): string {
  if (
    !value ||
    value !== value.trim() ||
    value.startsWith("refs/") ||
    value.length > 255 ||
    /[\s\u0000-\u001f\u007f]/.test(value)
  ) {
    throw new Error("Enter a branch name, such as main. It cannot start with refs/ or contain spaces.");
  }
  return value;
}

function requireIdempotencyKey(value: string): string {
  const normalized = value.trim();
  if (!normalized || normalized !== value) {
    console.error(
      "midnight: refusing GitHub write without a stable idempotency key",
    );
    throw new Error("Something went wrong preparing that request. Please try again.", {
      cause: "unstable GitHub idempotency key",
    });
  }
  return normalized;
}

function decodeCollection<T>(
  raw: unknown,
  decode: (value: unknown) => T,
  kind: string,
): T[] {
  const items = Array.isArray(raw)
    ? raw
    : isRecord(raw) && Array.isArray(raw.items)
      ? raw.items
      : undefined;
  if (!items) {
    throw new Error(`Midnight returned an invalid GitHub ${kind} list.`);
  }
  return items.map(decode);
}

function decodeRepository(value: unknown): ProjectGithubRepository {
  if (!isRecord(value)) {
    throw new Error("Midnight returned an invalid GitHub repository.");
  }
  const fullName =
    readNonEmptyString(value.repository_full_name) ??
    readNonEmptyString(value.full_name);
  const defaultBranch = readNonEmptyString(value.default_branch);
  if (
    !Number.isSafeInteger(value.installation_id) ||
    Number(value.installation_id) <= 0 ||
    !Number.isSafeInteger(value.repository_id ?? value.id) ||
    Number(value.repository_id ?? value.id) <= 0 ||
    !fullName ||
    !fullName.includes("/") ||
    !defaultBranch ||
    typeof value.private !== "boolean"
  ) {
    throw new Error("Midnight returned an invalid GitHub repository.");
  }
  return {
    installation_id: Number(value.installation_id),
    repository_id: Number(value.repository_id ?? value.id),
    repository_full_name: fullName,
    ...(readNonEmptyString(value.account_login)
      ? { account_login: readNonEmptyString(value.account_login) }
      : {}),
    private: value.private,
    default_branch: defaultBranch,
  };
}

function decodeBranch(value: unknown): ProjectGithubBranch {
  const name =
    typeof value === "string"
      ? readNonEmptyString(value)
      : isRecord(value)
        ? readNonEmptyString(value.name)
        : undefined;
  if (!name) {
    throw new Error("Midnight returned an invalid GitHub branch.");
  }
  return { name };
}

function decodeLink(value: unknown): HumanGithubLinkView {
  if (!isRecord(value)) {
    throw new Error("Midnight returned an invalid GitHub source binding.");
  }
  const fullName =
    readNonEmptyString(value.repository_full_name) ??
    readNonEmptyString(value.repo);
  const branch = readNonEmptyString(value.branch);
  if (
    !Number.isSafeInteger(value.installation_id) ||
    Number(value.installation_id) <= 0 ||
    !Number.isSafeInteger(value.repository_id) ||
    Number(value.repository_id) <= 0 ||
    !fullName ||
    !branch
  ) {
    throw new Error("Midnight returned an invalid GitHub source binding.");
  }
  const bindingState =
    value.binding_state === "bound" ||
    value.binding_state === "legacy_unverified"
      ? value.binding_state
      : undefined;
  return {
    installation_id: Number(value.installation_id),
    repository_id: Number(value.repository_id),
    repository_full_name: fullName,
    branch,
    ...(bindingState ? { binding_state: bindingState } : {}),
    ...(readNonEmptyString(value.repository_access_verified_at)
      ? {
          repository_access_verified_at: readNonEmptyString(
            value.repository_access_verified_at,
          ),
        }
      : {}),
    ...(readNonEmptyString(value.updated_at)
      ? { updated_at: readNonEmptyString(value.updated_at) }
      : {}),
  };
}

function readNonEmptyString(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim();
  return normalized ? normalized : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
