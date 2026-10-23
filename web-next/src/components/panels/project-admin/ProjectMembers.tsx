import { useEffect, useState } from "react";
import { useAuth } from "@/auth/AuthProvider";
import { ErrorState, Loading } from "@/components/states";
import { Button, Card, InlineError } from "@/components/panels/form-kit";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import {
  useProjectMembers,
  useRemoveProjectMember,
  useRemoveWorkspaceMember,
  useUpdateProjectMember,
  useUpdateWorkspaceMember,
  useWorkspaceMembers,
  type ProjectMember,
  type ProjectMemberRole,
  type WorkspaceMember,
  type WorkspaceMemberRole,
} from "./api";

const PROJECT_ROLES: ProjectMemberRole[] = ["owner", "editor", "viewer"];
const WORKSPACE_ROLES: WorkspaceMemberRole[] = [
  "admin",
  "member",
  "deployer",
  "viewer",
];

export function ProjectMembers({ projectId }: { projectId: string }) {
  const auth = useAuth();
  const human = auth.state.status === "human";
  const access = useProjectAccess(projectId, undefined, human);
  const [scope, setScope] = useState<"project" | "workspace">("project");
  const exact =
    access.data?.principal_kind === "human" &&
    access.data.project_id === projectId &&
    access.data.environment_id === undefined;
  const canProject =
    exact && access.data?.permissions.includes("project.members.manage") === true;
  const canWorkspace =
    exact &&
    access.data?.permissions.includes("workspace.members.manage") === true &&
    Boolean(access.data.workspace_id);

  if (!human) return null;

  if (access.isPending) {
    return (
      <Card title="Members" description="Current project and workspace access.">
        <Loading label="Checking membership access…" />
      </Card>
    );
  }
  if (access.error || !exact) {
    return (
      <Card title="Members" description="Current project and workspace access.">
        <ErrorState
          title="Membership access unavailable"
          error={
            access.error ??
            new Error("Midnight could not verify exact project membership access.")
          }
          onRetry={() => void access.refetch()}
        />
      </Card>
    );
  }
  if (!canProject && !canWorkspace) return null;

  const activeScope = scope === "workspace" && !canWorkspace ? "project" : scope;
  return (
    <Card
      title="Members"
      description="Edit or remove current access with server-enforced conditional writes."
    >
      {canProject && canWorkspace ? (
        <div role="tablist" aria-label="Membership scope" className="flex gap-2">
          <ScopeTab active={activeScope === "project"} onClick={() => setScope("project")}>
            Project
          </ScopeTab>
          <ScopeTab active={activeScope === "workspace"} onClick={() => setScope("workspace")}>
            Workspace
          </ScopeTab>
        </div>
      ) : null}
      {activeScope === "workspace" && canWorkspace ? (
        <WorkspaceMemberList
          workspaceId={access.data!.workspace_id}
          currentUserId={auth.state.status === "human" ? auth.state.me.principal.user_id : ""}
        />
      ) : canProject ? (
        <ProjectMemberList
          projectId={projectId}
          currentUserId={auth.state.status === "human" ? auth.state.me.principal.user_id : ""}
          canGrantOwner={
            access.data?.workspace_role === "admin" ||
            access.data?.project_role === "owner"
          }
        />
      ) : null}
    </Card>
  );
}

function ProjectMemberList({
  projectId,
  currentUserId,
  canGrantOwner,
}: {
  projectId: string;
  currentUserId: string;
  canGrantOwner: boolean;
}) {
  const query = useProjectMembers(projectId, true);
  const update = useUpdateProjectMember(projectId);
  const remove = useRemoveProjectMember(projectId);

  if (query.isPending) return <Loading label="Loading project members…" />;
  if (query.error) {
    return <ErrorState title="Failed to load project members" error={query.error} onRetry={() => void query.refetch()} />;
  }
  return (
    <MemberList
      members={query.data ?? []}
      currentUserId={currentUserId}
      roles={canGrantOwner ? PROJECT_ROLES : PROJECT_ROLES.slice(1)}
      busy={update.isPending || remove.isPending}
      error={update.error ?? remove.error}
      onReload={() => void query.refetch()}
      onUpdate={(member, role) => update.mutateAsync({ member, role })}
      onRemove={(member) => remove.mutateAsync(member)}
    />
  );
}

function WorkspaceMemberList({
  workspaceId,
  currentUserId,
}: {
  workspaceId: string;
  currentUserId: string;
}) {
  const query = useWorkspaceMembers(workspaceId, true);
  const update = useUpdateWorkspaceMember(workspaceId);
  const remove = useRemoveWorkspaceMember(workspaceId);

  if (query.isPending) return <Loading label="Loading workspace members…" />;
  if (query.error) {
    return <ErrorState title="Failed to load workspace members" error={query.error} onRetry={() => void query.refetch()} />;
  }
  return (
    <MemberList
      members={query.data ?? []}
      currentUserId={currentUserId}
      roles={WORKSPACE_ROLES}
      busy={update.isPending || remove.isPending}
      error={update.error ?? remove.error}
      onReload={() => void query.refetch()}
      onUpdate={(member, role) => update.mutateAsync({ member, role })}
      onRemove={(member) => remove.mutateAsync(member)}
    />
  );
}

function MemberList<T extends ProjectMember | WorkspaceMember>({
  members,
  currentUserId,
  roles,
  busy,
  error,
  onReload,
  onUpdate,
  onRemove,
}: {
  members: T[];
  currentUserId: string;
  roles: readonly T["role"][];
  busy: boolean;
  error: Error | null;
  onReload: () => void;
  onUpdate: (member: T, role: T["role"]) => Promise<unknown>;
  onRemove: (member: T) => Promise<unknown>;
}) {
  return (
    <div className="grid gap-2">
      {members.length === 0 ? <p className="m-0 text-xs text-muted">No direct members.</p> : null}
      {members.map((member) => (
        <MemberRow
          key={member.user.id}
          member={member}
          current={member.user.id === currentUserId}
          roles={roles}
          busy={busy}
          onUpdate={onUpdate}
          onRemove={onRemove}
        />
      ))}
      {error ? (
        <div className="grid gap-2">
          <InlineError error={error} />
          <div><Button variant="secondary" onClick={onReload}>Reload latest members</Button></div>
        </div>
      ) : null}
    </div>
  );
}

function MemberRow<T extends ProjectMember | WorkspaceMember>({
  member,
  current,
  roles,
  busy,
  onUpdate,
  onRemove,
}: {
  member: T;
  current: boolean;
  roles: readonly T["role"][];
  busy: boolean;
  onUpdate: (member: T, role: T["role"]) => Promise<unknown>;
  onRemove: (member: T) => Promise<unknown>;
}) {
  const [role, setRole] = useState<T["role"]>(member.role);
  const [confirming, setConfirming] = useState(false);
  useEffect(() => {
    setRole(member.role);
    setConfirming(false);
  }, [member.resource_version, member.role]);
  const availableRoles = roles.includes(member.role) ? roles : [member.role, ...roles];
  return (
    <div className="grid gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto]">
      <div className="min-w-0">
        <p className="m-0 truncate text-sm font-semibold text-text">
          {member.user.display_name || member.user.email} {current ? <span className="text-[10px] text-subtle">(you)</span> : null}
        </p>
        <p className="m-0 truncate font-mono text-[11px] text-muted">{member.user.email}</p>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor={`member-role-${member.user.id}`}>Role for {member.user.email}</label>
        <select
          id={`member-role-${member.user.id}`}
          value={role}
          disabled={busy}
          onChange={(event) => setRole(event.target.value as T["role"])}
          className="rounded-[var(--r-sm)] border border-line bg-surface px-2 py-2 text-xs text-text"
        >
          {availableRoles.map((candidate) => <option key={candidate} value={candidate}>{capitalize(candidate)}</option>)}
        </select>
        <Button variant="secondary" disabled={busy || role === member.role} onClick={() => void onUpdate(member, role).catch(() => undefined)}>
          Save role
        </Button>
        {confirming ? (
          <>
            <Button variant="danger" disabled={busy} onClick={() => void onRemove(member).catch(() => undefined)}>Confirm remove</Button>
            <Button variant="secondary" disabled={busy} onClick={() => setConfirming(false)}>Cancel</Button>
          </>
        ) : (
          <Button variant="danger" disabled={busy} onClick={() => setConfirming(true)}>Remove</Button>
        )}
      </div>
    </div>
  );
}

function ScopeTab({ active, onClick, children }: { active: boolean; onClick: () => void; children: string }) {
  return (
    <button role="tab" aria-selected={active} onClick={onClick} className={`rounded-[var(--r-sm)] px-3 py-1.5 text-xs font-semibold ${active ? "bg-accent-soft text-accent" : "bg-surface-2 text-muted"}`}>
      {children}
    </button>
  );
}

function capitalize(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
