// New-project flow (`/new`). A Railway-style SourcePicker with four starting
// points — GitHub repo · Template · Empty · Database — each wired to real
// create mutations (see components/panels/create/api.ts and
// components/panels/github/api.ts). On success we route to the new project's
// canvas, opening the created service (and its variables tab when the operator
// chose "configure first").

import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useAuth } from "@/auth/AuthProvider";
import { useIdentityWorkspaces } from "@/api/hooks";
import { Loading, Empty, ErrorState } from "@/components/states";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { Field, TextInput, Button, InlineError, Card } from "@/components/panels/form-kit";
import { RepoPicker, type RepoSelection } from "@/components/panels/github/RepoPicker";
import {
  linkServiceGithub,
  putHumanServiceGithubLink,
} from "@/components/panels/github/api";
import {
  createProject,
  createHumanProject,
  createHumanService,
  createService,
  createCommunityTemplateFork,
  deployTemplate,
  discoverCommunityTemplate,
  slugify,
  useTemplate,
  useTemplates,
  useTemplateProvisionRun,
  updateCommunityTemplateFork,
  type CommunityManifest,
  type CommunityTemplateDiscovery,
  type CommunityTemplateFork,
  type TemplateDeployResult,
  type TemplateSummary,
} from "@/components/panels/create/api";
import { describeCapability, type CapabilityState } from "@/lib/capabilities";

type SourceKind = "github" | "template" | "empty" | "database";

export const NEW_PROJECT_SOURCES: {
  kind: SourceKind;
  icon: string;
  title: string;
  detail: string;
  capability: CapabilityState;
}[] = [
  {
    kind: "github",
    icon: "🐙",
    title: "GitHub repo",
    detail: "Connect a repository for future push-to-deploy builds.",
    capability: "available",
  },
  {
    kind: "template",
    icon: "🧩",
    title: "Template",
    detail: "Discover and fork an immutable community template from GitHub.",
    capability: "available",
  },
  {
    kind: "empty",
    icon: "📦",
    title: "Empty project",
    detail: "Start blank and add services yourself.",
    capability: "available",
  },
  {
    kind: "database",
    icon: "🗄️",
    title: "Database",
    detail: "Deploy an official self-managed database with private networking and a persistent volume.",
    capability: "setup-required",
  },
];

function isDatabaseTemplate(template: TemplateSummary): boolean {
  return (template.tags ?? []).includes("database");
}

export function databaseSourceCapability(
  templates: TemplateSummary[] | undefined,
  catalogFailed = false,
): CapabilityState {
  if (catalogFailed) return "unavailable";
  return templates?.some((template) => isDatabaseTemplate(template) && template.provisionable)
    ? "available"
    : "setup-required";
}

export function templateSourceCapability(
  _templates: TemplateSummary[] | undefined,
  _catalogFailed = false,
): CapabilityState {
  return "available";
}

export function NewProject() {
  const auth = useAuth();
  const humanSession = auth.state.status === "human";
  return (
    <CapabilityNewProject
      humanSession={humanSession}
      installationAdmin={auth.operatorFeaturesAvailable}
    />
  );
}

export function CapabilityNewProject({
  humanSession,
  installationAdmin,
}: {
  humanSession: boolean;
  installationAdmin: boolean;
}) {
  return installationAdmin ? (
    <InstallationAdminNewProject humanSession={humanSession} />
  ) : humanSession ? (
    <HumanNewProject />
  ) : (
    <OperatorNewProject />
  );
}

function HumanNewProject() {
  const navigate = useNavigate();
  const workspaces = useIdentityWorkspaces();
  const eligible = (workspaces.data ?? []).filter(
    (workspace) => workspace.role === "admin" || workspace.role === "member",
  );
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState("");
  const workspaceId =
    eligible.length === 1 ? eligible[0].id : selectedWorkspaceId;
  const gotoCanvas = (projectId: string, opts?: { service?: string }) =>
    navigate({
      to: "/projects/$projectId",
      params: { projectId },
      search: opts?.service ? { service: opts.service } : {},
    } as never);

  return (
    <section className="mx-auto flex w-full max-w-3xl flex-col gap-6 p-[var(--main-gutter)]">
      <header>
        <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
          New
        </p>
        <h1 className="m-0 mt-1 text-2xl font-semibold tracking-tight">
          Create a project
        </h1>
        <p className="m-0 mt-1 text-muted">
          Start with an empty project. Connect its source and deploy from the
          project workspace after an environment is selected.
        </p>
      </header>
      {workspaces.isPending ? (
        <Loading label="Loading project workspaces…" />
      ) : workspaces.error ? (
        <ErrorState
          title="Could not load project workspaces"
          error={workspaces.error}
          onRetry={() => workspaces.refetch()}
        />
      ) : eligible.length === 0 ? (
        <Empty
          title="Project creation is unavailable"
          detail="Your workspace role can view existing projects but does not grant project creation."
          action={
            <Button variant="secondary" onClick={() => void navigate({ to: "/" })}>
              Back to projects
            </Button>
          }
        />
      ) : (
        <>
          {eligible.length > 1 ? (
            <Field label="Workspace">
              <select
                value={selectedWorkspaceId}
                onChange={(event) => setSelectedWorkspaceId(event.target.value)}
                className="field-input"
              >
                <option value="">Select a workspace</option>
                {eligible.map((workspace) => (
                  <option key={workspace.id} value={workspace.id}>
                    {workspace.display_name || workspace.slug}
                  </option>
                ))}
              </select>
            </Field>
          ) : null}
          <EmptyFlow
            onDone={gotoCanvas}
            mutationAuthority="human"
            workspaceId={workspaceId}
          />
        </>
      )}
    </section>
  );
}

function OperatorNewProject() {
  return <InstallationAdminNewProject humanSession={false} />;
}

function InstallationAdminNewProject({ humanSession }: { humanSession: boolean }) {
  const navigate = useNavigate();
  const [kind, setKind] = useState<SourceKind | null>(null);
  const workspaces = useIdentityWorkspaces({ enabled: humanSession });
  const eligible = (workspaces.data ?? []).filter(
    (workspace) => workspace.role === "admin" || workspace.role === "member",
  );
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState("");
  const selectedWorkspaceStillEligible = eligible.some(
    (workspace) => workspace.id === selectedWorkspaceId,
  );
  const workspaceId = selectedWorkspaceStillEligible
    ? selectedWorkspaceId
    : !selectedWorkspaceId && eligible.length === 1
      ? eligible[0].id
      : "";
  const projectCreationAuthorized =
    !humanSession ||
    (!workspaces.isPending && !workspaces.error && Boolean(workspaceId));
  const catalog = useTemplates({ enabled: projectCreationAuthorized });

  // Environment is intentionally omitted here. The project-route scope guard
  // resolves Production/default and preserves the requested service/tab before
  // rendering any mutating project UI.
  const gotoCanvas = (projectId: string, opts?: { service?: string; tab?: string }) =>
    navigate({
      to: "/projects/$projectId",
      params: { projectId },
      search: {
        ...(opts?.service ? { service: opts.service } : {}),
        ...(opts?.tab ? { tab: opts.tab } : {}),
      },
    } as never);

  return (
    <section className="mx-auto flex w-full max-w-3xl flex-col gap-6 p-[var(--main-gutter)]">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">New</p>
          <h1 className="m-0 mt-1 text-2xl font-semibold tracking-tight">Create a project</h1>
          <p className="m-0 mt-1 text-muted">Pick a starting point.</p>
        </div>
        {kind ? (
          <Button variant="secondary" onClick={() => setKind(null)}>
            ← Back to sources
          </Button>
        ) : null}
      </header>

      {humanSession ? (
        workspaces.isPending ? (
          <Loading label="Loading project workspaces…" />
        ) : workspaces.error ? (
          <ErrorState
            title="Could not load project workspaces"
            error={workspaces.error}
            onRetry={() => workspaces.refetch()}
          />
        ) : eligible.length === 0 ? (
          <CapabilityBoundary
            state="forbidden"
            title="Project creation"
            detail="Installation administration enables catalogs and operational tools, but the current workspace role does not grant project creation."
          />
        ) : eligible.length > 1 ? (
          <Field label="Workspace">
            <select
              value={selectedWorkspaceId}
              onChange={(event) => setSelectedWorkspaceId(event.target.value)}
              className="field-input"
            >
              <option value="">Select a workspace</option>
              {eligible.map((workspace) => (
                <option key={workspace.id} value={workspace.id}>
                  {workspace.display_name || workspace.slug}
                </option>
              ))}
            </select>
          </Field>
        ) : null
      ) : null}

      {kind === null ? (
        <div className="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,240px),1fr))]">
          {NEW_PROJECT_SOURCES.map((s) => {
            const capability =
              s.kind === "database"
                ? databaseSourceCapability(catalog.data, Boolean(catalog.error))
                : s.capability;
            const disposition = describeCapability(capability);
            const database = catalog.data?.find(isDatabaseTemplate);
            const capabilityLabel =
              (s.kind === "database" || s.kind === "template") && catalog.isPending
                ? "Checking availability"
                : disposition.label;
            return (
              <button
                key={s.kind}
                type="button"
                aria-labelledby={`new-project-source-${s.kind}-title`}
                aria-describedby={`new-project-source-${s.kind}-detail`}
                disabled={!disposition.canUse || !projectCreationAuthorized}
                onClick={() => {
                  if (humanSession) setSelectedWorkspaceId(workspaceId);
                  setKind(s.kind);
                }}
                className="grid gap-1.5 rounded-lg border border-line bg-surface px-4 py-4 text-left transition-colors hover:border-line-strong hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-60"
              >
                <span className="text-2xl" aria-hidden>
                  {s.icon}
                </span>
                <span
                  id={`new-project-source-${s.kind}-title`}
                  className="text-sm font-semibold text-text"
                >
                  {s.title}
                </span>
                <span
                  id={`new-project-source-${s.kind}-detail`}
                  className="text-xs text-muted"
                >
                  {s.kind === "database" && !disposition.canUse
                    ? database?.capability_reason || s.detail
                    : s.detail}
                </span>
                {!disposition.canUse ? (
                  <span className="font-mono text-[10px] uppercase tracking-wider text-warn">
                    {capabilityLabel}
                  </span>
                ) : !projectCreationAuthorized ? (
                  <span className="font-mono text-[10px] uppercase tracking-wider text-warn">
                    Select an authorized workspace
                  </span>
                ) : null}
              </button>
            );
          })}
        </div>
      ) : null}

      {kind !== null && !projectCreationAuthorized ? (
        <CapabilityBoundary
          state="forbidden"
          title="Project creation"
          detail="The selected workspace is no longer authorized for project creation. No create request was sent."
        />
      ) : null}
      {kind === "empty" && projectCreationAuthorized ? (
        <EmptyFlow
          onDone={gotoCanvas}
          mutationAuthority={humanSession ? "human" : "operator"}
          workspaceId={humanSession ? workspaceId : undefined}
        />
      ) : null}
      {kind === "template" && projectCreationAuthorized ? (
        <CommunityTemplateFlow
          onDone={gotoCanvas}
          mutationAuthority={humanSession ? "human" : "operator"}
          workspaceId={humanSession ? workspaceId : undefined}
        />
      ) : null}
      {kind === "database" && projectCreationAuthorized ? (
        <TemplateFlow
          onDone={gotoCanvas}
          onlyTag="database"
          mutationAuthority={humanSession ? "human" : "operator"}
          workspaceId={humanSession ? workspaceId : undefined}
        />
      ) : null}
      {kind === "github" && projectCreationAuthorized ? (
        <GithubFlow
          onDone={gotoCanvas}
          mutationAuthority={humanSession ? "human" : "operator"}
          workspaceId={humanSession ? workspaceId : undefined}
        />
      ) : null}
    </section>
  );
}

type DoneFn = (projectId: string, opts?: { service?: string; tab?: string }) => void;

function useProjectInvalidator() {
  const queryClient = useQueryClient();
  return () => void queryClient.invalidateQueries({ queryKey: ["projects"] });
}

// ── Empty ─────────────────────────────────────────────────────────

function EmptyFlow({
  onDone,
  mutationAuthority = "operator",
  workspaceId,
}: {
  onDone: DoneFn;
  mutationAuthority?: "human" | "operator";
  workspaceId?: string;
}) {
  const invalidate = useProjectInvalidator();
  const [name, setName] = useState("");
  const [addService, setAddService] = useState(false);
  const [serviceName, setServiceName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [intent, setIntent] = useState<{
    fingerprint: string;
    projectKey: string;
    serviceKey: string;
    project?: { id: string };
  }>();
  const createdProject = intent?.project;

  async function submit() {
    setError(null);
    if (!name.trim()) {
      setError("A project name is required.");
      return;
    }
    if (mutationAuthority === "human" && !workspaceId?.trim()) {
      setError("Select a workspace before creating the project.");
      return;
    }
    if (addService && !serviceName.trim()) {
      setError("A starter service name is required.");
      return;
    }
    const fingerprint = JSON.stringify({
      name: name.trim(),
      workspaceId: workspaceId?.trim() ?? "",
      addService,
      serviceName: serviceName.trim(),
    });
    const activeIntent =
      intent?.fingerprint === fingerprint
        ? intent
        : {
            fingerprint,
            projectKey: newProjectIntentKey("project"),
            serviceKey: newProjectIntentKey("service"),
          };
    setIntent(activeIntent);
    setBusy(true);
    try {
      let project = activeIntent.project;
      if (!project) {
        project =
          mutationAuthority === "human"
            ? await createHumanProject(
                { name, workspaceId: workspaceId as string },
                activeIntent.projectKey,
              )
            : await createProject({ name }, activeIntent.projectKey);
        activeIntent.project = project;
        setIntent({ ...activeIntent });
        invalidate();
      }
      if (addService && serviceName.trim()) {
        const create =
          mutationAuthority === "human" ? createHumanService : createService;
        const svc = await create(
          { projectId: project.id, name: serviceName },
          activeIntent.serviceKey,
        );
        onDone(project.id, { service: svc.id });
        return;
      }
      onDone(project.id);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Empty project" description="Create the project now and add services whenever you like.">
      <form
        className="grid gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Field label="Project name" hint={name.trim() ? `slug: ${slugify(name)}` : undefined}>
          <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="my-app" autoFocus disabled={Boolean(createdProject)} />
        </Field>

        <label className="flex items-center gap-2 text-xs text-muted">
          <input
            type="checkbox"
            checked={addService}
            onChange={(e) => {
              setAddService(e.target.checked);
              if (e.target.checked && !serviceName.trim()) setServiceName("api");
            }}
            disabled={Boolean(createdProject)}
            className="h-3.5 w-3.5 accent-[var(--accent,#7c8cff)]"
          />
          Also create a starter service
        </label>

        {addService ? (
          <Field label="Service name">
            <TextInput
              value={serviceName}
              onChange={(e) => setServiceName(e.target.value)}
              placeholder="api"
              disabled={Boolean(createdProject)}
            />
          </Field>
        ) : null}

        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={busy}>
            {busy
              ? "Creating…"
              : createdProject
                ? "Retry starter service"
                : "Create project"}
          </Button>
          {createdProject ? (
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => onDone(createdProject.id)}
            >
              Continue without starter service
            </Button>
          ) : null}
        </div>
        {error ? <InlineError error={error} /> : null}
      </form>
    </Card>
  );
}

function newProjectIntentKey(kind: "project" | "service"): string {
  try {
    return `${kind}-${crypto.randomUUID()}`;
  } catch {
    return `${kind}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

// ── Template / Database ───────────────────────────────────────────

function CommunityTemplateFlow({
  onDone,
  mutationAuthority,
  workspaceId,
}: {
  onDone: DoneFn;
  mutationAuthority: "human" | "operator";
  workspaceId?: string;
}) {
  const invalidate = useProjectInvalidator();
  const [source, setSource] = useState("");
  const [ref, setRef] = useState("");
  const [discovery, setDiscovery] = useState<CommunityTemplateDiscovery | null>(null);
  const [projectName, setProjectName] = useState("");
  const [fork, setFork] = useState<CommunityTemplateFork | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState<"discover" | "fork" | "save" | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [intent, setIntent] = useState<{
    fingerprint: string;
    projectKey: string;
    forkKey: string;
    project?: { id: string };
  } | null>(null);
  const auth = mutationAuthority === "human" ? "human" : "operator";

  async function discover() {
    setError(null);
    setFork(null);
    setBusy("discover");
    try {
      const result = await discoverCommunityTemplate({ source, ref }, auth);
      setDiscovery(result);
      setProjectName(result.manifest.template.name);
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(null);
    }
  }

  async function forkIntoProject() {
    if (!discovery?.valid) return;
    if (!projectName.trim()) {
      setError("A project name is required.");
      return;
    }
    setError(null);
    setBusy("fork");
    const fingerprint = JSON.stringify({
      source: discovery.provenance.source,
      ref: discovery.provenance.ref,
      digest: discovery.provenance.manifest_sha256,
      projectName: projectName.trim(),
      workspaceId: workspaceId?.trim() ?? "",
    });
    let activeIntent =
      intent?.fingerprint === fingerprint
        ? intent
        : {
            fingerprint,
            projectKey: newProjectIntentKey("project"),
            forkKey: newProjectIntentKey("service"),
          };
    setIntent(activeIntent);
    try {
      let project = activeIntent.project;
      if (!project) {
        const created =
          mutationAuthority === "human"
            ? await createHumanProject(
                { name: projectName, workspaceId: workspaceId as string },
                activeIntent.projectKey,
              )
            : await createProject({ name: projectName }, activeIntent.projectKey);
        project = { id: created.id };
        activeIntent = { ...activeIntent, project };
        setIntent(activeIntent);
        invalidate();
      }
      const createdFork = await createCommunityTemplateFork(
        { projectId: project.id, discovery },
        activeIntent.forkKey,
        auth,
      );
      setFork(createdFork);
      setDraft(JSON.stringify(createdFork.draft, null, 2));
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(null);
    }
  }

  async function saveDraft() {
    if (!fork) return;
    setError(null);
    let parsed: CommunityManifest;
    try {
      parsed = JSON.parse(draft) as CommunityManifest;
    } catch {
      setError("The draft must be valid JSON before it can be saved.");
      return;
    }
    setBusy("save");
    try {
      const updated = await updateCommunityTemplateFork(
        {
          id: fork.id,
          projectId: fork.project_id,
          resourceVersion: fork.resource_version,
          draft: parsed,
        },
        auth,
      );
      setFork(updated);
      setDraft(JSON.stringify(updated.draft, null, 2));
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(null);
    }
  }

  if (fork) {
    return (
      <div className="grid gap-4">
        <Card
          title={fork.draft.template.name || "Local template draft"}
          description="Your copy records the GitHub source it came from, so upstream stays unchanged while you edit this draft."
          action={
            <span className="rounded-full border border-line px-2 py-1 font-mono text-[10px] uppercase text-accent">
              local
            </span>
          }
        >
          <div className="grid gap-3">
            <div className="grid gap-1 font-mono text-[11px] text-muted">
              <span className="break-all">Upstream: {fork.provenance.source}</span>
              <span className="break-all">Commit: {fork.provenance.ref}</span>
              <span className="break-all">Manifest: sha256:{fork.provenance.manifest_sha256}</span>
            </div>
            <Field label="Editable manifest JSON" hint="Unknown fields are rejected. Invalid intermediate drafts are saved with explicit findings and cannot be applied.">
              <textarea
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                spellCheck={false}
                className="min-h-80 w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 font-mono text-xs text-text focus:border-accent focus:outline-none"
              />
            </Field>
            <CommunityFindings findings={fork.findings} valid={fork.valid} />
            <div className="rounded-[var(--r-sm)] border border-warn/40 bg-warn/5 px-3 py-2 text-xs text-warn">
              Import creates a project-owned draft. Map its services and stage valid desired state from Project settings; pinned-image deployment remains a separate explicit action.
            </div>
            <div className="flex flex-wrap gap-2">
              <Button onClick={() => void saveDraft()} disabled={busy !== null}>
                {busy === "save" ? "Saving…" : "Save draft"}
              </Button>
              <Button variant="secondary" onClick={() => onDone(fork.project_id)} disabled={busy !== null}>
                Continue to project
              </Button>
            </div>
            {error ? <InlineError error={error} /> : null}
          </div>
        </Card>
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <Card title="Discover a community template" description="Midnight reads template.yaml, README, and SBOM from one exact public GitHub commit. Branches, tags, credentials, redirects, and arbitrary hosts are rejected.">
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            void discover();
          }}
        >
          <Field label="GitHub repository" hint="https://github.com/owner/repository">
            <TextInput value={source} onChange={(event) => setSource(event.target.value)} placeholder="https://github.com/acme/midnight-template" autoFocus />
          </Field>
          <Field label="Exact commit SHA" hint="A full 40-character commit; branches and tags are not accepted.">
            <TextInput value={ref} onChange={(event) => setRef(event.target.value)} placeholder="0123456789abcdef0123456789abcdef01234567" className="font-mono" />
          </Field>
          <div>
            <Button type="submit" disabled={busy !== null || !source.trim() || !/^[0-9a-fA-F]{40}$/.test(ref.trim())}>
              {busy === "discover" ? "Inspecting…" : "Inspect immutable source"}
            </Button>
          </div>
          {error ? <InlineError error={error} /> : null}
        </form>
      </Card>

      {discovery ? (
        <Card
          title={discovery.manifest.template.name || "Community template"}
          description={discovery.manifest.template.summary}
          action={
            <span className="rounded-full border border-warn/40 px-2 py-1 font-mono text-[10px] uppercase text-warn">
              community
            </span>
          }
        >
          <div className="grid gap-3 text-xs">
            <div className="grid gap-1 text-muted sm:grid-cols-2">
              <span>Maintainer: {discovery.manifest.metadata.maintainer || "Not declared"}</span>
              <span>License: {discovery.manifest.metadata.license || "Not declared"}</span>
              <span>Security: {discovery.manifest.metadata.security_review || "Not declared"}</span>
              <span>Risk: {discovery.manifest.metadata.risk_class || "Not declared"}</span>
              <span>Services: {discovery.manifest.template.services.length}</span>
              <span className="break-all">Commit: {discovery.provenance.ref}</span>
            </div>
            <CommunityFindings findings={discovery.findings} valid={discovery.valid} />
            {discovery.readme ? (
              <details className="rounded-[var(--r-sm)] border border-line px-3 py-2">
                <summary className="cursor-pointer font-semibold text-text">README</summary>
                <pre className="mt-3 max-h-64 overflow-auto whitespace-pre-wrap font-sans text-xs text-muted">{discovery.readme}</pre>
              </details>
            ) : null}
            {discovery.valid ? (
              <div className="grid gap-3 border-t border-line pt-3">
                <Field label="New project name">
                  <TextInput value={projectName} onChange={(event) => setProjectName(event.target.value)} />
                </Field>
                <div>
                  <Button onClick={() => void forkIntoProject()} disabled={busy !== null || !projectName.trim()}>
                    {busy === "fork" ? "Creating local fork…" : "Create project and fork"}
                  </Button>
                </div>
              </div>
            ) : null}
          </div>
        </Card>
      ) : null}
    </div>
  );
}

function CommunityFindings({
  findings,
  valid,
}: {
  findings: CommunityTemplateDiscovery["findings"];
  valid: boolean;
}) {
  return (
    <div className={`rounded-[var(--r-sm)] border px-3 py-2 ${valid ? "border-success/40 bg-success/5 text-success" : "border-danger/40 bg-danger-soft text-danger"}`}>
      <p className="m-0 font-semibold">{valid ? "Validation passed" : `${findings.length} validation issue${findings.length === 1 ? "" : "s"}`}</p>
      {findings.length > 0 ? (
        <ul className="mb-0 mt-2 grid gap-1 pl-5">
          {findings.map((finding) => (
            <li key={`${finding.path}:${finding.code}`}>
              <span className="font-mono">{finding.path}</span>: {finding.message}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function TemplateFlow({
  onDone,
  onlyTag,
  mutationAuthority = "operator",
  workspaceId,
}: {
  onDone: DoneFn;
  onlyTag?: string;
  mutationAuthority?: "human" | "operator";
  workspaceId?: string;
}) {
  const invalidate = useProjectInvalidator();
  const { data, error, isPending, refetch } = useTemplates();
  const [slug, setSlug] = useState<string | null>(null);
  const [intent, setIntent] = useState<{
    fingerprint: string;
    projectKey: string;
    deployKey: string;
    project?: { id: string; name: string };
  } | null>(null);

  if (isPending) return <Loading label="Loading templates…" />;
  if (error) return <ErrorState title="Failed to load templates" error={error} onRetry={() => refetch()} />;

  const templates = (data ?? []).filter((t) =>
    onlyTag ? (t.tags ?? []).includes(onlyTag) : true,
  );

  if (templates.length === 0) {
    return (
      <Empty
        title={onlyTag ? "No database templates" : "No templates available"}
        detail="This instance ships no matching one-click templates."
        icon={<span aria-hidden>🧩</span>}
        action={<Button onClick={() => refetch()}>Reload</Button>}
      />
    );
  }

  if (slug) {
    return (
      <TemplateConfig
        slug={slug}
        onBack={() => setSlug(null)}
        onDeploy={async (name) => {
          const fingerprint = JSON.stringify({
            slug,
            name,
            workspaceId: workspaceId?.trim() ?? "",
          });
          let activeIntent =
            intent?.fingerprint === fingerprint
              ? intent
              : {
                  fingerprint,
                  projectKey: newProjectIntentKey("project"),
                  deployKey: newProjectIntentKey("service"),
                };
          setIntent(activeIntent);
          let project = activeIntent.project;
          if (!project) {
            const created =
              mutationAuthority === "human"
                ? await createHumanProject(
                    { name, workspaceId: workspaceId as string },
                    activeIntent.projectKey,
                  )
                : await createProject({ name }, activeIntent.projectKey);
            project = { id: created.id, name };
            activeIntent = { ...activeIntent, project };
            setIntent(activeIntent);
            invalidate();
          }
          const result = await deployTemplate(
            slug,
            {
              projectId: project.id,
              name: project.name,
            },
            activeIntent.deployKey,
          );
          return { projectId: project.id, result };
        }}
        onDone={onDone}
      />
    );
  }

  return (
    <div className="grid gap-3">
      <p className="m-0 text-xs text-muted">
        {onlyTag
          ? "Provision a self-managed database. Midnight verifies engine connectivity and container-replacement persistence; it does not provide managed HA."
          : "Choose a template to materialize into a new project."}
      </p>
      <ul className="m-0 grid list-none gap-2 p-0 [grid-template-columns:repeat(auto-fill,minmax(min(100%,260px),1fr))]">
        {templates.map((t) => (
          <li key={t.slug}>
            <TemplateCard template={t} onSelect={() => setSlug(t.slug)} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function TemplateCard({ template, onSelect }: { template: TemplateSummary; onSelect: () => void }) {
  return (
    <button
      type="button"
      disabled={!template.provisionable}
      onClick={onSelect}
      className="grid h-full w-full gap-1.5 rounded-lg border border-line bg-surface px-4 py-4 text-left transition-colors hover:border-line-strong hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-60"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-semibold text-text">{template.name}</span>
        <span className="font-mono text-[10px] text-subtle">v{template.version}</span>
      </div>
      {template.summary ? <span className="text-xs text-muted">{template.summary}</span> : null}
      {!template.provisionable ? (
        <span className="text-xs text-warn">
          {template.capability_reason || "Runtime provisioning is unavailable on this instance."}
        </span>
      ) : null}
      {template.tags && template.tags.length > 0 ? (
        <div className="mt-1 flex flex-wrap gap-1">
          {template.tags.map((tag) => (
            <span
              key={tag}
              className="rounded-full border border-line bg-surface-2 px-2 py-0.5 text-[10px] text-subtle"
            >
              {tag}
            </span>
          ))}
        </div>
      ) : null}
    </button>
  );
}

function TemplateConfig({
  slug,
  onBack,
  onDeploy,
  onDone,
}: {
  slug: string;
  onBack: () => void;
  onDeploy: (name: string) => Promise<{ projectId: string; result: TemplateDeployResult }>;
  onDone: DoneFn;
}) {
  const { data, error, isPending, refetch } = useTemplate(slug);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [deployError, setDeployError] = useState<unknown>(null);
  const [provision, setProvision] = useState<{
    projectId: string;
    result: TemplateDeployResult;
  } | null>(null);
  const run = useTemplateProvisionRun(slug, provision?.result.provision_run_id);

  const projectName = name.trim() || data?.name || slug;

  async function submit() {
    setDeployError(null);
    setBusy(true);
    try {
      setProvision(await onDeploy(projectName));
      setBusy(false);
    } catch (err) {
      setDeployError(err);
      setBusy(false);
    }
  }

  return (
    <Card
      title={data?.name ?? slug}
      description={data?.summary}
      action={
        <Button variant="secondary" onClick={onBack}>
          ← Templates
        </Button>
      }
    >
      {isPending ? <Loading label="Loading template…" /> : null}
      {error ? <ErrorState title="Failed to load template" error={error} onRetry={() => refetch()} /> : null}

      {data ? (
        <div className="grid gap-4">
          {provision ? (
            <div
              className="grid gap-3 rounded-lg border border-line bg-surface-2 p-4"
              aria-live="polite"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-sm font-semibold text-text">{data.name} provisioning</span>
                <span className="font-mono text-[10px] uppercase tracking-wider text-accent">
                  {run.data?.state ?? provision.result.state}
                </span>
              </div>
              <div className="grid gap-1 text-xs text-muted">
                <span>
                  Connectivity query: {run.data?.connectivity_verified ? "verified" : "pending"}
                </span>
                <span>
                  Marker after container replacement: {run.data?.persistence_verified ? "verified" : "pending"}
                </span>
                <span>
                  Secret-bearing probes removed: {run.data?.cleanup_completed ? "verified" : "pending"}
                </span>
              </div>
              {run.data?.state === "failed" ? (
                <p className="m-0 text-xs text-danger">
                  {run.data.failure_code}: {run.data.failure_reason}
                </p>
              ) : null}
              {run.error ? (
                <ErrorState
                  title="Failed to read provisioning status"
                  error={run.error}
                  onRetry={() => run.refetch()}
                />
              ) : null}
              <div>
                <Button
                  onClick={() =>
                    onDone(provision.projectId, {
                      service: provision.result.created_services?.[0]?.id,
                    })
                  }
                >
                  Open project
                </Button>
              </div>
            </div>
          ) : null}
          {!provision ? (
            <>
          <div className="grid gap-2">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-subtle">
              Includes
            </span>
            <ul className="m-0 grid list-none gap-1.5 p-0">
              {data.services.map((svc) => (
                <li
                  key={svc.slug}
                  className="flex items-center justify-between gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs"
                >
                  <span className="font-mono text-text">{svc.slug}</span>
                  <span className="font-mono text-subtle">{svc.image}</span>
                </li>
              ))}
            </ul>
            {data.consumer_docs ? (
              <p className="m-0 text-[11px] text-muted">{data.consumer_docs}</p>
            ) : null}
          </div>

          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <Field label="Project name" hint={`slug: ${slugify(projectName)}`}>
              <TextInput
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={data.name}
              />
            </Field>
            <div>
              <Button type="submit" disabled={busy}>
                {busy ? "Deploying…" : "Deploy template"}
              </Button>
            </div>
            {deployError ? <InlineError error={deployError} /> : null}
          </form>
            </>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}

// ── GitHub ────────────────────────────────────────────────────────

function GithubFlow({
  onDone,
  mutationAuthority = "operator",
  workspaceId,
}: {
  onDone: DoneFn;
  mutationAuthority?: "human" | "operator";
  workspaceId?: string;
}) {
  const invalidate = useProjectInvalidator();
  const [name, setName] = useState("");
  const [selection, setSelection] = useState<RepoSelection | null>(null);
  const [busy, setBusy] = useState<"connect" | "configure" | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [intake, setIntake] = useState<{
    fingerprint: string;
    repo: string;
    projectKey: string;
    serviceKey: string;
    linkKey: string;
    projectId?: string;
    serviceId?: string;
  } | null>(null);

  async function launch(mode: "connect" | "configure") {
    if (!selection) return;
    setError(null);
    setBusy(mode);
    try {
      const repo = selection.repo;
      const projectName = name.trim() || repo.name || repo.full_name.split("/").pop()!;
      const branch = repo.default_branch || "main";
      const repositoryId = Number(repo.id);
      if (
        mutationAuthority === "human" &&
        (!Number.isSafeInteger(repositoryId) || repositoryId <= 0)
      ) {
        throw new Error("GitHub did not report a valid repository identity.");
      }
      const fingerprint = JSON.stringify({
        installationId: Number(selection.installationId),
        repositoryId,
        repo: repo.full_name,
        branch,
        projectName,
        workspaceId: workspaceId?.trim() ?? "",
      });
      let draft = intake?.fingerprint === fingerprint ? intake : null;
      if (!draft) {
        draft = {
          fingerprint,
          repo: repo.full_name,
          projectKey: newProjectIntentKey("project"),
          serviceKey: newProjectIntentKey("service"),
          linkKey: newProjectIntentKey("service"),
        };
        setIntake(draft);
      }
      if (!draft.projectId) {
        const project =
          mutationAuthority === "human"
            ? await createHumanProject(
                { name: projectName, workspaceId: workspaceId as string },
                draft.projectKey,
              )
            : await createProject({ name: projectName }, draft.projectKey);
        draft = { ...draft, projectId: project.id };
        setIntake(draft);
        invalidate();
      }
      if (!draft.serviceId) {
        const create =
          mutationAuthority === "human" ? createHumanService : createService;
        const service = await create({
          projectId: draft.projectId as string,
          name: repo.name || projectName,
        }, draft.serviceKey);
        draft = { ...draft, serviceId: service.id };
        setIntake(draft);
      }
      const serviceId = draft.serviceId;
      if (!serviceId) {
        throw new Error("Midnight created the service but did not return its identifier. Reload the project before trying again.");
      }
      if (mutationAuthority === "human") {
        await putHumanServiceGithubLink(
          serviceId,
          {
            installationId: Number(selection.installationId),
            repositoryId,
            branch,
          },
          draft.linkKey,
        );
      } else {
        await linkServiceGithub(serviceId, {
          installationId: selection.installationId,
          repo: repo.full_name,
          branch,
          cloneUrl: `https://github.com/${repo.full_name}.git`,
        });
      }
      setIntake(null);
      onDone(draft.projectId as string, {
        service: serviceId,
        ...(mode === "configure" ? { tab: "variables" } : {}),
      });
    } catch (err) {
      setError(err);
      setBusy(null);
    }
  }

  const recoverableIntake =
    error && intake?.repo === selection?.repo.full_name ? intake : null;

  return (
    <Card
      title="Connect a GitHub repository"
      description="Midnight creates the project and service, then links the repo. This step does not enqueue a deployment."
    >
      <div className="grid gap-4">
        <Field label="Project name" hint="Optional — defaults from the repository name.">
          <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="my-app" />
        </Field>

        {selection ? (
          <div className="grid gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3">
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <div className="truncate font-mono text-sm text-text">
                  {selection.repo.full_name}
                </div>
                <div className="mt-0.5 text-[11px] text-subtle">
                  branch {selection.repo.default_branch || "main"}
                </div>
              </div>
              <Button variant="secondary" onClick={() => setSelection(null)} disabled={busy !== null}>
                Change
              </Button>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button onClick={() => void launch("connect")} disabled={busy !== null}>
                {busy === "connect" ? "Connecting…" : "Create and connect"}
              </Button>
              <Button
                variant="secondary"
                onClick={() => void launch("configure")}
                disabled={busy !== null}
              >
                {busy === "configure" ? "Creating…" : "Configure variables first"}
              </Button>
            </div>
          </div>
        ) : (
          <RepoPicker onSelect={(sel) => setSelection(sel)} ctaLabel="Choose" />
        )}

        {error ? <InlineError error={error} /> : null}
        {recoverableIntake ? (
          <p className="m-0 text-xs text-muted" role="status">
            Retry resumes project {recoverableIntake.projectId}
            {recoverableIntake.serviceId ? ` and service ${recoverableIntake.serviceId}` : ""} instead of creating duplicates.
          </p>
        ) : null}
      </div>
    </Card>
  );
}
