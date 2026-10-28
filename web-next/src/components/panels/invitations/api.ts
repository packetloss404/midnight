import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type WorkspaceInvitationRole =
  | "admin"
  | "member"
  | "deployer"
  | "viewer";
export type ProjectInvitationRole = "owner" | "editor" | "viewer";
export type InvitationRole =
  | WorkspaceInvitationRole
  | ProjectInvitationRole;

export type IdentityInvitation = {
  id: string;
  email: string;
  workspace_id: string;
  project_id?: string;
  workspace_role?: WorkspaceInvitationRole;
  project_role?: ProjectInvitationRole;
  token_prefix: string;
  invited_by_user_id: string;
  created_at: string;
  updated_at: string;
  expires_at: string;
  revoked_at?: string;
  revoked_by_user_id?: string;
  revoked_reason?: string;
  accepted_at?: string;
  accepted_by_user_id?: string;
  replaces_invitation_id?: string;
  resource_version: number;
};

export type IdentityInvitationList = {
  items: IdentityInvitation[];
  next_cursor?: string;
};

export type IdentityInvitationCredentialResult = {
  invitation_token: string;
  invitation: IdentityInvitation;
};

export type InvitationOnboardingResult = {
  user_id: string;
  email: string;
  invitation: IdentityInvitation;
};

// Keep credentials in this call's memory. This is deliberately not a React
// Query mutation, whose retained variables would extend the secret lifetime.
export async function onboardIdentityInvitation(input: {
  invitation_token: string;
  display_name: string;
  password: string;
}): Promise<InvitationOnboardingResult> {
  const result = await apiFetch<InvitationOnboardingResult>("/invitations/onboard", {
    auth: "none",
    method: "POST",
    body: input,
  });
  if (!result || typeof result.user_id !== "string" || !result.user_id.startsWith("usr_") ||
      typeof result.email !== "string" || !validInvitationMetadata(result.invitation) ||
      result.invitation.email !== result.email || !result.invitation.accepted_at ||
      result.invitation.accepted_by_user_id !== result.user_id || "invitation_token" in result) {
    throw new Error("Midnight returned an invalid invitation enrollment.");
  }
  return result;
}

export type InvitationScope = {
  workspaceId: string;
  projectId?: string;
};

export type CreateIdentityInvitationInput = {
  email: string;
  role: InvitationRole;
  expiresAt: string;
};

export type ReissueIdentityInvitationInput = {
  invitationId: string;
  expiresAt: string;
};

export const identityInvitationKeys = {
  list: (scope: InvitationScope) =>
    [
      "identity-invitations",
      scope.workspaceId,
      scope.projectId ?? null,
    ] as const,
};

function mutationKey(prefix: string): string {
  try {
    return `${prefix}-${crypto.randomUUID()}`;
  } catch {
    return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

function scopeQuery(scope: InvitationScope): string {
  const query = new URLSearchParams({ workspace_id: scope.workspaceId });
  if (scope.projectId) query.set("project_id", scope.projectId);
  return query.toString();
}

function pageQuery(
  scope: InvitationScope,
  page: { limit: number; cursor?: string },
): string {
  const query = new URLSearchParams(scopeQuery(scope));
  query.set("limit", String(page.limit));
  if (page.cursor) query.set("cursor", page.cursor);
  return query.toString();
}

function invitationPath(invitationId: string): string {
  return `/invitations/${encodeURIComponent(invitationId)}`;
}

export async function fetchIdentityInvitations(
  scope: InvitationScope,
  signal?: AbortSignal,
  page: { limit?: number; cursor?: string } = {},
): Promise<IdentityInvitationList> {
  const result = await apiFetch<IdentityInvitationList>(
    `/invitations?${pageQuery(scope, {
      limit: page.limit ?? 50,
      cursor: page.cursor,
    })}`,
    { auth: "human", signal },
  );
  if (
    !result ||
    !Array.isArray(result.items) ||
    result.items.some(
      (invitation) =>
        !validScopedInvitation(scope, invitation) ||
        "invitation_token" in invitation,
    ) ||
    (result.next_cursor !== undefined &&
      typeof result.next_cursor !== "string")
  ) {
    throw new Error("Midnight returned an invalid invitation metadata page.");
  }
  return result;
}

export async function createIdentityInvitation(
  scope: InvitationScope,
  input: CreateIdentityInvitationInput,
  idempotencyKey = mutationKey("identity-invitation-create"),
): Promise<IdentityInvitationCredentialResult> {
  const role = scopedRole(scope, input.role);
  const email = input.email.trim().toLowerCase();
  const result = await apiFetch<IdentityInvitationCredentialResult>(
    "/invitations",
    {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: {
        email,
        workspace_id: scope.workspaceId,
        ...(scope.projectId
          ? {
              project_id: scope.projectId,
              project_role: role,
            }
          : { workspace_role: role }),
        expires_at: input.expiresAt,
      },
    },
  );
  const validated = validateCredentialResult(scope, result);
  const responseRole =
    validated.invitation.project_role ??
    validated.invitation.workspace_role;
  if (
    validated.invitation.email !== email ||
    responseRole !== role
  ) {
    throw new Error(
      "Midnight returned an invitation for a different recipient or role.",
    );
  }
  return validated;
}

export async function reissueIdentityInvitation(
  scope: InvitationScope,
  input: ReissueIdentityInvitationInput,
  idempotencyKey = mutationKey("identity-invitation-reissue"),
): Promise<IdentityInvitationCredentialResult> {
  const result = await apiFetch<IdentityInvitationCredentialResult>(
    `${invitationPath(input.invitationId)}/reissue`,
    {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: {
        workspace_id: scope.workspaceId,
        ...(scope.projectId ? { project_id: scope.projectId } : {}),
        expires_at: input.expiresAt,
      },
    },
  );
  return validateCredentialResult(scope, result);
}

export async function revokeIdentityInvitation(
  scope: InvitationScope,
  invitationId: string,
  expectedVersion: number,
  idempotencyKey = mutationKey("identity-invitation-revoke"),
): Promise<void> {
  const path = `${invitationPath(invitationId)}?${scopeQuery(scope)}`;
  if (!Number.isSafeInteger(expectedVersion) || expectedVersion < 1) {
    throw new Error("Reload invitation metadata before revoking it.");
  }
  return apiFetch<void>(
    path,
    {
      auth: "human",
      method: "DELETE",
      headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${expectedVersion}"` },
    },
  );
}

export async function acceptIdentityInvitation(
  invitationToken: string,
  idempotencyKey = mutationKey("identity-invitation-accept"),
): Promise<IdentityInvitation> {
  const token = invitationToken.trim();
  const result = await apiFetch<IdentityInvitation>("/invitations/accept", {
    auth: "human",
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: { invitation_token: token },
  });
  if (
    !validInvitationMetadata(result) ||
    !result.accepted_at ||
    "invitation_token" in result
  ) {
    throw new Error("Midnight returned an invalid invitation acceptance.");
  }
  return result;
}

export function useIdentityInvitations(
  scope: InvitationScope,
  enabled: boolean,
) {
  const query = useInfiniteQuery({
    queryKey: identityInvitationKeys.list(scope),
    queryFn: ({ signal, pageParam }) =>
      fetchIdentityInvitations(scope, signal, {
        limit: 50,
        cursor: pageParam || undefined,
      }),
    initialPageParam: "",
    getNextPageParam: (lastPage: IdentityInvitationList) =>
      typeof lastPage.next_cursor === "string"
        ? lastPage.next_cursor.trim() || undefined
        : undefined,
    enabled: enabled && Boolean(scope.workspaceId),
  });
  const data = query.data
    ? {
        items: query.data.pages.flatMap((page) => page.items),
        next_cursor: normalizeCursor(
          query.data.pages.at(-1)?.next_cursor,
        ),
      }
    : undefined;
  return { ...query, data };
}

export function useCreateIdentityInvitation(scope: InvitationScope) {
  const queryClient = useQueryClient();
  return useMutation<
    IdentityInvitationCredentialResult,
    Error,
    CreateIdentityInvitationInput
  >({
    mutationFn: (input) => createIdentityInvitation(scope, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: identityInvitationKeys.list(scope),
      });
    },
  });
}

export function useReissueIdentityInvitation(scope: InvitationScope) {
  const queryClient = useQueryClient();
  return useMutation<
    IdentityInvitationCredentialResult,
    Error,
    ReissueIdentityInvitationInput
  >({
    mutationFn: (input) => reissueIdentityInvitation(scope, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: identityInvitationKeys.list(scope),
      });
    },
  });
}

export function useRevokeIdentityInvitation(scope: InvitationScope) {
  const queryClient = useQueryClient();
  return useMutation<void, Error, { invitationId: string; expectedVersion: number }>({
    mutationFn: ({ invitationId, expectedVersion }) =>
      revokeIdentityInvitation(scope, invitationId, expectedVersion),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: identityInvitationKeys.list(scope),
      });
    },
  });
}

export function useAcceptIdentityInvitation() {
  const queryClient = useQueryClient();
  return useMutation<IdentityInvitation, Error, string>({
    mutationFn: (token) => acceptIdentityInvitation(token),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
      void queryClient.invalidateQueries({ queryKey: ["project-access"] });
    },
  });
}

function scopedRole(
  scope: InvitationScope,
  role: InvitationRole,
): InvitationRole {
  const projectRoles: InvitationRole[] = ["owner", "editor", "viewer"];
  const workspaceRoles: InvitationRole[] = [
    "admin",
    "member",
    "deployer",
    "viewer",
  ];
  const allowed = scope.projectId ? projectRoles : workspaceRoles;
  if (!allowed.includes(role)) {
    throw new Error("Choose a role valid for this invitation scope.");
  }
  return role;
}

function validateCredentialResult(
  scope: InvitationScope,
  result: IdentityInvitationCredentialResult,
): IdentityInvitationCredentialResult {
  if (
    !result ||
    typeof result.invitation_token !== "string" ||
    !result.invitation_token.startsWith("midn_inv_") ||
    !validInvitationMetadata(result.invitation) ||
    result.invitation.workspace_id !== scope.workspaceId ||
    (result.invitation.project_id ?? "") !== (scope.projectId ?? "") ||
    (scope.projectId
      ? !result.invitation.project_role ||
        Boolean(result.invitation.workspace_role)
      : !result.invitation.workspace_role ||
        Boolean(result.invitation.project_role))
  ) {
    throw new Error(
      "Midnight returned an invalid invitation credential response.",
    );
  }
  return result;
}

function validInvitationMetadata(
  value: IdentityInvitation | undefined,
): value is IdentityInvitation {
  return Boolean(
    value &&
      typeof value.id === "string" &&
      value.id.startsWith("inv_") &&
      typeof value.email === "string" &&
      value.email.includes("@") &&
      typeof value.workspace_id === "string" &&
      value.workspace_id.startsWith("wrk_") &&
      typeof value.token_prefix === "string" &&
      value.token_prefix.startsWith("midn_inv_"),
  );
}

function validScopedInvitation(
  scope: InvitationScope,
  invitation: IdentityInvitation,
): boolean {
  return (
    validInvitationMetadata(invitation) &&
    invitation.workspace_id === scope.workspaceId &&
    (invitation.project_id ?? "") === (scope.projectId ?? "") &&
    (scope.projectId
      ? Boolean(invitation.project_role) &&
        !invitation.workspace_role
      : Boolean(invitation.workspace_role) &&
        !invitation.project_role)
  );
}

function normalizeCursor(value: string | undefined): string | undefined {
  return typeof value === "string" ? value.trim() || undefined : undefined;
}
