import { useState, type FormEvent, type ReactNode } from "react";
import { StatusPill } from "@/components/StatusPill";
import { ErrorState, Loading } from "@/components/states";
import {
  normalizeRecoveryDomainState,
  useCreateRecoveryDomain,
  useDeleteRecoveryDomain,
  useRecoveryServiceDomains,
  type RecoveryDomainRecord,
} from "./domains/recovery-api";

export type RecoveryDomainListProps = {
  projectId?: string;
  serviceId: string;
  environmentId?: string;
};

export function RecoveryDomainList({
  projectId,
  serviceId,
  environmentId,
}: RecoveryDomainListProps) {
  const query = useRecoveryServiceDomains(
    projectId ?? "",
    serviceId,
    environmentId,
  );
  const create = useCreateRecoveryDomain();
  const remove = useDeleteRecoveryDomain(
    projectId ?? "",
    serviceId,
    environmentId ?? "",
  );
  const [hostname, setHostname] = useState("");
  const [generatedTLS, setGeneratedTLS] = useState(false);
  const [customTLS, setCustomTLS] = useState(false);

  if (!projectId || !environmentId) {
    return (
      <Notice>
        Select a concrete project environment to manage recovery domains.
      </Notice>
    );
  }
  if (query.isPending) return <Loading label="Loading recovery domains…" />;
  if (query.error) {
    return (
      <ErrorState
        title="Failed to load recovery domains"
        error={query.error}
        onRetry={() => void query.refetch()}
      />
    );
  }

  const submitCustom = (event: FormEvent) => {
    event.preventDefault();
    const value = hostname.trim();
    if (!value) return;
    create.mutate(
      {
        projectId,
        environmentId,
        serviceId,
        kind: "custom",
        hostname: value,
        tls: customTLS,
      },
      { onSuccess: () => setHostname("") },
    );
  };

  return (
    <div className="grid gap-5">
      <Notice>
        This installation-wide recovery surface uses the flat domain API. It is
        separate from the tenant generated-domain lifecycle.
      </Notice>

      <section className="grid gap-2">
        <Heading
          title="Generated domains"
          detail="Recovery compatibility for operator-managed generated routes below the configured apps domain."
        />
        <button
          type="button"
          className="w-fit rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-3 py-2 text-xs font-semibold text-text disabled:opacity-50"
          disabled={create.isPending}
          onClick={() =>
            create.mutate({
              projectId,
              environmentId,
              serviceId,
              kind: "generated",
              tls: generatedTLS,
            })
          }
        >
          {create.isPending ? "Creating…" : "Generate recovery domain"}
        </button>
        <label className="flex items-center gap-2 text-xs text-muted">
          <input
            type="checkbox"
            checked={generatedTLS}
            onChange={(event) => setGeneratedTLS(event.target.checked)}
          />
          Request HTTPS for the generated recovery domain
        </label>
        <RecoveryDomainRows
          records={query.data?.generatedDomains ?? []}
          empty="No recovery-generated domain has been created."
          onDelete={(id) => remove.mutate(id)}
        />
      </section>

      <section className="grid gap-2">
        <Heading
          title="Custom domains"
          detail="Custom hostname, ownership, and TLS controls are available in recovery mode only. Sign in to the console to manage domains."
        />
        <form
          onSubmit={submitCustom}
          className="grid gap-2 sm:grid-cols-[1fr_auto]"
        >
          <input
            className="field-input"
            value={hostname}
            onChange={(event) => setHostname(event.target.value)}
            placeholder="app.example.com"
            aria-label="Recovery custom domain hostname"
          />
          <button
            type="submit"
            className="rounded-[var(--r-sm)] bg-accent px-3 py-2 text-xs font-semibold text-white disabled:opacity-50"
            disabled={!hostname.trim() || create.isPending}
          >
            Add recovery domain
          </button>
        </form>
        <label className="flex items-center gap-2 text-xs text-muted">
          <input
            type="checkbox"
            checked={customTLS}
            onChange={(event) => setCustomTLS(event.target.checked)}
          />
          Request HTTPS for the custom recovery domain
        </label>
        {create.error ? <Notice>{create.error.message}</Notice> : null}
        {remove.error ? <Notice>{remove.error.message}</Notice> : null}
        <RecoveryDomainRows
          records={query.data?.customDomains ?? []}
          empty="No recovery custom domains have been attached."
          onDelete={(id) => remove.mutate(id)}
        />
      </section>
    </div>
  );
}

function RecoveryDomainRows({
  records,
  empty,
  onDelete,
}: {
  records: RecoveryDomainRecord[];
  empty: string;
  onDelete: (id: string) => void;
}) {
  if (records.length === 0) return <EmptyRow label={empty} />;
  return (
    <ul className="m-0 grid list-none gap-2 p-0">
      {records.map((record) => {
        const state = normalizeRecoveryDomainState(record);
        return (
          <li
            key={record.id}
            className="grid gap-2 rounded-lg border border-line bg-surface-2 px-4 py-3"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="break-all font-mono text-sm text-text">
                {record.hostname}
              </span>
              <div className="flex items-center gap-2">
                <StatusPill status={state.pillStatus} />
                <span className="text-[11px] text-muted">{state.label}</span>
                <button
                  type="button"
                  onClick={() => onDelete(record.id)}
                  className="text-[11px] text-danger hover:underline"
                >
                  Remove
                </button>
              </div>
            </div>
            <p className="m-0 text-xs text-muted">{state.description}</p>
            {record.dns_challenge ? (
              <div className="grid gap-1 rounded border border-line bg-bg-2 p-2 text-xs">
                <strong className="text-text">DNS TXT ownership record</strong>
                <code className="break-all text-muted">
                  {record.dns_challenge.record_name}
                </code>
                <code className="break-all text-muted">
                  {record.dns_challenge.record_value}
                </code>
              </div>
            ) : null}
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
