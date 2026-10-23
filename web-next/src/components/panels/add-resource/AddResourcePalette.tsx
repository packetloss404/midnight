import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { deployServiceNow } from "@/api/deployActions";
import { useAuth } from "@/auth/AuthProvider";
import { AddServiceForm } from "@/components/panels/AddServiceForm";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import {
  newIdempotencyKey,
  useTemplates,
  createHumanService,
} from "@/components/panels/create/api";
import {
  listProjectGithubBranches,
  listProjectGithubRepositories,
  putHumanServiceGithubLink,
} from "@/components/panels/github/api";
import { Button, Field, InlineError, TextInput } from "@/components/panels/form-kit";
import { ErrorState, Loading } from "@/components/states";
import {
  createOperatorVolume,
  deployOperatorTemplate,
  listResourceServices,
  resolveResourceCapabilities,
  type ResourceMutationAuthority,
  type ResourceKind,
} from "./api";
import {
  ADD_RESOURCE_CATALOG,
  RESOURCE_CATEGORIES,
  resourceDefinition,
  searchResourceCatalog,
  type AddResourceDefinition,
} from "./catalog";

export type AddResourceResult = {
  kind: ResourceKind;
  serviceId?: string;
  operationId?: string;
  volumeId?: string;
};

export type AddResourcePaletteProps = {
  projectId: string;
  environmentId?: string;
  onClose: () => void;
  onCreated?: (result: AddResourceResult) => void;
  /** Installation capability bit; exact project/environment access is still required. */
  installationAdminAuthorized?: boolean;
  /** Optional command-palette deep link into one concrete source flow. */
  initialKind?: ResourceKind;
};

const RECENT_RESOURCE_KEY = "midnight.add-resource.recent";

export function AddResourcePalette({
  projectId,
  environmentId,
  onClose,
  onCreated,
  installationAdminAuthorized,
  initialKind,
}: AddResourcePaletteProps) {
  const auth = useAuth();
  const human = auth.state.status === "human";
  const operator = auth.state.status === "operator" || auth.state.status === "developer";
  const projectAccess = useProjectAccess(projectId, undefined, human);
  const environmentAccess = useProjectAccess(projectId, environmentId, human && Boolean(environmentId));
  const [mode, setMode] = useState<ResourceKind | undefined>(initialKind);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const [recentKinds, setRecentKinds] = useState<ResourceKind[]>(readRecentResources);
  const searchRef = useRef<HTMLInputElement>(null);
  const capabilities = resolveResourceCapabilities({
    authority: human ? "human" : "operator",
    projectId,
    environmentId,
    projectAccess: projectAccess.data,
    environmentAccess: environmentAccess.data,
    accessPending: human && (projectAccess.isPending || (Boolean(environmentId) && environmentAccess.isPending)),
    installationAdminAuthorized:
      human
        ? installationAdminAuthorized === true
        : installationAdminAuthorized ?? auth.operatorFeaturesAvailable,
  });

  if (!human && !operator) {
    for (const capability of Object.values(capabilities)) {
      capability.available = false;
      capability.reason = "A verified project session is required.";
    }
  }

  const activeMode = mode && capabilities[mode].available ? mode : undefined;
  useEffect(() => {
    if (mode && !capabilities[mode].available) setMode(undefined);
  }, [capabilities, mode]);
  useEffect(() => {
    if (initialKind) setMode(initialKind);
  }, [initialKind]);

  const catalogSections = useMemo(() => {
    const matches = searchResourceCatalog(query);
    if (query.trim()) {
      return RESOURCE_CATEGORIES.map((category) => ({
        category,
        resources: matches.filter((resource) => resource.category === category),
      })).filter((section) => section.resources.length > 0);
    }
    const recent = recentKinds
      .map((kind) => ADD_RESOURCE_CATALOG.find((resource) => resource.kind === kind))
      .filter((resource): resource is AddResourceDefinition => Boolean(resource));
    const recentSet = new Set(recent.map((resource) => resource.kind));
    return [
      ...(recent.length > 0 ? [{ category: "Recent", resources: recent }] : []),
      ...RESOURCE_CATEGORIES.map((category) => ({
        category,
        resources: matches.filter(
          (resource) =>
            resource.category === category && !recentSet.has(resource.kind),
        ),
      })).filter((section) => section.resources.length > 0),
    ];
  }, [query, recentKinds]);
  const visibleResources = useMemo(
    () => catalogSections.flatMap((section) => section.resources),
    [catalogSections],
  );

  useEffect(() => {
    setActiveIndex((current) =>
      nextAvailableResource(visibleResources, capabilities, current, 0),
    );
  }, [capabilities, visibleResources]);

  function selectResource(kind: ResourceKind) {
    if (!capabilities[kind].available) return;
    const nextRecent = [kind, ...recentKinds.filter((item) => item !== kind)].slice(0, 3);
    setRecentKinds(nextRecent);
    writeRecentResources(nextRecent);
    setMode(kind);
  }

  function onCatalogKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    const direction = event.key === "ArrowDown" ? 1 : event.key === "ArrowUp" ? -1 : 0;
    if (direction !== 0) {
      event.preventDefault();
      setActiveIndex((current) =>
        nextAvailableResource(visibleResources, capabilities, current, direction),
      );
      return;
    }
    if (event.key === "Enter") {
      const selected = visibleResources[activeIndex];
      if (selected && capabilities[selected.kind].available) {
        event.preventDefault();
        selectResource(selected.kind);
      }
    }
  }

  return (
    <Dialog.Root open onOpenChange={(open) => { if (!open) onClose(); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <Dialog.Content
          className="fixed inset-x-0 bottom-0 z-50 max-h-[92dvh] w-full overflow-auto rounded-t-xl border border-line bg-surface p-4 shadow-2xl sm:bottom-auto sm:left-1/2 sm:top-1/2 sm:max-h-[90vh] sm:w-[calc(100vw-2rem)] sm:max-w-3xl sm:-translate-x-1/2 sm:-translate-y-1/2 sm:rounded-xl sm:p-5"
          onOpenAutoFocus={(event) => {
            if (!activeMode) {
              event.preventDefault();
              requestAnimationFrame(() => searchRef.current?.focus());
            }
          }}
        >
        <header className="mb-4 flex items-start justify-between gap-4">
          <div>
            <p className="m-0 font-mono text-[10px] uppercase tracking-wider text-accent">
              {projectId} / {environmentId || "Choose an environment"}
            </p>
            <Dialog.Title className="m-0 mt-1 text-lg font-semibold text-text">{activeMode ? resourceTitle(activeMode) : "Add a resource"}</Dialog.Title>
            <Dialog.Description className="m-0 mt-1 text-xs text-muted">
              {activeMode
                ? `Add this resource to ${environmentId ? "the selected environment" : "this project"}.`
                : "Search the catalog, then choose what to add to this project and environment."}
            </Dialog.Description>
          </div>
          <Dialog.Close asChild><Button variant="secondary">Close</Button></Dialog.Close>
        </header>

        {activeMode ? (
          <div className="grid gap-4">
            <button type="button" className="w-fit text-xs font-semibold text-accent" onClick={() => setMode(undefined)}>← All resources</button>
            <ResourceFlow
              kind={activeMode}
              human={human}
              projectId={projectId}
              environmentId={environmentId}
              mutationAuthority={human ? "human" : "operator"}
              onCreated={(result) => {
                if (onCreated) {
                  onCreated(result);
                } else {
                  onClose();
                }
              }}
            />
          </div>
        ) : (
          <div className="grid gap-4">
            <div className="grid gap-1.5">
              <label htmlFor="add-resource-search" className="text-xs font-semibold text-text">
                Search resources
              </label>
              <input
                ref={searchRef}
                id="add-resource-search"
                type="search"
                value={query}
                onChange={(event) => {
                  setQuery(event.target.value);
                  setActiveIndex(0);
                }}
                onKeyDown={onCatalogKeyDown}
                placeholder="GitHub, Postgres, volume…"
                className="field-input"
                role="combobox"
                aria-expanded="true"
                aria-controls="add-resource-catalog"
                aria-describedby="add-resource-search-help"
                aria-activedescendant={
                  visibleResources[activeIndex]
                    ? `add-resource-${visibleResources[activeIndex].kind}`
                    : undefined
                }
              />
              <span id="add-resource-search-help" className="text-[11px] text-subtle">
                Use ↑ and ↓ to move, then Enter to choose.
              </span>
            </div>

            <div
              id="add-resource-catalog"
              role="listbox"
              aria-label="Resource catalog"
              className="grid max-h-[55vh] gap-4 overflow-y-auto pr-1"
            >
              {visibleResources.length === 0 ? (
                <p className="m-0 rounded-lg border border-dashed border-line px-4 py-8 text-center text-sm text-muted">
                  No resources match “{query.trim()}”.
                </p>
              ) : (
                catalogSections.map((section) => (
                  <section
                    key={section.category}
                    role="group"
                    aria-labelledby={`resource-category-${section.category.toLocaleLowerCase()}`}
                  >
                    <h2
                      id={`resource-category-${section.category.toLocaleLowerCase()}`}
                      className="m-0 mb-2 font-mono text-[10px] uppercase tracking-wider text-muted"
                    >
                      {section.category}
                    </h2>
                    <div className="grid gap-2 sm:grid-cols-2">
                      {section.resources.map((resource) => {
                        const capability = capabilities[resource.kind];
                        const index = visibleResources.findIndex(
                          (item) => item.kind === resource.kind,
                        );
                        return (
                          <button
                            key={resource.kind}
                            id={`add-resource-${resource.kind}`}
                            type="button"
                            role="option"
                            aria-labelledby={`add-resource-${resource.kind}-title`}
                            aria-describedby={`add-resource-${resource.kind}-detail`}
                            aria-selected={index === activeIndex}
                            disabled={!capability.available}
                            onMouseEnter={() => setActiveIndex(index)}
                            onFocus={() => setActiveIndex(index)}
                            onClick={() => selectResource(resource.kind)}
                            className={[
                              "grid min-h-28 grid-cols-[2.25rem_1fr] gap-x-3 gap-y-1 rounded-lg border bg-surface-2 p-3 text-left transition-colors sm:p-4",
                              index === activeIndex && capability.available
                                ? "border-accent ring-1 ring-accent"
                                : "border-line",
                              capability.available
                                ? "hover:border-accent"
                                : "cursor-not-allowed opacity-60",
                            ].join(" ")}
                          >
                            <span
                              aria-hidden="true"
                              className="row-span-3 grid size-9 place-items-center rounded-md border border-line bg-bg-2 font-mono text-[10px] font-bold text-accent"
                            >
                              {resource.icon}
                            </span>
                            <span
                              id={`add-resource-${resource.kind}-title`}
                              className="text-sm font-semibold text-text"
                            >
                              {resource.title}
                            </span>
                            <span
                              id={`add-resource-${resource.kind}-detail`}
                              className="text-xs text-muted"
                            >
                              {resource.detail}
                            </span>
                            <span className={`text-[10px] font-semibold uppercase tracking-wide ${capability.available ? "text-accent" : "text-subtle"}`}>
                              {capability.available ? "Available" : capability.reason}
                            </span>
                          </button>
                        );
                      })}
                    </div>
                  </section>
                ))
              )}
            </div>
          </div>
        )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function ResourceFlow({
  kind,
  human,
  projectId,
  environmentId,
  mutationAuthority,
  onCreated,
}: {
  kind: ResourceKind;
  human: boolean;
  projectId: string;
  environmentId?: string;
  mutationAuthority: ResourceMutationAuthority;
  onCreated: (result: AddResourceResult) => void;
}) {
  if (kind === "empty" || kind === "image" || (kind === "github" && !human)) {
    return (
      <AddServiceForm
        projectId={projectId}
        environmentId={environmentId}
        mutationAuthority={human ? "human" : "operator"}
        imageDeployAvailable={kind === "image"}
        githubSourceAvailable={kind === "github"}
        onCreated={(serviceId) => onCreated({ kind, serviceId })}
      />
    );
  }
  if (kind === "github") {
    return <HumanGithubResource projectId={projectId} environmentId={environmentId} onCreated={(serviceId) => onCreated({ kind, serviceId })} />;
  }
  if (kind === "template" || kind === "database") {
    return <TemplateResource projectId={projectId} environmentId={environmentId} databaseOnly={kind === "database"} mutationAuthority={mutationAuthority} onCreated={(operationId) => onCreated({ kind, operationId })} />;
  }
  return <VolumeResource projectId={projectId} environmentId={environmentId!} mutationAuthority={mutationAuthority} onCreated={(volumeId) => onCreated({ kind, volumeId })} />;
}

type GithubIntent = {
  repositoryId: number;
  branch: string;
  name: string;
  createKey: string;
  linkKey: string;
  deployKey: string;
  serviceId?: string;
};

// Linking a repository starts no deployment. `deployNow` opts into the first
// one so a newly linked service is not stranded until someone pushes.
function HumanGithubResource({ projectId, environmentId, onCreated }: { projectId: string; environmentId?: string; onCreated: (serviceId: string) => void }) {
  const client = useQueryClient();
  const repos = useQuery({ queryKey: ["project-github-repositories", projectId], queryFn: ({ signal }) => listProjectGithubRepositories(projectId, signal) });
  const [repositoryId, setRepositoryId] = useState<number>();
  const selected = repos.data?.find((repo) => repo.repository_id === repositoryId);
  const branches = useQuery({
    queryKey: ["project-github-branches", projectId, repositoryId],
    queryFn: ({ signal }) => listProjectGithubBranches(projectId, repositoryId!, signal),
    enabled: Boolean(repositoryId),
  });
  const [branchChoice, setBranchChoice] = useState("");
  const [name, setName] = useState("");
  const [intent, setIntent] = useState<GithubIntent>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const [deployNow, setDeployNow] = useState(false);
  const branch = branchChoice || selected?.default_branch || "";

  async function submit() {
    if (!selected || !branch || busy) return;
    setError(undefined);
    const serviceName = name.trim() || selected.repository_full_name.split("/").pop()!;
    if (intent && (intent.repositoryId !== selected.repository_id || intent.branch !== branch || intent.name !== serviceName)) {
      setError("A GitHub create request is already in progress for different choices. Restore them before retrying.");
      return;
    }
    const stable = intent ?? { repositoryId: selected.repository_id, branch, name: serviceName, createKey: newIdempotencyKey(), linkKey: newIdempotencyKey(), deployKey: newIdempotencyKey() };
    setIntent(stable);
    setBusy(true);
    try {
      const service = stable.serviceId ? { id: stable.serviceId } : await createHumanService({ projectId, name: serviceName }, stable.createKey);
      setIntent({ ...stable, serviceId: service.id });
      await putHumanServiceGithubLink(service.id, { installationId: selected.installation_id, repositoryId: selected.repository_id, branch }, stable.linkKey);
      if (deployNow && environmentId) {
        await deployServiceNow(service.id, environmentId, "operator", stable.deployKey);
        await client.invalidateQueries({ queryKey: ["deployments", service.id] });
      }
      await client.invalidateQueries({ queryKey: ["services"] });
      setIntent(undefined);
      onCreated(service.id);
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  }

  if (repos.isPending) return <Loading label="Loading project repositories…" />;
  if (repos.error) return <ErrorState title="Repositories unavailable" error={repos.error} onRetry={() => void repos.refetch()} />;
  return (
    <div className="grid gap-3">
      <Field label="Repository">
        <select value={repositoryId ?? ""} onChange={(event) => { setRepositoryId(Number(event.target.value) || undefined); setBranchChoice(""); }} className="rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text">
          <option value="">Choose a repository</option>
          {(repos.data ?? []).map((repo) => <option key={repo.repository_id} value={repo.repository_id}>{repo.repository_full_name}</option>)}
        </select>
      </Field>
      {repositoryId && branches.isPending ? <Loading label="Loading branches…" /> : null}
      {repositoryId && branches.error ? <ErrorState title="Branches unavailable" error={branches.error} onRetry={() => void branches.refetch()} /> : null}
      {branches.data ? (
        <Field label="Branch">
          <select value={branch} onChange={(event) => setBranchChoice(event.target.value)} className="rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text">
            {branches.data.map((candidate) => <option key={candidate.name} value={candidate.name}>{candidate.name}</option>)}
          </select>
        </Field>
      ) : null}
      <Field label="Service name" hint="Optional — defaults to the repository name."><TextInput value={name} onChange={(event) => setName(event.target.value)} /></Field>
      {environmentId ? (
        <label className="flex items-start gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2.5">
          <input type="checkbox" checked={deployNow} onChange={(event) => setDeployNow(event.target.checked)} className="mt-0.5" />
          <span className="grid gap-0.5">
            <span className="text-sm font-semibold text-text">Deploy now after linking</span>
            <span className="text-xs text-muted">Build the linked branch immediately. Leave this unchecked to wait for the next push; the link itself never deploys.</span>
          </span>
        </label>
      ) : null}
      <div><Button disabled={!selected || !branch || busy} onClick={() => void submit()}>{busy ? "Creating…" : "Create and link"}</Button></div>
      {error ? <InlineError error={error} /> : null}
      {intent?.serviceId ? <p role="status" className="m-0 text-xs text-muted">Service {intent.serviceId} was created. Retry resumes the same source-link request.</p> : null}
    </div>
  );
}

function TemplateResource({ projectId, environmentId, databaseOnly, mutationAuthority, onCreated }: { projectId: string; environmentId?: string; databaseOnly: boolean; mutationAuthority: ResourceMutationAuthority; onCreated: (operationId: string) => void }) {
  const templates = useTemplates();
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const [intent, setIntent] = useState<{
    slug: string;
    name?: string;
    environmentId?: string;
    idempotencyKey: string;
  }>();
  const choices = useMemo(() => (templates.data ?? []).filter((template) => template.provisionable && (!databaseOnly || template.tags?.some((tag) => tag.toLowerCase() === "database"))), [templates.data, databaseOnly]);

  if (templates.isPending) return <Loading label="Loading official templates…" />;
  if (templates.error) return <ErrorState title="Templates unavailable" error={templates.error} onRetry={() => void templates.refetch()} />;
  return (
    <div className="grid gap-3">
      {choices.length === 0 ? <p role="note" className="m-0 text-sm text-muted">No executable {databaseOnly ? "database " : ""}template is installed. No resource can be provisioned here.</p> : (
        <>
          <Field label={databaseOnly ? "Database template" : "Template"}>
            <select value={slug} onChange={(event) => setSlug(event.target.value)} className="rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text"><option value="">Choose a template</option>{choices.map((template) => <option key={template.slug} value={template.slug}>{template.name} · {template.version}</option>)}</select>
          </Field>
          <Field label="Instance name" hint="Optional; the template supplies a deterministic default."><TextInput value={name} onChange={(event) => setName(event.target.value)} /></Field>
          <div><Button disabled={!slug || busy} onClick={() => {
            const normalizedName = name.trim() || undefined;
            if (intent && (intent.slug !== slug || intent.name !== normalizedName || intent.environmentId !== environmentId)) {
              setError("A template request is already in progress for different choices. Restore them before retrying.");
              return;
            }
            const stable = intent ?? { slug, name: normalizedName, environmentId, idempotencyKey: newIdempotencyKey() };
            setIntent(stable); setBusy(true); setError(undefined);
            void deployOperatorTemplate(slug, { projectId, environmentId, name: normalizedName }, stable.idempotencyKey, mutationAuthority)
              .then((result) => { setIntent(undefined); onCreated(result.operation_id); })
              .catch(setError).finally(() => setBusy(false));
          }}>{busy ? "Provisioning…" : "Provision template"}</Button></div>
        </>
      )}
      {error ? <InlineError error={error} /> : null}
    </div>
  );
}

function VolumeResource({ projectId, environmentId, mutationAuthority, onCreated }: { projectId: string; environmentId: string; mutationAuthority: ResourceMutationAuthority; onCreated: (volumeId: string) => void }) {
  const services = useQuery({
    queryKey: ["services", projectId, environmentId],
    queryFn: ({ signal }) => listResourceServices(projectId, environmentId, mutationAuthority, signal),
  });
  const client = useQueryClient();
  const [serviceId, setServiceId] = useState("");
  const [name, setName] = useState("");
  const [mountPath, setMountPath] = useState("/data");
  const [sizeGiB, setSizeGiB] = useState("10");
  const [key, setKey] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const selected = services.data?.find((service) => service.id === serviceId);
  const sizeBytes = parseVolumeSizeBytes(sizeGiB);

  if (services.isPending) return <Loading label="Loading services…" />;
  if (services.error) return <ErrorState title="Services unavailable" error={services.error} onRetry={() => void services.refetch()} />;
  if (!services.data?.length) return <p role="note" className="m-0 text-sm text-muted">Create a service before adding storage. No volume was created.</p>;
  return (
    <div className="grid gap-3">
      <Field label="Service"><select value={serviceId} onChange={(event) => { setServiceId(event.target.value); setKey(undefined); }} className="rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text"><option value="">Choose a service</option>{services.data.map((service) => <option key={service.id} value={service.id}>{service.display_name || service.slug}</option>)}</select></Field>
      <Field label="Volume name"><TextInput value={name} onChange={(event) => { setName(event.target.value); setKey(undefined); }} placeholder="data" /></Field>
      <Field label="Mount path"><TextInput value={mountPath} onChange={(event) => { setMountPath(event.target.value); setKey(undefined); }} placeholder="/data" /></Field>
      <Field label="Size (GiB)"><TextInput type="number" min="1" value={sizeGiB} onChange={(event) => { setSizeGiB(event.target.value); setKey(undefined); }} /></Field>
      <div><Button disabled={!selected || !name.trim() || !mountPath.trim() || sizeBytes === undefined || busy} onClick={() => {
        if (!selected || sizeBytes === undefined) {
          setError("Volume size must be a whole, positive number of GiB within the supported range.");
          return;
        }
        const stableKey = key ?? newIdempotencyKey(); setKey(stableKey); setBusy(true); setError(undefined);
        void createOperatorVolume({ projectId, environmentId, service: selected, name, mountPath, sizeBytes }, stableKey, mutationAuthority)
          .then(async (volume) => { await client.invalidateQueries({ queryKey: ["environment-resources", environmentId] }); onCreated(volume.id); })
          .catch(setError).finally(() => setBusy(false));
      }}>{busy ? "Creating…" : "Create volume"}</Button></div>
      {error ? <InlineError error={error} /> : null}
    </div>
  );
}

export function parseVolumeSizeBytes(rawGiB: string): number | undefined {
  const gibibytes = Number(rawGiB);
  const bytes = gibibytes * 1024 ** 3;
  if (!Number.isFinite(gibibytes) || gibibytes < 1 || !Number.isSafeInteger(bytes)) {
    return undefined;
  }
  return bytes;
}

function resourceTitle(kind: ResourceKind): string {
  return resourceDefinition(kind).title;
}

function readRecentResources(): ResourceKind[] {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(RECENT_RESOURCE_KEY) ?? "[]");
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((kind): kind is ResourceKind =>
      ADD_RESOURCE_CATALOG.some((resource) => resource.kind === kind),
    ).slice(0, 3);
  } catch {
    return [];
  }
}

function writeRecentResources(kinds: ResourceKind[]) {
  try {
    window.localStorage.setItem(RECENT_RESOURCE_KEY, JSON.stringify(kinds));
  } catch {
    // The catalog remains usable when browser storage is unavailable.
  }
}

function nextAvailableResource(
  resources: AddResourceDefinition[],
  capabilities: Record<ResourceKind, { available: boolean }>,
  from: number,
  direction: -1 | 0 | 1,
): number {
  if (resources.length === 0) return 0;
  if (direction === 0 && capabilities[resources[from]?.kind]?.available) {
    return from;
  }
  let index = direction === 0 ? -1 : from;
  for (let offset = 0; offset < resources.length; offset += 1) {
    index = (index + (direction || 1) + resources.length) % resources.length;
    if (capabilities[resources[index].kind].available) return index;
  }
  return Math.min(from, resources.length - 1);
}
