import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { queryKeys, useServices } from "@/api/hooks";
import { useAuth } from "@/auth/AuthProvider";
import { Empty, ErrorState, Loading } from "@/components/states";
import { Button, Card, Field, InlineError } from "@/components/panels/form-kit";
import {
  updateCommunityTemplateFork,
  stageCommunityTemplateFork,
  useCommunityTemplateForks,
  type CommunityManifest,
  type CommunityTemplateFork,
} from "./api";

export function CommunityTemplateForks({
  projectId,
  environmentId,
}: {
  projectId: string;
  environmentId?: string;
}) {
  const authState = useAuth();
  const auth = authState.state.status === "human" ? "human" : "operator";
  const queryClient = useQueryClient();
  const forks = useCommunityTemplateForks(projectId, auth);
  const services = useServices(projectId, environmentId);
  const [selected, setSelected] = useState<CommunityTemplateFork | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [serviceIds, setServiceIds] = useState<Record<string, string>>({});
  const [stagedRevision, setStagedRevision] = useState<number>();

  function open(fork: CommunityTemplateFork) {
    setSelected(fork);
    setDraft(JSON.stringify(fork.draft, null, 2));
    setError(null);
    setStagedRevision(undefined);
    const mapped: Record<string, string> = {};
    for (const templateService of fork.draft.template.services) {
      const match = services.data?.find(
        (service) => service.slug === templateService.slug,
      );
      if (match) mapped[templateService.slug] = match.id;
    }
    setServiceIds(mapped);
  }

  async function stage() {
    if (!selected || !environmentId) return;
    setBusy(true);
    setError(null);
    try {
      const result = await stageCommunityTemplateFork({
        fork: selected,
        projectId,
        environmentId,
        serviceIds,
      });
      setStagedRevision(result.changeset?.revision);
      await queryClient.invalidateQueries({
        queryKey: queryKeys.environmentChangeset(environmentId),
      });
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    if (!selected) return;
    let parsed: CommunityManifest;
    try {
      parsed = JSON.parse(draft) as CommunityManifest;
    } catch {
      setError("The draft must be valid JSON before it can be saved.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const updated = await updateCommunityTemplateFork(
        {
          id: selected.id,
          projectId,
          resourceVersion: selected.resource_version,
          draft: parsed,
        },
        auth,
      );
      setSelected(updated);
      setDraft(JSON.stringify(updated.draft, null, 2));
      queryClient.setQueryData<CommunityTemplateFork[]>(
        ["community-template-forks", projectId, auth],
        (current = []) => current.map((fork) => (fork.id === updated.id ? updated : fork)),
      );
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card
      title="Community template drafts"
      description="Your own copies of community templates, each recording the GitHub source it came from. Import another from New project → Template."
    >
      {forks.isPending ? (
        <Loading label="Loading community template drafts…" />
      ) : forks.error ? (
        <ErrorState title="Community template drafts unavailable" error={forks.error} onRetry={() => void forks.refetch()} />
      ) : (forks.data ?? []).length === 0 ? (
        <Empty
          title="No local template drafts"
          detail="Discover an exact public GitHub commit from New project → Template to create a provenance-preserving local fork."
          icon={<span aria-hidden>🧩</span>}
          action={<span className="text-xs text-muted">Import from New project → Template</span>}
        />
      ) : (
        <div className="grid gap-3">
          <ul className="m-0 grid list-none gap-2 p-0">
            {(forks.data ?? []).map((fork) => (
              <li key={fork.id} className="flex flex-wrap items-center justify-between gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-semibold text-text">{fork.draft.template.name || fork.draft.template.slug}</span>
                    <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] uppercase text-accent">local</span>
                    <span className={`font-mono text-[10px] uppercase ${fork.valid ? "text-success" : "text-danger"}`}>{fork.valid ? "valid" : "invalid"}</span>
                  </div>
                  <p className="m-0 mt-1 truncate font-mono text-[10px] text-subtle">{fork.provenance.source}@{fork.provenance.ref.slice(0, 12)}</p>
                </div>
                <Button variant="secondary" onClick={() => open(fork)}>Edit draft</Button>
              </li>
            ))}
          </ul>

          {selected ? (
            <div className="grid gap-3 rounded-[var(--r-sm)] border border-line bg-surface px-3 py-3">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <p className="m-0 text-xs font-semibold text-text">{selected.draft.template.name}</p>
                  <p className="m-0 mt-1 break-all font-mono text-[10px] text-subtle">Manifest sha256:{selected.provenance.manifest_sha256}</p>
                </div>
                <Button variant="secondary" onClick={() => setSelected(null)} disabled={busy}>Close editor</Button>
              </div>
              <Field label="Editable manifest JSON" hint={`Current resource version: ${selected.resource_version}. Invalid drafts are retained but cannot be applied.`}>
                <textarea
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  spellCheck={false}
                  className="min-h-72 w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 font-mono text-xs text-text focus:border-accent focus:outline-none"
                />
              </Field>
              <TemplateDraftFindings fork={selected} />
              <div className="rounded-[var(--r-sm)] border border-warn/40 bg-warn/5 px-3 py-2 text-xs text-warn">
                Local drafts never inherit official trust. Staging compiles runtime, generated values, and volume intent for review; it does not deploy the pinned images.
              </div>
              {environmentId && auth === "human" ? (
                <div className="grid gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3">
                  <p className="m-0 text-xs font-semibold text-text">
                    Map template services
                  </p>
                  {selected.draft.template.services.map((templateService) => (
                    <label
                      key={templateService.slug}
                      className="grid gap-1 text-xs text-muted"
                    >
                      <span className="font-mono">{templateService.slug}</span>
                      <select
                        className="field-input"
                        value={serviceIds[templateService.slug] ?? ""}
                        onChange={(event) =>
                          setServiceIds((current) => ({
                            ...current,
                            [templateService.slug]: event.target.value,
                          }))
                        }
                      >
                        <option value="">Select an existing service</option>
                        {(services.data ?? []).map((service) => (
                          <option key={service.id} value={service.id}>
                            {service.display_name || service.slug}
                          </option>
                        ))}
                      </select>
                    </label>
                  ))}
                  <Button
                    onClick={() => void stage()}
                    disabled={
                      busy ||
                      !selected.valid ||
                      selected.draft.template.services.some(
                        (service) => !serviceIds[service.slug],
                      )
                    }
                  >
                    {busy ? "Working…" : "Add to draft"}
                  </Button>
                  {stagedRevision ? (
                    <p className="m-0 text-xs text-success">
                      Added to your draft (revision {stagedRevision}). Review it
                      in Changes.
                    </p>
                  ) : null}
                </div>
              ) : (
                <p className="m-0 text-xs text-muted">
                  Open project settings with a selected environment and a human
                  session to stage this draft.
                </p>
              )}
              <div><Button onClick={() => void save()} disabled={busy}>{busy ? "Saving…" : "Save draft"}</Button></div>
              {error ? <InlineError error={error} /> : null}
            </div>
          ) : null}
        </div>
      )}
    </Card>
  );
}

function TemplateDraftFindings({ fork }: { fork: CommunityTemplateFork }) {
  return (
    <div className={`rounded-[var(--r-sm)] border px-3 py-2 text-xs ${fork.valid ? "border-success/40 bg-success/5 text-success" : "border-danger/40 bg-danger-soft text-danger"}`}>
      <p className="m-0 font-semibold">{fork.valid ? "Validation passed" : `${fork.findings.length} validation issue${fork.findings.length === 1 ? "" : "s"}`}</p>
      {fork.findings.length > 0 ? (
        <ul className="mb-0 mt-2 grid gap-1 pl-5">
          {fork.findings.map((finding) => <li key={`${finding.path}:${finding.code}`}><span className="font-mono">{finding.path}</span>: {finding.message}</li>)}
        </ul>
      ) : null}
    </div>
  );
}
