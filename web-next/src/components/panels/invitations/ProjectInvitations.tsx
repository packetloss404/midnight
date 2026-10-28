import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { useAuth } from "@/auth/AuthProvider";
import { Empty, Loading } from "@/components/states";
import {
  Button,
  Card,
  Field,
  InlineError,
  TextInput,
} from "@/components/panels/form-kit";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import {
  type IdentityInvitation,
  type IdentityInvitationCredentialResult,
  type InvitationRole,
  type InvitationScope,
  type ProjectInvitationRole,
  type WorkspaceInvitationRole,
  useCreateIdentityInvitation,
  useIdentityInvitations,
  useReissueIdentityInvitation,
  useRevokeIdentityInvitation,
} from "./api";

const PROJECT_MEMBERS_MANAGE = "project.members.manage";
const WORKSPACE_MEMBERS_MANAGE = "workspace.members.manage";
const RELATIVE_EXPIRIES = [
  { days: 1, label: "1 day" },
  { days: 7, label: "7 days" },
  { days: 14, label: "14 days" },
  { days: 30, label: "30 days" },
] as const;

type ScopeKind = "project" | "workspace";

export function ProjectInvitations({ projectId }: { projectId: string }) {
  const auth = useAuth();
  const isHuman = auth.state.status === "human";
  const access = useProjectAccess(projectId, undefined, isHuman);
  const [selectedScope, setSelectedScope] = useState<ScopeKind>("project");

  if (!isHuman) return null;

  if (access.isPending) {
    return (
      <Card
        title="Invitations"
        description="Invite people to this project or its workspace with an exact role."
      >
        <Loading label="Checking invitation access…" />
      </Card>
    );
  }

  if (access.error || !access.data) {
    const forbidden =
      access.error instanceof ApiError && access.error.status === 403;
    return (
      <Card
        title="Invitations"
        description="Invite people to this project or its workspace with an exact role."
      >
        <CapabilityNotice
          title={
            forbidden
              ? "Membership manager access required"
              : "Invitation management unavailable"
          }
          detail={
            forbidden
              ? "Your account cannot inspect or manage invitations for this project."
              : "Midnight could not verify your membership-management permissions. No invitation controls are available until that check succeeds."
          }
          tone={forbidden ? "forbidden" : "unavailable"}
          onRetry={forbidden ? undefined : () => void access.refetch()}
        />
      </Card>
    );
  }

  const canManageProject = access.data.permissions.includes(
    PROJECT_MEMBERS_MANAGE,
  );
  const canManageWorkspace = access.data.permissions.includes(
    WORKSPACE_MEMBERS_MANAGE,
  );
  const scope: InvitationScope = {
    workspaceId: access.data.workspace_id,
    ...(selectedScope === "project" ? { projectId } : {}),
  };
  const canGrantProjectOwner =
    access.data.workspace_role === "admin" ||
    access.data.project_role === "owner";
  const authorized =
    selectedScope === "project" ? canManageProject : canManageWorkspace;

  return (
    <Card
      title="Invitations"
      description="Invite people to this project or its workspace with an exact role. Credentials are delivered manually."
    >
      <div
        role="group"
        aria-label="Invitation scope"
        className="grid grid-cols-2 gap-1 rounded-[var(--r-sm)] border border-line bg-bg-2 p-1"
      >
        <ScopeTab
          active={selectedScope === "project"}
          onClick={() => setSelectedScope("project")}
        >
          This project
        </ScopeTab>
        <ScopeTab
          active={selectedScope === "workspace"}
          onClick={() => setSelectedScope("workspace")}
        >
          Workspace
        </ScopeTab>
      </div>

      <div
        aria-label={`${selectedScope} invitations`}
        className="grid gap-3"
      >
        {authorized ? (
          <InvitationScopeManager
            key={selectedScope}
            scope={scope}
            scopeKind={selectedScope}
            canGrantProjectOwner={canGrantProjectOwner}
          />
        ) : (
          <CapabilityNotice
            title={
              selectedScope === "project"
                ? "Project membership manager access required"
                : "Workspace administrator access required"
            }
            detail={
              selectedScope === "project"
                ? "Project owners and editors can manage direct-project invitations. Editors cannot grant the owner role."
                : "Only workspace administrators can manage workspace-wide invitations."
            }
            tone="forbidden"
          />
        )}
      </div>
    </Card>
  );
}

function InvitationScopeManager({
  scope,
  scopeKind,
  canGrantProjectOwner,
}: {
  scope: InvitationScope;
  scopeKind: ScopeKind;
  canGrantProjectOwner: boolean;
}) {
  const invitations = useIdentityInvitations(scope, true);
  const createInvitation = useCreateIdentityInvitation(scope);
  const reissueInvitation = useReissueIdentityInvitation(scope);
  const revokeInvitation = useRevokeIdentityInvitation(scope);
  const [createOpen, setCreateOpen] = useState(false);
  const [reissueTarget, setReissueTarget] =
    useState<IdentityInvitation | null>(null);
  const [revokeTarget, setRevokeTarget] =
    useState<IdentityInvitation | null>(null);
  const [credential, setCredential] = useState<{
    action: "created" | "reissued";
    result: IdentityInvitationCredentialResult;
  } | null>(null);

  const items = useMemo(
    () =>
      [...(invitations.data?.items ?? [])].sort((left, right) =>
        right.created_at.localeCompare(left.created_at),
      ),
    [invitations.data?.items],
  );
  const initialLoadFailed =
    Boolean(invitations.error) && !invitations.data;
  const controlsAvailable =
    !invitations.isPending && !initialLoadFailed;

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="m-0 text-xs leading-5 text-muted">
          {scopeKind === "project"
            ? "Direct-project access applies only to this project."
            : "Workspace access can apply across projects in this workspace."}
        </p>
        {controlsAvailable ? (
          <Button
            className="w-full sm:w-auto"
            onClick={() => setCreateOpen(true)}
          >
            + New invitation
          </Button>
        ) : null}
      </div>

      {invitations.isPending ? (
        <Loading label="Loading invitations…" />
      ) : initialLoadFailed ? (
        <CapabilityNotice
          title={
            invitations.error instanceof ApiError &&
            invitations.error.status === 403
              ? "Membership manager access required"
              : "Invitations unavailable"
          }
          detail={
            invitations.error instanceof ApiError &&
            invitations.error.status === 403
              ? "Your permission changed while this page was open. Invitation controls are disabled."
              : "Midnight could not load invitation metadata. Existing invitations are unaffected."
          }
          tone={
            invitations.error instanceof ApiError &&
            invitations.error.status === 403
              ? "forbidden"
              : "unavailable"
          }
          onRetry={() => void invitations.refetch()}
        />
      ) : items.length === 0 ? (
        <Empty
          title={`No ${scopeKind} invitations`}
          detail="Create a time-limited credential and deliver it manually through a secure channel."
          icon={<span aria-hidden>✉</span>}
          action={
            <Button onClick={() => setCreateOpen(true)}>
              Create invitation
            </Button>
          }
        />
      ) : (
        <ul className="m-0 grid list-none gap-2 p-0">
          {items.map((invitation) => {
            const status = invitationStatus(invitation);
            return (
              <li
                key={invitation.id}
                className="grid gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3"
              >
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0">
                    <strong className="break-all text-sm text-text">
                      {invitation.email}
                    </strong>
                    <p className="m-0 mt-1 break-all font-mono text-[11px] text-subtle">
                      {invitation.token_prefix}
                    </p>
                  </div>
                  <StatusPill status={status} />
                </div>
                <dl className="m-0 grid grid-cols-2 gap-x-4 gap-y-2 text-[11px] sm:grid-cols-4">
                  <Metadata
                    label="Role"
                    value={
                      invitation.project_role ??
                      invitation.workspace_role ??
                      "Unknown"
                    }
                  />
                  <Metadata
                    label="Created"
                    value={formatDate(invitation.created_at)}
                    dateTime={invitation.created_at}
                  />
                  <Metadata
                    label="Expires"
                    value={formatDate(invitation.expires_at)}
                    dateTime={invitation.expires_at}
                  />
                  <Metadata
                    label="Scope"
                    value={
                      invitation.project_id
                        ? "Direct project"
                        : "Workspace"
                    }
                  />
                </dl>
                {status === "pending" ? (
                  <div className="flex flex-col gap-2 border-t border-line-soft pt-3 sm:flex-row sm:justify-end">
                    <Button
                      variant="secondary"
                      className="w-full sm:w-auto"
                      onClick={() => setReissueTarget(invitation)}
                    >
                      Reissue
                    </Button>
                    <Button
                      variant="danger"
                      className="w-full sm:w-auto"
                      onClick={() => setRevokeTarget(invitation)}
                    >
                      Revoke
                    </Button>
                  </div>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      {invitations.data && invitations.error ? (
        <InlineError error={invitations.error} />
      ) : null}
      {invitations.data && invitations.hasNextPage ? (
        <div className="flex justify-center">
          <Button
            variant="secondary"
            className="w-full sm:w-auto"
            disabled={invitations.isFetchingNextPage}
            onClick={() => void invitations.fetchNextPage()}
          >
            {invitations.isFetchingNextPage
              ? "Loading more…"
              : "Load more invitations"}
          </Button>
        </div>
      ) : null}

      <CreateInvitationDialog
        open={createOpen}
        scopeKind={scopeKind}
        canGrantProjectOwner={canGrantProjectOwner}
        busy={createInvitation.isPending}
        error={createInvitation.error}
        onOpenChange={(open) => {
          setCreateOpen(open);
          if (!open) createInvitation.reset();
        }}
        onCreate={async (email, role, expiresAt) => {
          const result = await createInvitation.mutateAsync({
            email,
            role,
            expiresAt,
          });
          setCreateOpen(false);
          setCredential({ action: "created", result });
          createInvitation.reset();
        }}
      />

      <ReissueInvitationDialog
        invitation={reissueTarget}
        busy={reissueInvitation.isPending}
        error={reissueInvitation.error}
        onOpenChange={(open) => {
          if (!open) {
            setReissueTarget(null);
            reissueInvitation.reset();
          }
        }}
        onReissue={async (expiresAt) => {
          if (!reissueTarget) return;
          const result = await reissueInvitation.mutateAsync({
            invitationId: reissueTarget.id,
            expiresAt,
          });
          setReissueTarget(null);
          setCredential({ action: "reissued", result });
          reissueInvitation.reset();
        }}
      />

      <RevokeInvitationDialog
        invitation={revokeTarget}
        busy={revokeInvitation.isPending}
        error={revokeInvitation.error}
        onOpenChange={(open) => {
          if (!open) {
            setRevokeTarget(null);
            revokeInvitation.reset();
          }
        }}
        onRevoke={async () => {
          if (!revokeTarget) return;
          await revokeInvitation.mutateAsync({ invitationId: revokeTarget.id, expectedVersion: revokeTarget.resource_version });
          setRevokeTarget(null);
          revokeInvitation.reset();
        }}
      />

      <InvitationCredentialDialog
        credential={credential}
        onOpenChange={(open) => {
          if (!open) setCredential(null);
        }}
      />
    </>
  );
}

function CreateInvitationDialog({
  open,
  scopeKind,
  canGrantProjectOwner,
  busy,
  error,
  onOpenChange,
  onCreate,
}: {
  open: boolean;
  scopeKind: ScopeKind;
  canGrantProjectOwner: boolean;
  busy: boolean;
  error: Error | null;
  onOpenChange: (open: boolean) => void;
  onCreate: (
    email: string,
    role: InvitationRole,
    expiresAt: string,
  ) => Promise<void>;
}) {
  const formId = useId();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<InvitationRole>(
    scopeKind === "project" ? "editor" : "member",
  );
  const [expiry, setExpiry] = useState<ExpiryValue>(() => defaultExpiry());
  const [validationError, setValidationError] = useState<string>();

  useEffect(() => {
    if (open) return;
    setEmail("");
    setRole(scopeKind === "project" ? "editor" : "member");
    setExpiry(defaultExpiry());
    setValidationError(undefined);
  }, [open, scopeKind]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setValidationError(undefined);
    const normalizedEmail = email.trim().toLowerCase();
    if (!normalizedEmail || !normalizedEmail.includes("@")) {
      setValidationError("Enter the recipient's email address.");
      return;
    }
    const expiresAt = expiryISO(expiry);
    if (!expiresAt.ok) {
      setValidationError(expiresAt.error);
      return;
    }
    try {
      await onCreate(normalizedEmail, role, expiresAt.value);
    } catch {
      // The mutation owns and renders the bounded API error.
    }
  }

  const projectRoles: ProjectInvitationRole[] = canGrantProjectOwner
    ? ["owner", "editor", "viewer"]
    : ["editor", "viewer"];
  const workspaceRoles: WorkspaceInvitationRole[] = [
    "admin",
    "member",
    "deployer",
    "viewer",
  ];
  const roles = scopeKind === "project" ? projectRoles : workspaceRoles;

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/70 backdrop-blur-[2px]" />
        <Dialog.Content className={dialogClassName("max-w-lg")}>
          <DialogHeader
            title={`New ${scopeKind} invitation`}
            description={`Grant one exact ${scopeKind} role with a time-limited credential. Midnight does not send email.`}
          />
          <DialogClose />
          <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
            <Field label="Recipient email" htmlFor={`${formId}-email`}>
              <TextInput
                id={`${formId}-email`}
                type="email"
                autoComplete="email"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
                placeholder="teammate@example.com"
                autoFocus
                required
              />
            </Field>
            <Field label={`${capitalize(scopeKind)} role`} htmlFor={`${formId}-role`}>
              <select
                id={`${formId}-role`}
                value={role}
                onChange={(event) =>
                  setRole(event.target.value as InvitationRole)
                }
                className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
              >
                {roles.map((candidate) => (
                  <option key={candidate} value={candidate}>
                    {capitalize(candidate)}
                  </option>
                ))}
              </select>
            </Field>
            <ExpiryEditor
              formId={formId}
              value={expiry}
              onChange={setExpiry}
            />
            {validationError ? <InlineError error={validationError} /> : null}
            {error ? <InlineError error={error} /> : null}
            <DialogActions
              busy={busy}
              primaryLabel={busy ? "Creating…" : "Create invitation"}
              onCancel={() => onOpenChange(false)}
            />
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function ReissueInvitationDialog({
  invitation,
  busy,
  error,
  onOpenChange,
  onReissue,
}: {
  invitation: IdentityInvitation | null;
  busy: boolean;
  error: Error | null;
  onOpenChange: (open: boolean) => void;
  onReissue: (expiresAt: string) => Promise<void>;
}) {
  const formId = useId();
  const [expiry, setExpiry] = useState<ExpiryValue>(() => defaultExpiry());
  const [validationError, setValidationError] = useState<string>();

  useEffect(() => {
    if (invitation) return;
    setExpiry(defaultExpiry());
    setValidationError(undefined);
  }, [invitation]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setValidationError(undefined);
    const expiresAt = expiryISO(expiry);
    if (!expiresAt.ok) {
      setValidationError(expiresAt.error);
      return;
    }
    try {
      await onReissue(expiresAt.value);
    } catch {
      // The mutation owns and renders the bounded API error.
    }
  }

  return (
    <Dialog.Root open={Boolean(invitation)} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/70 backdrop-blur-[2px]" />
        <Dialog.Content className={dialogClassName("max-w-lg")}>
          <DialogHeader
            title="Reissue invitation credential?"
            description={`The current credential for ${invitation?.email ?? "this recipient"} will stop working immediately. Email, exact scope, and role are inherited.`}
          />
          <DialogClose />
          <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
            <div
              role="alert"
              className="rounded-[var(--r-sm)] border border-warn/40 bg-warn-soft px-3 py-2 text-xs leading-5 text-muted"
            >
              Reissue is an atomic replacement, not a second active
              credential. You must deliver the replacement manually.
            </div>
            <ExpiryEditor
              formId={formId}
              value={expiry}
              onChange={setExpiry}
            />
            {validationError ? <InlineError error={validationError} /> : null}
            {error ? <InlineError error={error} /> : null}
            <DialogActions
              busy={busy}
              primaryLabel={busy ? "Reissuing…" : "Reissue credential"}
              onCancel={() => onOpenChange(false)}
            />
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function RevokeInvitationDialog({
  invitation,
  busy,
  error,
  onOpenChange,
  onRevoke,
}: {
  invitation: IdentityInvitation | null;
  busy: boolean;
  error: Error | null;
  onOpenChange: (open: boolean) => void;
  onRevoke: () => Promise<void>;
}) {
  return (
    <Dialog.Root open={Boolean(invitation)} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/70 backdrop-blur-[2px]" />
        <Dialog.Content className={dialogClassName("max-w-md")}>
          <DialogHeader
            title="Revoke invitation?"
            description={`${invitation?.email ?? "The recipient"} will no longer be able to accept this credential. Existing membership is not changed. This action cannot be undone.`}
          />
          {error ? <InlineError error={error} /> : null}
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button
              variant="secondary"
              className="w-full sm:w-auto"
              disabled={busy}
              onClick={() => onOpenChange(false)}
            >
              Keep invitation
            </Button>
            <Button
              variant="danger"
              className="w-full sm:w-auto"
              disabled={busy}
              onClick={() => void onRevoke().catch(() => undefined)}
            >
              {busy ? "Revoking…" : "Revoke invitation"}
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function InvitationCredentialDialog({
  credential,
  onOpenChange,
}: {
  credential: {
    action: "created" | "reissued";
    result: IdentityInvitationCredentialResult;
  } | null;
  onOpenChange: (open: boolean) => void;
}) {
  const formId = useId();
  const [copyState, setCopyState] = useState<
    "idle" | "copied" | "error"
  >("idle");

  useEffect(() => {
    if (!credential) setCopyState("idle");
  }, [credential]);

  async function copyCredential() {
    if (!credential) return;
    try {
      await navigator.clipboard.writeText(
        credential.result.invitation_token,
      );
      setCopyState("copied");
    } catch {
      setCopyState("error");
    }
  }

  return (
    <Dialog.Root open={Boolean(credential)} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/70 backdrop-blur-[2px]" />
        <Dialog.Content className={dialogClassName("max-w-lg")}>
          <DialogHeader
            title={`Copy ${credential?.action ?? "new"} invitation credential`}
            description="This secret is available only in this dialog."
          />
          <DialogClose />
          <div className="grid gap-4">
            <div
              role="alert"
              className="rounded-[var(--r-sm)] border border-warn/50 bg-warn-soft px-3 py-3 text-xs leading-5 text-muted"
            >
              <strong className="block text-warn">
                Copy it now. Midnight will never show it again.
              </strong>
              Midnight does not send invitation email. Deliver this credential
              manually through a secure channel. New recipients can create their
              account at <a href="/invite" className="text-accent underline">/invite</a>
              {" "}using this credential. Existing recipients must sign in
              and paste it into <strong className="text-text">Accept invitation</strong>.
              Do not put the credential in a URL or query string.
            </div>
            <p className="m-0 text-xs text-muted">
              Recipient:{" "}
              <strong className="break-all text-text">
                {credential?.result.invitation.email}
              </strong>
            </p>
            <div className="grid gap-2">
              <label
                htmlFor={`${formId}-credential`}
                className="text-xs font-semibold text-text"
              >
                Invitation credential
              </label>
              <textarea
                id={`${formId}-credential`}
                readOnly
                rows={4}
                spellCheck={false}
                value={credential?.result.invitation_token ?? ""}
                className="w-full resize-none break-all rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-2 font-mono text-xs text-text focus:border-accent focus:outline-none"
                onFocus={(event) => event.currentTarget.select()}
              />
              <div className="flex flex-wrap items-center gap-2">
                <Button onClick={() => void copyCredential()}>
                  {copyState === "copied"
                    ? "Copied"
                    : "Copy credential"}
                </Button>
                <span
                  aria-live="polite"
                  className={
                    copyState === "error"
                      ? "text-xs text-danger"
                      : "text-xs text-muted"
                  }
                >
                  {copyState === "copied"
                    ? "Copied to clipboard."
                    : copyState === "error"
                      ? "Clipboard access failed. Select and copy it manually."
                      : ""}
                </span>
              </div>
            </div>
            <Button
              variant="secondary"
              className="w-full"
              onClick={() => onOpenChange(false)}
            >
              I have saved the credential
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

type ExpiryValue = {
  mode: "relative" | "absolute";
  days: string;
  absolute: string;
};

function ExpiryEditor({
  formId,
  value,
  onChange,
}: {
  formId: string;
  value: ExpiryValue;
  onChange: (value: ExpiryValue) => void;
}) {
  return (
    <div className="grid gap-3">
      <fieldset className="grid gap-2 border-0 p-0">
        <legend className="text-xs font-semibold text-text">Expiry</legend>
        <div className="grid grid-cols-2 gap-2">
          <label className="flex min-h-10 items-center gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs text-text">
            <input
              type="radio"
              name={`${formId}-expiry-mode`}
              checked={value.mode === "relative"}
              onChange={() => onChange({ ...value, mode: "relative" })}
            />
            Relative
          </label>
          <label className="flex min-h-10 items-center gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs text-text">
            <input
              type="radio"
              name={`${formId}-expiry-mode`}
              checked={value.mode === "absolute"}
              onChange={() => onChange({ ...value, mode: "absolute" })}
            />
            Date and time
          </label>
        </div>
      </fieldset>
      {value.mode === "relative" ? (
        <Field
          label="Lifetime"
          htmlFor={`${formId}-relative-expiry`}
          hint="Invitation credentials must expire within 30 days."
        >
          <select
            id={`${formId}-relative-expiry`}
            value={value.days}
            onChange={(event) =>
              onChange({ ...value, days: event.target.value })
            }
            className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
          >
            {RELATIVE_EXPIRIES.map((option) => (
              <option key={option.days} value={option.days}>
                {option.label}
              </option>
            ))}
          </select>
        </Field>
      ) : (
        <Field
          label="Expires at"
          htmlFor={`${formId}-absolute-expiry`}
          hint="Uses your browser's local time zone; choose between five minutes and 30 days from now."
        >
          <TextInput
            id={`${formId}-absolute-expiry`}
            type="datetime-local"
            value={value.absolute}
            min={localDateTimeValue(
              new Date(Date.now() + 5 * 60 * 1000),
            )}
            max={localDateTimeValue(
              new Date(Date.now() + 30 * 24 * 60 * 60 * 1000),
            )}
            onChange={(event) =>
              onChange({ ...value, absolute: event.target.value })
            }
            required
          />
        </Field>
      )}
    </div>
  );
}

function ScopeTab({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={
        "min-h-9 rounded-[var(--r-sm)] px-3 py-1.5 text-xs font-semibold transition-colors " +
        (active
          ? "bg-surface-2 text-accent shadow-sm"
          : "text-muted hover:bg-surface-2 hover:text-text")
      }
    >
      {children}
    </button>
  );
}

function CapabilityNotice({
  title,
  detail,
  tone,
  onRetry,
}: {
  title: string;
  detail: string;
  tone: "forbidden" | "unavailable";
  onRetry?: () => void;
}) {
  return (
    <div
      role={tone === "unavailable" ? "alert" : "status"}
      className={
        tone === "forbidden"
          ? "grid gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-4 py-4 text-sm"
          : "grid gap-2 rounded-[var(--r-sm)] border border-warn/40 bg-warn-soft px-4 py-4 text-sm"
      }
    >
      <strong className={tone === "forbidden" ? "text-text" : "text-warn"}>
        {title}
      </strong>
      <span className="text-xs leading-5 text-muted">{detail}</span>
      {onRetry ? (
        <div>
          <Button variant="secondary" onClick={onRetry}>
            Retry
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function StatusPill({
  status,
}: {
  status: "pending" | "accepted" | "expired" | "revoked" | "replaced";
}) {
  const tone =
    status === "pending"
      ? "border-accent/50 bg-accent-soft text-accent"
      : status === "accepted"
        ? "border-info/50 bg-info-soft text-info"
        : status === "expired"
          ? "border-warn/50 bg-warn-soft text-warn"
          : "border-line-strong bg-surface text-subtle";
  return (
    <span
      className={`rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider ${tone}`}
    >
      {status}
    </span>
  );
}

function Metadata({
  label,
  value,
  dateTime,
}: {
  label: string;
  value: string;
  dateTime?: string;
}) {
  return (
    <div className="min-w-0">
      <dt className="uppercase tracking-wider text-subtle">{label}</dt>
      <dd className="m-0 mt-0.5 truncate capitalize text-text">
        {dateTime ? <time dateTime={dateTime}>{value}</time> : value}
      </dd>
    </div>
  );
}

function DialogHeader({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <header className="grid gap-1 pr-8">
      <Dialog.Title className="m-0 text-base font-semibold text-text">
        {title}
      </Dialog.Title>
      <Dialog.Description className="m-0 text-xs leading-5 text-muted">
        {description}
      </Dialog.Description>
    </header>
  );
}

function DialogClose() {
  return (
    <Dialog.Close
      className="absolute right-3 top-3 grid h-9 w-9 place-items-center rounded-[var(--r-sm)] border border-line bg-surface-2 text-lg text-muted hover:bg-surface-3 hover:text-text"
      aria-label="Close"
    >
      ×
    </Dialog.Close>
  );
}

function DialogActions({
  busy,
  primaryLabel,
  onCancel,
}: {
  busy: boolean;
  primaryLabel: string;
  onCancel: () => void;
}) {
  return (
    <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
      <Button
        variant="secondary"
        className="w-full sm:w-auto"
        disabled={busy}
        onClick={onCancel}
      >
        Cancel
      </Button>
      <Button
        type="submit"
        className="w-full sm:w-auto"
        disabled={busy}
      >
        {primaryLabel}
      </Button>
    </div>
  );
}

function dialogClassName(maxWidth: "max-w-md" | "max-w-lg"): string {
  return `fixed left-1/2 top-1/2 z-50 grid max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] ${maxWidth} -translate-x-1/2 -translate-y-1/2 gap-4 overflow-y-auto rounded-lg border border-line-strong bg-surface p-4 shadow-2xl sm:p-5`;
}

function invitationStatus(
  invitation: IdentityInvitation,
): "pending" | "accepted" | "expired" | "revoked" | "replaced" {
  if (invitation.accepted_at) return "accepted";
  if (invitation.revoked_at) {
    return invitation.revoked_reason === "reissued" ? "replaced" : "revoked";
  }
  if (new Date(invitation.expires_at).getTime() <= Date.now()) {
    return "expired";
  }
  return "pending";
}

function formatDate(value: string): string {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "Unknown";
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

function defaultExpiry(): ExpiryValue {
  return {
    mode: "relative",
    days: "7",
    absolute: localDateTimeValue(
      new Date(Date.now() + 7 * 24 * 60 * 60 * 1000),
    ),
  };
}

function expiryISO(
  value: ExpiryValue,
): { ok: true; value: string } | { ok: false; error: string } {
  const expiry =
    value.mode === "relative"
      ? new Date(
          Date.now() + Number(value.days) * 24 * 60 * 60 * 1000,
        )
      : new Date(value.absolute);
  const lifetime = expiry.getTime() - Date.now();
  if (!Number.isFinite(expiry.getTime()) || lifetime < 5 * 60 * 1000) {
    return {
      ok: false,
      error: "Choose an expiry at least five minutes in the future.",
    };
  }
  if (lifetime > 30 * 24 * 60 * 60 * 1000) {
    return {
      ok: false,
      error: "Invitation credentials cannot last longer than 30 days.",
    };
  }
  return { ok: true, value: expiry.toISOString() };
}

function localDateTimeValue(date: Date): string {
  const offset = date.getTimezoneOffset() * 60 * 1000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
