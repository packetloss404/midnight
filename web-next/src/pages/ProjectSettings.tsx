// Project settings (`/projects/:projectId/settings`). Tenant-scoped environment
// listing plus ordinary creation and metadata editing and a shared/environment
// variables entry point.
//
// Human environment changes are admitted only after exact access responses
// grant environment.write. Protected-state changes, duplicate, copy, and delete
// environment controls remain outside this route boundary.
//
// Deleting a single service is exposed on the service's own settings panel
// (Danger zone). Deleting a project or an environment has no API yet, so this
// page says so plainly rather than showing a control that cannot work.

import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { queryKeys, useEnvironments, useProject } from "@/api/hooks";
import { useAuth } from "@/auth/AuthProvider";
import { Loading, Empty, ErrorState } from "@/components/states";
import { Field, TextInput, Button, InlineError, Card } from "@/components/panels/form-kit";
import {
  createEnvironment,
  newIdempotencyKey,
  type EnvironmentTier,
} from "@/components/panels/create/api";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import { ProjectAutomationTokens } from "@/components/panels/automation-tokens/ProjectAutomationTokens";
import { ProjectInvitations } from "@/components/panels/invitations/ProjectInvitations";
import { EnvironmentMetadataEditor } from "@/components/panels/environments/EnvironmentMetadataEditor";
import { ProjectMembers } from "@/components/panels/project-admin/ProjectMembers";
import { ProjectWebhookDeliveries } from "@/components/panels/webhooks/ProjectWebhookDeliveries";
import { GithubWorkspaceSetup } from "@/components/panels/github/WorkspaceSetup";
import { CommunityTemplateForks } from "@/components/panels/create/CommunityTemplateForks";

type Props = {
  projectId: string;
  environmentId?: string;
};

const TIERS: EnvironmentTier[] = ["PROD", "STAGING", "PREVIEW", "DEV"];
const ENVIRONMENT_WRITE_PERMISSION = "environment.write";

type EnvironmentCreateIntent = {
  projectId: string;
  name: string;
  tier: EnvironmentTier;
  idempotencyKey: string;
};

export function ProjectSettings({ projectId, environmentId }: Props) {
  const navigate = useNavigate();
  const project = useProject(projectId);

  return (
    <section className="mx-auto flex w-full max-w-3xl flex-col gap-5 p-[var(--main-gutter)]">
      <header>
        <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">Configure</p>
        <h1 className="m-0 mt-1 text-xl font-semibold tracking-tight">
          {project.data?.display_name || project.data?.slug || "Project settings"}
        </h1>
        <p className="m-0 mt-1 font-mono text-[11px] text-subtle">{projectId}</p>
      </header>

      <EnvironmentsSection projectId={projectId} />

      <ProjectAutomationTokens projectId={projectId} />

      <ProjectInvitations projectId={projectId} />

      <ProjectMembers projectId={projectId} />

      <GithubWorkspaceSetup projectId={projectId} />

      <CommunityTemplateForks projectId={projectId} environmentId={environmentId} />

      <Card
        title="Variables"
        description="Encrypted shared and per-environment variable metadata."
        action={
          <Button
            onClick={() =>
              navigate({
                to: "/projects/$projectId",
                params: { projectId },
                search: environmentId ? { env: environmentId } : {},
              } as never)
            }
          >
            Open service canvas
          </Button>
        }
      >
        <p className="m-0 text-xs text-muted">
          Variables are edited per service on the canvas (Variables tab), encrypted at rest, and
          resolved by build and deploy workers. Saved edits affect the next deployment and do not
          rewrite containers that are already running. Reference syntax such as{" "}
          <code className="rounded bg-surface-2 px-1 py-0.5 font-mono text-[11px]">
            ${"{{Service.VAR}}"}
          </code>{" "}
          is resolved from the immutable variable versions captured for that deployment.
        </p>
      </Card>

      <Card
        title="Integrations"
        description="Repository links, custom domains, storage, and deployment settings are scoped to individual services."
        action={
          <Button
            variant="secondary"
            onClick={() =>
              navigate({
                to: "/projects/$projectId",
                params: { projectId },
                search: environmentId ? { env: environmentId } : {},
              } as never)
            }
          >
            Manage service integrations
          </Button>
        }
      >
        <p className="m-0 text-xs text-muted">
          Select a service on the canvas to manage only the integrations its
          current API exposes. Deleting a single service also lives there, under
          Settings &rarr; Danger zone; the service has to be stopped first.
          Deleting a whole project, and project-wide feature flags and transfer
          controls, are not available in Midnight yet.
        </p>
      </Card>

      <ProjectWebhookDeliveries projectId={projectId} />
    </section>
  );
}

// ── Environments ──────────────────────────────────────────────────

function EnvironmentsSection({ projectId }: { projectId: string }) {
  const queryClient = useQueryClient();
  const auth = useAuth();
  const humanSession = auth.state.status === "human";
  const access = useProjectAccess(projectId, undefined, humanSession);
  const envs = useEnvironments(projectId);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [tier, setTier] = useState<EnvironmentTier>("STAGING");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [createIntent, setCreateIntent] =
    useState<EnvironmentCreateIntent | null>(null);
  const [editingEnvironmentId, setEditingEnvironmentId] = useState<string>();
  const exactProjectAccess =
    access.data?.principal_kind === "human" &&
    access.data.project_id === projectId &&
    access.data.environment_id === undefined;
  const canCreateEnvironment =
    humanSession &&
    !access.isPending &&
    !access.error &&
    exactProjectAccess &&
    access.data?.permissions.includes(ENVIRONMENT_WRITE_PERMISSION) === true;

  function openCreate() {
    setError(null);
    setCreateIntent(null);
    setEditingEnvironmentId(undefined);
    setAdding(true);
  }

  function cancelCreate() {
    setAdding(false);
    setName("");
    setError(null);
    setCreateIntent(null);
  }

  async function submit() {
    setError(null);
    if (!canCreateEnvironment) {
      setError("The current project access does not grant environment creation.");
      return;
    }
    if (!name.trim()) {
      setError("An environment name is required.");
      return;
    }
    const normalizedName = name.trim();
    const intent =
      createIntent?.projectId === projectId &&
      createIntent.name === normalizedName &&
      createIntent.tier === tier
        ? createIntent
        : {
            projectId,
            name: normalizedName,
            tier,
            idempotencyKey: newIdempotencyKey(),
          };
    if (intent !== createIntent) {
      setCreateIntent(intent);
    }
    setBusy(true);
    try {
      await createEnvironment(
        { projectId, name: normalizedName, tier },
        intent.idempotencyKey,
      );
      await queryClient.invalidateQueries({
        queryKey: queryKeys.environments(projectId),
      });
      setName("");
      setAdding(false);
      setCreateIntent(null);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card
      title="Environments"
      description="Isolated service scopes. Names, slugs, and tiers are metadata; promotion and copying are separate workflows."
      action={
        canCreateEnvironment && !adding ? (
          <Button onClick={openCreate}>+ New environment</Button>
        ) : canCreateEnvironment && adding ? (
          <Button variant="secondary" onClick={cancelCreate} disabled={busy}>
            Cancel
          </Button>
        ) : undefined
      }
    >
      {humanSession && access.isPending ? (
        <Loading label="Checking environment access…" />
      ) : humanSession && (access.error || !access.data || !exactProjectAccess) ? (
        <ErrorState
          title="Environment changes unavailable"
          error={
            access.error ??
            new Error("Midnight could not verify exact project access.")
          }
          onRetry={() => void access.refetch()}
        />
      ) : humanSession && !canCreateEnvironment ? (
        <div
          role="note"
          className="rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3 text-xs text-muted"
        >
          Your current project role can view environments but does not grant
          environment creation or metadata editing.
        </div>
      ) : null}

      {envs.isPending ? (
        <Loading label="Loading environments…" />
      ) : envs.error ? (
        <ErrorState title="Failed to load environments" error={envs.error} onRetry={() => envs.refetch()} />
      ) : (envs.data ?? []).length === 0 && !adding && canCreateEnvironment ? (
        <Empty
          title="No environments yet"
          detail="Create an environment to give your services somewhere to run."
          icon={<span aria-hidden>🌱</span>}
          action={<Button onClick={openCreate}>+ New environment</Button>}
        />
      ) : (envs.data ?? []).length === 0 && !adding ? (
        <div className="rounded-lg border border-dashed border-line bg-bg-2 px-6 py-8 text-center">
          <strong className="text-text">No environments yet</strong>
          <p className="m-0 mt-2 text-sm text-muted">
            No environment is visible in this project.
          </p>
        </div>
      ) : (
        <ul className="m-0 grid list-none gap-2 p-0">
          {(envs.data ?? []).map((e) => (
            <li
              key={e.id}
              className="grid gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-4 py-3 text-sm"
            >
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <span className="font-mono text-text">{e.name || e.slug}</span>
                  <span className="ml-2 font-mono text-[10px] text-subtle">
                    {e.slug}
                  </span>
                  {e.is_protected ? (
                    <span className="ml-2 text-[10px] uppercase tracking-wider text-subtle">protected</span>
                  ) : null}
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <span
                    className={
                      "rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider " +
                      (e.tier?.toLowerCase() === "production"
                        ? "border-accent bg-accent-soft text-accent"
                        : "border-line bg-surface text-subtle")
                    }
                  >
                    {e.tier}
                  </span>
                  {canCreateEnvironment &&
                  editingEnvironmentId !== e.id ? (
                    <Button
                      variant="secondary"
                      onClick={() => {
                        cancelCreate();
                        setEditingEnvironmentId(e.id);
                      }}
                    >
                      Edit {e.name || e.slug}
                    </Button>
                  ) : null}
                </div>
              </div>
              {editingEnvironmentId === e.id ? (
                <EnvironmentMetadataEditor
                  projectId={projectId}
                  environment={e}
                  onCancel={() => setEditingEnvironmentId(undefined)}
                  onReloadLatest={async () => {
                    const latest = await envs.refetch();
                    if (latest.error) {
                      throw latest.error;
                    }
                    return latest.data?.find(
                      (environment) => environment.id === e.id,
                    );
                  }}
                  onSaved={async () => {
                    await queryClient.invalidateQueries({
                      queryKey: queryKeys.environments(projectId),
                    });
                  }}
                />
              ) : null}
            </li>
          ))}
        </ul>
      )}

      {adding && canCreateEnvironment ? (
        <form
          className="mt-1 grid gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <Field label="Name">
            <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="staging" autoFocus />
          </Field>
          <Field label="Tier">
            <select
              value={tier}
              onChange={(e) => setTier(e.target.value as EnvironmentTier)}
              className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
            >
              {TIERS.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </Field>
          <div>
            <Button type="submit" disabled={busy}>
              {busy ? "Creating…" : "Create environment"}
            </Button>
          </div>
          {error ? <InlineError error={error} /> : null}
        </form>
      ) : null}
    </Card>
  );
}
