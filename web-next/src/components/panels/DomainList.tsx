import * as Dialog from "@radix-ui/react-dialog";
import { useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { StatusPill } from "@/components/StatusPill";
import { ErrorState, Loading } from "@/components/states";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import { presentError } from "@/lib/errorPresentation";
import {
  normalizeDomainState,
  useCreateDomain,
  useDeleteDomain,
  useServiceDomains,
  type GeneratedDomainRecord,
} from "./domains/api";
import {
  useCreateCustomDomain,
  useCustomDomains,
  useDeleteCustomDomain,
  useVerifyCustomDomain,
  type CustomDomainRecord,
} from "./domains/custom-api";

const DOMAIN_READ_PERMISSION = "domains.read";
const DOMAIN_WRITE_PERMISSION = "domains.write";

export type DomainListProps = {
  projectId?: string;
  serviceId: string;
  environmentId?: string;
};

export function DomainList({
  projectId,
  serviceId,
  environmentId,
}: DomainListProps) {
  const target = {
    projectId: projectId ?? "",
    serviceId,
    environmentId: environmentId ?? "",
  };
  const validTarget = Boolean(projectId && serviceId && environmentId);
  const access = useProjectAccess(
    target.projectId,
    environmentId,
    validTarget,
  );
  const exactAccess =
    access.data?.principal_kind === "human" &&
    access.data.project_id === target.projectId &&
    access.data.environment_id === target.environmentId;
  const canRead =
    exactAccess &&
    !access.isPending &&
    !access.error &&
    access.data?.permissions.includes(DOMAIN_READ_PERMISSION) === true;
  const canWrite =
    canRead &&
    access.data?.permissions.includes(DOMAIN_WRITE_PERMISSION) === true;
  const query = useServiceDomains(target, canRead);
  const create = useCreateDomain(target);
  const remove = useDeleteDomain(target);
  const [removeCandidate, setRemoveCandidate] =
    useState<GeneratedDomainRecord>();
  const [mutationError, setMutationError] = useState<unknown>(null);
  const [notice, setNotice] = useState<string>();
  const [customExpanded, setCustomExpanded] = useState(false);

  if (!validTarget) {
    return (
      <CapabilityBoundary
        state="unavailable"
        title="Generated HTTP domains"
        detail="A concrete project, service, and environment are required. No domain request was sent."
      />
    );
  }
  if (access.isPending) {
    return <Loading label="Checking exact domain access…" />;
  }
  if (access.error || !exactAccess) {
    return (
      <ErrorState
        title="Domain access unavailable"
        error={
          access.error ??
          new Error(
            "Midnight could not verify the exact project and environment scope.",
          )
        }
        onRetry={() => void access.refetch()}
      />
    );
  }
  if (!canRead) {
    return (
      <CapabilityBoundary
        state="unavailable"
        title="Generated HTTP domains"
        detail="The current role does not grant domains-read permission for this exact environment. No domain request was sent."
      />
    );
  }
  if (query.isPending) return <Loading label="Loading generated domains…" />;
  if (query.error) {
    return (
      <ErrorState
        title="Failed to load generated domains"
        error={query.error}
        onRetry={() => void query.refetch()}
      />
    );
  }

  async function refreshAfterConflict(action: "create" | "delete") {
    await query.refetch();
    setRemoveCandidate(undefined);
    setNotice(
      action === "delete"
        ? "The domain changed before it could be removed. Midnight refreshed the latest state; review its version before trying again."
        : "Domain state changed while generation was being accepted. Midnight refreshed the latest state without claiming that a new domain was created.",
    );
  }

  async function generateDomain() {
    if (!canWrite || create.isPending || remove.isPending) return;
    setMutationError(null);
    setNotice(undefined);
    try {
      await create.mutateAsync();
      setNotice(
        "Generated HTTP intent is ready in Changes and will autosave for review.",
      );
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        await refreshAfterConflict("create");
        return;
      }
      setMutationError(error);
    }
  }

  async function confirmRemove() {
    if (
      !canWrite ||
      !removeCandidate ||
      create.isPending ||
      remove.isPending
    ) {
      return;
    }
    const candidate = removeCandidate;
    setMutationError(null);
    setNotice(undefined);
    try {
      await remove.mutateAsync({
        domainId: candidate.id,
        expectedVersion: candidate.version,
      });
      setRemoveCandidate(undefined);
      setNotice(
        `Removal of ${candidate.hostname} is ready in Changes. Review, then apply and deploy the environment draft to remove route intent; this does not prove that external DNS caches are clear.`,
      );
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        await refreshAfterConflict("delete");
        return;
      }
      setMutationError(error);
    }
  }

  const mutationPresentation = mutationError
    ? presentError(mutationError)
    : undefined;

  return (
    <div className="grid gap-5">
      <section className="grid gap-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <Heading
            title="Generated HTTP domain"
            detail="Midnight gives this service one hostname in this environment. It serves plain HTTP; HTTPS and reachability from the Internet are checked separately."
          />
          {canWrite ? (
            <button
              type="button"
              className="w-fit rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-3 py-2 text-xs font-semibold text-text disabled:opacity-50"
              disabled={create.isPending || remove.isPending}
              onClick={() => void generateDomain()}
            >
              {create.isPending ? "Staging…" : "Stage domain"}
            </button>
          ) : null}
        </div>

        {!canWrite ? (
          <Notice>
            You can view this domain and its routing, but not add or remove
            domains in this environment. Ask an administrator for access.
          </Notice>
        ) : null}
        {notice ? <Notice>{notice}</Notice> : null}
        {mutationPresentation ? (
          <div
            role="alert"
            className="rounded-[var(--r-sm)] border border-danger/40 bg-danger/10 px-3 py-2 text-xs text-danger"
          >
            {mutationPresentation.message}
            {mutationPresentation.recovery
              ? ` ${mutationPresentation.recovery}`
              : ""}
          </div>
        ) : null}

        <DomainRows
          records={query.data ?? []}
          canWrite={canWrite}
          busy={create.isPending || remove.isPending}
          onDelete={setRemoveCandidate}
        />

        {removeCandidate ? (
          <DomainRemovalDialog
            kind="generated domain and its route intent"
            hostname={removeCandidate.hostname}
            version={removeCandidate.version}
            busy={remove.isPending}
            onCancel={() => setRemoveCandidate(undefined)}
            onConfirm={() => void confirmRemove()}
          />
        ) : null}
      </section>

      <section className="grid gap-2">
        <Heading
          title="Custom HTTP hostnames"
          detail="Add a hostname, prove you own it with the TXT record shown, then publish HTTP routing. Ownership, routing, and reachability from the Internet are checked separately."
        />
        {!customExpanded ? (
          <button
            type="button"
            className="w-fit rounded border border-line-strong bg-surface-2 px-3 py-2 text-xs font-semibold text-text"
            onClick={() => setCustomExpanded(true)}
          >
            Manage custom hostnames
          </button>
        ) : (
          <CustomDomainPanel
            target={target}
            canWrite={canWrite}
            onCollapse={() => setCustomExpanded(false)}
          />
        )}
      </section>
    </div>
  );
}

function CustomDomainRows({
  records,
  canWrite,
  busy,
  onVerify,
  onDelete,
}: {
  records: CustomDomainRecord[];
  canWrite: boolean;
  busy: boolean;
  onVerify: (record: CustomDomainRecord) => void;
  onDelete: (record: CustomDomainRecord) => void;
}) {
  const [copied, setCopied] = useState<string>();
  const [copyError, setCopyError] = useState(false);

  if (records.length === 0) {
    return <EmptyRow label="No tenant-managed custom hostname has been added." />;
  }

  async function copy(label: string, value: string) {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(value);
      setCopied(label);
      setCopyError(false);
      window.setTimeout(
        () => setCopied((current) => (current === label ? undefined : current)),
        1_800,
      );
    } catch {
      setCopied(undefined);
      setCopyError(true);
    }
  }
  return (
    <div className="grid gap-2">
      {copyError ? (
        <p role="status" className="m-0 text-xs text-danger">
          Clipboard access failed. Select the value and copy it manually.
        </p>
      ) : null}
      <ul className="m-0 grid list-none gap-2 p-0">
        {records.map((record) => {
          const ownershipLabel =
            record.ownership_state === "verified"
              ? "Ownership verified"
              : record.ownership_state === "failed"
                ? "DNS check unavailable"
                : "Ownership pending";
          return (
            <li
              key={record.id}
              className="grid gap-3 rounded-lg border border-line bg-surface-2 px-4 py-3"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="grid gap-0.5">
                  <span className="break-all font-mono text-sm text-text">
                    {record.hostname}
                  </span>
                  <span className="text-[11px] text-muted">
                    Custom · {record.tls ? "HTTPS requested" : "HTTP only"} · version {record.version}
                  </span>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <StatusPill
                    status={
                      record.ownership_state === "verified"
                        ? "active"
                        : record.ownership_state === "failed"
                          ? "error"
                          : "pending"
                    }
                  />
                  <span className="text-[11px] text-muted">{ownershipLabel}</span>
                  {canWrite ? (
                    <>
                      <button
                        type="button"
                        className="text-[11px] text-text hover:underline disabled:opacity-50"
                        disabled={busy || record.ownership_state === "verified"}
                        onClick={() => onVerify(record)}
                        aria-label={`Verify DNS for ${record.hostname}`}
                      >
                        Verify DNS
                      </button>
                      <button
                        type="button"
                        className="text-[11px] text-danger hover:underline disabled:opacity-50"
                        disabled={busy}
                        onClick={() => onDelete(record)}
                        aria-label={`Remove ${record.hostname}`}
                      >
                        Remove
                      </button>
                    </>
                  ) : null}
                </div>
              </div>
              <div className="grid gap-1 rounded border border-line p-3 text-xs">
                <strong className="text-text">Routing DNS</strong>
                <span className="text-muted">
                  {record.routing_dns.record_type} · {record.routing_dns.state.replaceAll("_", " ")}
                </span>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="break-all font-mono text-muted">
                    Name: {record.routing_dns.record_name}
                  </span>
                  {record.routing_dns.record_value ? (
                    <CopyButton
                      label="routing DNS target"
                      copied={copied === `${record.id}:routing`}
                      onCopy={() =>
                        void copy(
                          `${record.id}:routing`,
                          record.routing_dns.record_value ?? "",
                        )
                      }
                    />
                  ) : null}
                </div>
                {record.routing_dns.record_value ? (
                  <span className="break-all font-mono text-muted">
                    Target: {record.routing_dns.record_value}
                  </span>
                ) : null}
                <span className="text-muted">{record.routing_dns.note}</span>
              </div>
              <div className="grid gap-1 rounded border border-line p-3 text-xs">
                <strong className="text-text">Ownership TXT record</strong>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="break-all font-mono text-muted">
                    Name: {record.dns_challenge.record_name}
                  </span>
                  <CopyButton
                    label="TXT name"
                    copied={copied === `${record.id}:name`}
                    onCopy={() =>
                      void copy(
                        `${record.id}:name`,
                        record.dns_challenge.record_name,
                      )
                    }
                  />
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="break-all font-mono text-muted">
                    Value: {record.dns_challenge.record_value}
                  </span>
                  <CopyButton
                    label="TXT value"
                    copied={copied === `${record.id}:value`}
                    onCopy={() =>
                      void copy(
                        `${record.id}:value`,
                        record.dns_challenge.record_value,
                      )
                    }
                  />
                </div>
                <span className="text-muted">
                  {record.ownership_state === "verified"
                    ? "Midnight observed the exact TXT value. This does not prove public routing or browser access."
                    : "Add this exact TXT record, then choose Verify DNS. Route publication is withheld until ownership is verified."}
                </span>
              </div>
              <div className="text-xs text-muted">
                {record.tls
                  ? "HTTPS requested. The certificate must be issued before this domain is published."
                  : "TLS off."}{" "}
                Public reachability not tested by Midnight.
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function CopyButton({ label, copied, onCopy }: { label: string; copied: boolean; onCopy: () => void }) {
  return (
    <button
      type="button"
      className="rounded border border-line-strong px-2 py-1 text-[10px] font-semibold text-text hover:bg-surface-3"
      aria-label={`Copy ${label}`}
      onClick={onCopy}
    >
      {copied ? "Copied" : "Copy"}
    </button>
  );
}

function CustomDomainPanel({
  target,
  canWrite,
  onCollapse,
}: {
  target: { projectId: string; serviceId: string; environmentId: string };
  canWrite: boolean;
  onCollapse: () => void;
}) {
  const query = useCustomDomains(target, true);
  const create = useCreateCustomDomain(target);
  const verify = useVerifyCustomDomain(target);
  const remove = useDeleteCustomDomain(target);
  const [hostname, setHostname] = useState("");
  const [tls, setTLS] = useState(false);
  const [candidate, setCandidate] = useState<CustomDomainRecord>();
  const [mutationError, setMutationError] = useState<unknown>();
  const [notice, setNotice] = useState<string>();
  const busy = create.isPending || verify.isPending || remove.isPending;

  async function refreshAfterConflict(action: "verify" | "delete") {
    await query.refetch();
    setCandidate(undefined);
    setNotice(
      action === "delete"
        ? "The custom hostname changed before it could be removed. Midnight refreshed the latest record; review its version before trying again."
        : "The custom hostname changed before DNS verification completed. Midnight refreshed the latest record; review its DNS challenge and version before trying again.",
    );
  }

  async function addHostname() {
    const normalized = hostname.trim();
    if (!normalized || busy) return;
    setMutationError(undefined);
    setNotice(undefined);
    try {
      await create.mutateAsync({ hostname: normalized, tls });
      setHostname("");
      setTLS(false);
    } catch (error) {
      setMutationError(error);
    }
  }

  async function verifyHostname(record: CustomDomainRecord) {
    if (busy) return;
    setMutationError(undefined);
    setNotice(undefined);
    try {
      await verify.mutateAsync({
        domainId: record.id,
        expectedVersion: record.version,
      });
    } catch (error) {
      if (isStaleConflict(error)) {
        await refreshAfterConflict("verify");
        return;
      }
      setMutationError(error);
    }
  }

  async function removeHostname() {
    if (!candidate || busy) return;
    setMutationError(undefined);
    setNotice(undefined);
    try {
      await remove.mutateAsync({
        domainId: candidate.id,
        expectedVersion: candidate.version,
      });
      setCandidate(undefined);
    } catch (error) {
      if (isStaleConflict(error)) {
        await refreshAfterConflict("delete");
        return;
      }
      setMutationError(error);
    }
  }

  const error = mutationError ? presentError(mutationError) : undefined;

  return (
    <div className="grid gap-3">
      <div className="flex justify-end">
        <button
          type="button"
          className="text-xs text-muted hover:text-text"
          onClick={onCollapse}
        >
          Hide custom hostnames
        </button>
      </div>
      {canWrite ? (
        <form
          className="flex flex-wrap items-end gap-2 rounded-lg border border-line bg-surface-2 p-3"
          onSubmit={(event) => {
            event.preventDefault();
            void addHostname();
          }}
        >
          <label className="grid min-w-[16rem] flex-1 gap-1 text-xs text-muted">
            Hostname
            <input
              value={hostname}
              onChange={(event) => setHostname(event.target.value)}
              placeholder="api.example.com"
              aria-describedby="custom-domain-hint"
              autoComplete="off"
              spellCheck={false}
              required
              className="rounded border border-line-strong bg-bg-2 px-3 py-2 text-sm text-text"
            />
          </label>
          <label className="flex items-center gap-2 pb-2 text-xs text-muted">
            <input
              type="checkbox"
              checked={tls}
              onChange={(event) => setTLS(event.target.checked)}
            />
            Request HTTPS
          </label>
          <span id="custom-domain-hint" className="sr-only">
            Enter a lowercase hostname only—no protocol, port, path, or wildcard.
            HTTPS also needs ACME set up on the worker and a certificate issued.
          </span>
          <button
            type="submit"
            className="rounded border border-line-strong px-3 py-2 text-xs font-semibold text-text disabled:opacity-50"
            disabled={busy || !hostname.trim()}
          >
            {create.isPending ? "Adding…" : "Add hostname"}
          </button>
        </form>
      ) : null}
      {error ? (
        <p
          role="alert"
          className="m-0 rounded border border-danger/40 bg-danger/10 px-3 py-2 text-xs text-danger"
        >
          {error.message}
          {error.recovery ? ` ${error.recovery}` : ""}
        </p>
      ) : null}
      {notice ? <Notice>{notice}</Notice> : null}
      {query.isPending ? (
        <Loading label="Loading custom hostnames…" />
      ) : query.error ? (
        <ErrorState
          title="Failed to load custom hostnames"
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : (
        <CustomDomainRows
          records={query.data ?? []}
          canWrite={canWrite}
          busy={busy}
          onVerify={(record) => void verifyHostname(record)}
          onDelete={(record) => {
            setMutationError(undefined);
            setCandidate(record);
          }}
        />
      )}
      {candidate ? (
        <DomainRemovalDialog
          kind="custom hostname intent"
          hostname={candidate.hostname}
          version={candidate.version}
          busy={remove.isPending}
          onCancel={() => setCandidate(undefined)}
          onConfirm={() => void removeHostname()}
        />
      ) : null}
    </div>
  );
}

function DomainRemovalDialog({
  kind,
  hostname,
  version,
  busy,
  onCancel,
  onConfirm,
}: {
  kind: string;
  hostname: string;
  version: number;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open && !busy) onCancel();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-[70] bg-black/50" />
        <Dialog.Content
          className="fixed left-1/2 top-1/2 z-[71] grid w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 gap-3 rounded-lg border border-danger/40 bg-surface p-4 shadow-2xl"
        >
          <Dialog.Title className="m-0 text-sm font-semibold text-text">
            Remove {hostname}?
          </Dialog.Title>
          <Dialog.Description className="m-0 text-xs text-muted">
            This removes the {kind} using version {version}. It does not delete
            or restart a deployment.
          </Dialog.Description>
          <div className="flex justify-end gap-2">
            <Dialog.Close asChild>
              <button
                type="button"
                className="rounded px-3 py-1.5 text-xs text-muted hover:text-text"
                disabled={busy}
              >
                Cancel
              </button>
            </Dialog.Close>
            <button
              type="button"
              className="rounded bg-danger px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50"
              disabled={busy}
              onClick={onConfirm}
            >
              {busy ? "Removing…" : `Confirm remove ${hostname}`}
            </button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function isStaleConflict(error: unknown): error is ApiError {
  return (
    error instanceof ApiError && (error.status === 409 || error.status === 412)
  );
}

function DomainRows({
  records,
  canWrite,
  busy,
  onDelete,
}: {
  records: GeneratedDomainRecord[];
  canWrite: boolean;
  busy: boolean;
  onDelete: (record: GeneratedDomainRecord) => void;
}) {
  if (records.length === 0) {
    return (
      <EmptyRow label="No tenant-managed generated domain has been created." />
    );
  }
  return (
    <ul className="m-0 grid list-none gap-2 p-0">
      {records.map((record) => {
        const state = normalizeDomainState(record);
        return (
          <li
            key={record.id}
            className="grid gap-2 rounded-lg border border-line bg-surface-2 px-4 py-3"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="grid min-w-0 gap-0.5">
                <span className="break-all font-mono text-sm text-text">
                  {record.hostname}
                </span>
                <span className="text-[11px] text-muted">
                  Desired-state version {record.version}
                </span>
              </div>
              <div className="flex items-center gap-2">
                <StatusPill status={state.pillStatus} />
                <span className="text-[11px] text-muted">{state.label}</span>
                {canWrite ? (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => onDelete(record)}
                    className="text-[11px] text-danger hover:underline disabled:opacity-50"
                    aria-label={`Remove ${record.hostname}`}
                  >
                    Remove
                  </button>
                ) : null}
              </div>
            </div>
            <p className="m-0 text-xs text-muted">{state.description}</p>
          </li>
        );
      })}
    </ul>
  );
}

function Notice({ children }: { children: ReactNode }) {
  return (
    <p
      role="status"
      className="m-0 rounded-[var(--r-sm)] border border-warn/40 bg-warn-soft px-3 py-2 text-xs text-muted"
    >
      {children}
    </p>
  );
}

function Heading({ title, detail }: { title: string; detail: string }) {
  return (
    <header className="grid gap-0.5">
      <h3 className="m-0 text-sm font-semibold text-text">{title}</h3>
      <p className="m-0 text-xs text-muted">{detail}</p>
    </header>
  );
}

function EmptyRow({ label }: { label: string }) {
  return (
    <p className="m-0 rounded-lg border border-dashed border-line bg-bg-2 px-4 py-3 text-xs text-muted">
      {label}
    </p>
  );
}
