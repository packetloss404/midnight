import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  API_PREFIX,
  ApiError,
  apiFetch,
  authHeader,
  type ApiErrorBody,
} from "@/api/client";

export type MemberUser = {
  id: string;
  email: string;
  display_name: string;
  instance_admin: boolean;
  mfa_enabled: boolean;
};

export type ProjectMemberRole = "owner" | "editor" | "viewer";
export type WorkspaceMemberRole = "admin" | "member" | "deployer" | "viewer";

export type ProjectMember = {
  project_id: string;
  user: MemberUser;
  role: ProjectMemberRole;
  invited_by?: string;
  invited_at: string;
  accepted_at?: string;
  created_at: string;
  updated_at: string;
  resource_version: number;
};

export type WorkspaceMember = {
  workspace_id: string;
  user: MemberUser;
  role: WorkspaceMemberRole;
  invited_by?: string;
  invited_at: string;
  accepted_at?: string;
  created_at: string;
  updated_at: string;
  resource_version: number;
};

export const projectMemberKeys = {
  project: (projectId: string) => ["project-members", projectId] as const,
  workspace: (workspaceId: string) => ["workspace-members", workspaceId] as const,
};

type AuthoritativeMember<T> = { member: T; etag: string };

async function fetchAuthoritativeMember<T>(
  path: string,
): Promise<AuthoritativeMember<T>> {
  const response = await fetch(`${API_PREFIX}${path}`, {
    headers: { Accept: "application/json", ...authHeader("human") },
  });
  const text = await response.text();
  const parsed = text ? safeJSON(text) : undefined;
  if (!response.ok) {
    throw new ApiError(
      response.status,
      typeof parsed === "object" && parsed !== null
        ? (parsed as ApiErrorBody)
        : text,
      response.headers.get("x-request-id") ?? undefined,
      response.headers.get("retry-after") ?? undefined,
    );
  }
  const etag = response.headers.get("etag")?.trim();
  if (!etag || !/^"[\x21\x23-\x7e\x80-\xff]*"$/.test(etag) || !parsed) {
    throw new Error("Midnight returned membership metadata without a strong ETag.");
  }
  return { member: parsed as T, etag };
}

function safeJSON(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}

function requireUnchangedMemberVersion(
  displayedVersion: number,
  currentVersion: number,
) {
  if (currentVersion !== displayedVersion) {
    throw new Error(
      "Membership changed since this list loaded. Reload the latest members before retrying.",
    );
  }
}

function projectMemberPath(projectId: string, userId?: string): string {
  const base = `/projects/${encodeURIComponent(projectId)}/members`;
  return userId ? `${base}/${encodeURIComponent(userId)}` : base;
}

function workspaceMemberPath(workspaceId: string, userId?: string): string {
  const base = `/workspaces/${encodeURIComponent(workspaceId)}/members`;
  return userId ? `${base}/${encodeURIComponent(userId)}` : base;
}

export function listProjectMembers(
  projectId: string,
  signal?: AbortSignal,
): Promise<ProjectMember[]> {
  return apiFetch(projectMemberPath(projectId), { auth: "human", signal });
}

export function listWorkspaceMembers(
  workspaceId: string,
  signal?: AbortSignal,
): Promise<WorkspaceMember[]> {
  return apiFetch(workspaceMemberPath(workspaceId), { auth: "human", signal });
}

export function updateProjectMember(
  projectId: string,
  member: Pick<ProjectMember, "user" | "resource_version">,
  role: ProjectMemberRole,
): Promise<ProjectMember> {
  const path = projectMemberPath(projectId, member.user.id);
  return fetchAuthoritativeMember<ProjectMember>(path).then((current) => {
    requireUnchangedMemberVersion(
      member.resource_version,
      current.member.resource_version,
    );
    return apiFetch<ProjectMember>(path, {
      auth: "human",
      method: "PUT",
      headers: { "If-Match": current.etag },
      body: { role },
    });
  });
}

export function removeProjectMember(
  projectId: string,
  member: Pick<ProjectMember, "user" | "resource_version">,
): Promise<void> {
  const path = projectMemberPath(projectId, member.user.id);
  return fetchAuthoritativeMember<ProjectMember>(path).then((current) => {
    requireUnchangedMemberVersion(
      member.resource_version,
      current.member.resource_version,
    );
    return apiFetch<void>(path, {
      auth: "human",
      method: "DELETE",
      headers: { "If-Match": current.etag },
    });
  });
}

export function updateWorkspaceMember(
  workspaceId: string,
  member: Pick<WorkspaceMember, "user" | "resource_version">,
  role: WorkspaceMemberRole,
): Promise<WorkspaceMember> {
  const path = workspaceMemberPath(workspaceId, member.user.id);
  return fetchAuthoritativeMember<WorkspaceMember>(path).then((current) => {
    requireUnchangedMemberVersion(
      member.resource_version,
      current.member.resource_version,
    );
    return apiFetch<WorkspaceMember>(path, {
      auth: "human",
      method: "PUT",
      headers: { "If-Match": current.etag },
      body: { role },
    });
  });
}

export function removeWorkspaceMember(
  workspaceId: string,
  member: Pick<WorkspaceMember, "user" | "resource_version">,
): Promise<void> {
  const path = workspaceMemberPath(workspaceId, member.user.id);
  return fetchAuthoritativeMember<WorkspaceMember>(path).then((current) => {
    requireUnchangedMemberVersion(
      member.resource_version,
      current.member.resource_version,
    );
    return apiFetch<void>(path, {
      auth: "human",
      method: "DELETE",
      headers: { "If-Match": current.etag },
    });
  });
}

export function useProjectMembers(projectId: string, enabled: boolean) {
  return useQuery<ProjectMember[], Error>({
    queryKey: projectMemberKeys.project(projectId),
    queryFn: ({ signal }) => listProjectMembers(projectId, signal),
    enabled: enabled && Boolean(projectId),
  });
}

export function useWorkspaceMembers(workspaceId: string, enabled: boolean) {
  return useQuery<WorkspaceMember[], Error>({
    queryKey: projectMemberKeys.workspace(workspaceId),
    queryFn: ({ signal }) => listWorkspaceMembers(workspaceId, signal),
    enabled: enabled && Boolean(workspaceId),
  });
}

export function useUpdateProjectMember(projectId: string) {
  const client = useQueryClient();
  return useMutation<
    ProjectMember,
    Error,
    { member: ProjectMember; role: ProjectMemberRole }
  >({
    mutationFn: ({ member, role }) =>
      updateProjectMember(projectId, member, role),
    onSuccess: () =>
      client.invalidateQueries({
        queryKey: projectMemberKeys.project(projectId),
      }),
  });
}

export function useRemoveProjectMember(projectId: string) {
  const client = useQueryClient();
  return useMutation<void, Error, ProjectMember>({
    mutationFn: (member) => removeProjectMember(projectId, member),
    onSuccess: () =>
      client.invalidateQueries({
        queryKey: projectMemberKeys.project(projectId),
      }),
  });
}

export function useUpdateWorkspaceMember(workspaceId: string) {
  const client = useQueryClient();
  return useMutation<
    WorkspaceMember,
    Error,
    { member: WorkspaceMember; role: WorkspaceMemberRole }
  >({
    mutationFn: ({ member, role }) =>
      updateWorkspaceMember(workspaceId, member, role),
    onSuccess: () =>
      client.invalidateQueries({
        queryKey: projectMemberKeys.workspace(workspaceId),
      }),
  });
}

export function useRemoveWorkspaceMember(workspaceId: string) {
  const client = useQueryClient();
  return useMutation<void, Error, WorkspaceMember>({
    mutationFn: (member) => removeWorkspaceMember(workspaceId, member),
    onSuccess: () =>
      client.invalidateQueries({
        queryKey: projectMemberKeys.workspace(workspaceId),
      }),
  });
}
