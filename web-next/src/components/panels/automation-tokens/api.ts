import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type ProjectAccess = {
  principal_kind: "operator" | "developer" | "human";
  workspace_id: string;
  project_id: string;
  environment_id?: string;
  workspace_role?: "admin" | "member" | "deployer" | "viewer";
  project_role?: "owner" | "editor" | "viewer";
  environment_restricted: boolean;
  permissions: string[];
};

export type ProjectAutomationTokenProfile = "ci-deploy-v1";

export type ProjectAutomationToken = {
  id: string;
  service_account_id: string;
  workspace_id: string;
  project_id: string;
  name: string;
  token_prefix: string;
  profile: ProjectAutomationTokenProfile;
  created_by_user_id?: string;
  created_at: string;
  updated_at: string;
  expires_at: string;
  last_used_at?: string;
  revoked_at?: string;
  revoked_by_user_id?: string;
  revoked_reason?: string;
  resource_version: number;
};

export type ProjectAutomationTokenList = {
  items: ProjectAutomationToken[];
};

export type CreateProjectAutomationTokenInput = {
  name: string;
  expiresAt: string;
};

export type CreateProjectAutomationTokenResult = {
  access_token: string;
  token: ProjectAutomationToken;
};

export const projectAutomationTokenKeys = {
  access: (projectId: string, environmentId?: string) =>
    ["project-access", projectId, environmentId ?? null] as const,
  list: (projectId: string) =>
    ["project-automation-tokens", projectId] as const,
};

function mutationKey(prefix: string): string {
  try {
    return `${prefix}-${crypto.randomUUID()}`;
  } catch {
    return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

function projectTokenPath(projectId: string): string {
  return `/projects/${encodeURIComponent(projectId)}/automation-tokens`;
}

export async function fetchProjectAccess(
  projectId: string,
  environmentId?: string,
  signal?: AbortSignal,
): Promise<ProjectAccess> {
  if (!projectId || environmentId === "") {
    throw new Error("A valid project and environment scope is required.");
  }
  const query = environmentId
    ? `?environment_id=${encodeURIComponent(environmentId)}`
    : "";
  const result = await apiFetch<ProjectAccess>(
    `/projects/${encodeURIComponent(projectId)}/access${query}`,
    { auth: "human", signal },
  );
  const environmentMatches =
    environmentId === undefined
      ? result?.environment_id === undefined ||
        (typeof result.environment_id === "string" &&
          result.environment_id.length > 0)
      : result?.environment_id === environmentId;
  if (
    !result ||
    result.principal_kind !== "human" ||
    result.project_id !== projectId ||
    typeof result.workspace_id !== "string" ||
    result.workspace_id.length === 0 ||
    !environmentMatches ||
    (result.environment_restricted &&
      (typeof result.environment_id !== "string" ||
        result.environment_id.length === 0)) ||
    typeof result.environment_restricted !== "boolean" ||
    !Array.isArray(result.permissions) ||
    result.permissions.some(
      (permission) => typeof permission !== "string" || permission.length === 0,
    )
  ) {
    throw new Error("Midnight returned an invalid project access response.");
  }
  return result;
}

export function fetchProjectAutomationTokens(
  projectId: string,
  signal?: AbortSignal,
): Promise<ProjectAutomationTokenList> {
  return apiFetch<ProjectAutomationTokenList>(projectTokenPath(projectId), {
    auth: "human",
    signal,
  });
}

export async function createProjectAutomationToken(
  projectId: string,
  input: CreateProjectAutomationTokenInput,
  idempotencyKey = mutationKey("project-token-create"),
): Promise<CreateProjectAutomationTokenResult> {
  const result = await apiFetch<CreateProjectAutomationTokenResult>(
    projectTokenPath(projectId),
    {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: {
        name: input.name,
        expires_at: input.expiresAt,
      },
    },
  );
  if (
    !result ||
    typeof result.access_token !== "string" ||
    !result.access_token.startsWith("midn_sa_") ||
    !result.token ||
    result.token.project_id !== projectId ||
    result.token.profile !== "ci-deploy-v1"
  ) {
    throw new Error(
      "Midnight returned an invalid project automation-token response.",
    );
  }
  return result;
}

export function revokeProjectAutomationToken(
  projectId: string,
  tokenId: string,
  idempotencyKey = mutationKey("project-token-revoke"),
): Promise<void> {
  return apiFetch<void>(
    `${projectTokenPath(projectId)}/${encodeURIComponent(tokenId)}`,
    {
      auth: "human",
      method: "DELETE",
      headers: { "Idempotency-Key": idempotencyKey },
    },
  );
}

export function useProjectAccess(
  projectId: string,
  environmentId: string | undefined,
  enabled: boolean,
) {
  return useQuery<ProjectAccess, Error>({
    queryKey: projectAutomationTokenKeys.access(projectId, environmentId),
    queryFn: ({ signal }) =>
      fetchProjectAccess(projectId, environmentId, signal),
    enabled: enabled && Boolean(projectId),
    staleTime: 30_000,
  });
}

export function useProjectAutomationTokens(
  projectId: string,
  enabled: boolean,
) {
  return useQuery<ProjectAutomationTokenList, Error>({
    queryKey: projectAutomationTokenKeys.list(projectId),
    queryFn: ({ signal }) =>
      fetchProjectAutomationTokens(projectId, signal),
    enabled: enabled && Boolean(projectId),
  });
}

export function useCreateProjectAutomationToken(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation<
    CreateProjectAutomationTokenResult,
    Error,
    CreateProjectAutomationTokenInput
  >({
    mutationFn: (input) => createProjectAutomationToken(projectId, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: projectAutomationTokenKeys.list(projectId),
      });
    },
  });
}

export function useRevokeProjectAutomationToken(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation<void, Error, string>({
    mutationFn: (tokenId) =>
      revokeProjectAutomationToken(projectId, tokenId),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: projectAutomationTokenKeys.list(projectId),
      });
    },
  });
}
