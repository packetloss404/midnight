import { useState, type FormEvent } from "react";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { ErrorState, Loading } from "@/components/states";
import { presentError } from "@/lib/errorPresentation";
import {
  useApplyVolumeRestore,
  useCreateBackupPolicy,
  useCreateVolume,
  useCreateVolumeBackup,
  useCreateVolumeRestore,
  useDeleteBackupPolicy,
  useVolumeRecovery,
  type VolumeBackupPolicy,
  type VolumeRecoveryEntry,
} from "./storage/api";

type Props = {
  projectId?: string;
  environmentId?: string;
  serviceId: string;
  recoveryAvailable?: boolean;
};

export function VolumeRecoveryPanel({
  projectId,
  environmentId,
  serviceId,
  recoveryAvailable = true,
}: Props) {
  if (!projectId || !environmentId) {
    return (
      <CapabilityBoundary
        state="unavailable"
        title="Persistent storage"
        detail="Choose a project environment before managing storage and restores."
      />
    );
  }
  if (!recoveryAvailable) {
    return (
      <ScopedStagedStorage
        projectId={projectId}
        environmentId={environmentId}
        serviceId={serviceId}
      />
    );
  }
  return (
    <ScopedVolumeRecovery
      projectId={projectId}
      environmentId={environmentId}
      serviceId={serviceId}
    />
  );
}

function ScopedStagedStorage({
  projectId,
  environmentId,
  serviceId,
}: Required<Pick<Props, "projectId" | "environmentId" | "serviceId">>) {
  const createVolume = useCreateVolume(projectId, environmentId, serviceId);
  const [showCreate, setShowCreate] = useState(false);
  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="m-0 text-sm font-semibold text-text">Persistent storage</h3>
          <p className="mt-1 text-xs text-muted">
            Add a volume to your draft. It is created when you apply the changes.
          </p>
        </div>
        <button type="button" className={buttonClass} onClick={() => setShowCreate((value) => !value)}>
          {showCreate ? "Cancel" : "Stage volume"}
        </button>
      </div>
      {showCreate ? (
        <CreateVolumeForm
          pending={createVolume.isPending}
          error={createVolume.error}
          onSubmit={(input) =>
            createVolume.mutate(input, { onSuccess: () => setShowCreate(false) })
          }
        />
      ) : null}
      <CapabilityBoundary
        state="degraded"
        title="Applied volumes and recovery"
        detail="Existing volume, backup, and restore records still require recovery authority on this server. Midnight did not issue an installation-wide storage request from this project session."
      />
    </div>
  );
}

function ScopedVolumeRecovery({
  projectId,
  environmentId,
  serviceId,
}: Required<Pick<Props, "projectId" | "environmentId" | "serviceId">>) {
  const recovery = useVolumeRecovery(projectId, environmentId, serviceId);
  const createVolume = useCreateVolume(projectId, environmentId, serviceId);
  const [showCreate, setShowCreate] = useState(false);

  if (recovery.isPending) return <Loading label="Loading persistent storage…" />;
  if (recovery.error) {
    return (
      <ErrorState
        title="Failed to load persistent storage"
        error={recovery.error}
        onRetry={() => recovery.refetch()}
      />
    );
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="m-0 text-sm font-semibold text-text">Persistent storage</h3>
          <p className="mt-1 text-xs text-muted">
            Add or change a mount in your draft. Backups and restores act on volumes that
            have already been applied.
          </p>
        </div>
        <button type="button" className={buttonClass} onClick={() => setShowCreate((value) => !value)}>
          {showCreate ? "Cancel" : "Stage volume"}
        </button>
      </div>

      {showCreate ? (
        <CreateVolumeForm
          pending={createVolume.isPending}
          error={createVolume.error}
          onSubmit={(input) =>
            createVolume.mutate(input, { onSuccess: () => setShowCreate(false) })
          }
        />
      ) : null}

      {recovery.data?.entries.length ? (
        recovery.data.entries.map((entry) => (
          <VolumeCard
            key={entry.volume.id}
            entry={entry}
            projectId={projectId}
            environmentId={environmentId}
            serviceId={serviceId}
          />
        ))
      ) : (
        <div className="rounded-lg border border-dashed border-line px-4 py-6 text-center text-sm text-muted">
          No persistent volume is attached to this service and environment.
        </div>
      )}
    </div>
  );
}

function CreateVolumeForm({
  pending,
  error,
  onSubmit,
}: {
  pending: boolean;
  error: Error | null;
  onSubmit: (input: { name: string; mount_path: string; size_bytes: number }) => void;
}) {
  const [name, setName] = useState("data");
  const [mountPath, setMountPath] = useState("/data");
  const [sizeGiB, setSizeGiB] = useState(1);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSubmit({
      name: name.trim(),
      mount_path: mountPath.trim(),
      size_bytes: Math.round(sizeGiB * 1024 * 1024 * 1024),
    });
  };
  return (
    <form onSubmit={submit} className="grid gap-3 rounded-lg border border-line bg-bg-2 p-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <label className={labelClass}>
          Name
          <input className="field-input" value={name} onChange={(event) => setName(event.target.value)} required />
        </label>
        <label className={labelClass}>
          Mount path
          <input className="field-input font-mono" value={mountPath} onChange={(event) => setMountPath(event.target.value)} pattern="/.*" required />
        </label>
        <label className={labelClass}>
          Capacity (GiB)
          <input className="field-input" type="number" min={0.1} step={0.1} value={sizeGiB} onChange={(event) => setSizeGiB(Number(event.target.value))} required />
        </label>
      </div>
      {error ? <MutationError error={error} /> : null}
      <button type="submit" className={`${buttonClass} w-fit`} disabled={pending}>
        {pending ? "Staging…" : "Stage volume intent"}
      </button>
    </form>
  );
}

function VolumeCard({
  entry,
  projectId,
  environmentId,
  serviceId,
}: {
  entry: VolumeRecoveryEntry;
  projectId: string;
  environmentId: string;
  serviceId: string;
}) {
  const backup = useCreateVolumeBackup(projectId, environmentId, serviceId);
  const createPolicy = useCreateBackupPolicy(projectId, environmentId, serviceId);
  const deletePolicy = useDeleteBackupPolicy(projectId, environmentId, serviceId);
  const restore = useCreateVolumeRestore(projectId, environmentId, serviceId);
  const apply = useApplyVolumeRestore(projectId, environmentId, serviceId);
  const [schedule, setSchedule] = useState<VolumeBackupPolicy["schedule_kind"]>("daily");
  const [retentionDays, setRetentionDays] = useState(30);
  const capabilities = entry.capabilities;
  const mutationError =
    backup.error ?? createPolicy.error ?? deletePolicy.error ?? restore.error ?? apply.error;

  return (
    <article className="grid gap-4 rounded-lg border border-line bg-surface p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <strong className="text-sm text-text">{entry.volume.name}</strong>
            <StatusPill value={entry.volume.status} />
          </div>
          <p className="mt-1 font-mono text-xs text-muted">
            {entry.volume.mount_path || "mount pending"} · {formatBytes(entry.volume.size_bytes)}
          </p>
        </div>
        <button
          type="button"
          className={buttonClass}
          disabled={
            !capabilities.backups ||
            backup.isPending ||
            !["ready", "attached"].includes(entry.volume.status)
          }
          onClick={() => backup.mutate({ volumeId: entry.volume.id, retentionDays })}
        >
          {!capabilities.backups
            ? "Backup unavailable"
            : backup.isPending
              ? "Queueing…"
              : "Back up now"}
        </button>
      </div>

      <section className="grid gap-2 border-t border-line pt-3">
        <strong className="text-xs uppercase tracking-wide text-subtle">Schedule</strong>
        {capabilities.policies ? (
          <div className="flex flex-wrap items-end gap-2">
            <label className={labelClass}>
              Frequency
              <select className="field-input" value={schedule} onChange={(event) => setSchedule(event.target.value as VolumeBackupPolicy["schedule_kind"])}>
                <option value="daily">Daily</option>
                <option value="weekly">Weekly</option>
                <option value="monthly">Monthly</option>
              </select>
            </label>
            <label className={labelClass}>
              Retention days
              <input className="field-input w-28" type="number" min={1} max={3650} value={retentionDays} onChange={(event) => setRetentionDays(Number(event.target.value))} />
            </label>
            <button
              type="button"
              className={buttonClass}
              disabled={createPolicy.isPending}
              onClick={() => createPolicy.mutate({ volumeId: entry.volume.id, schedule, retentionDays })}
            >
              Add policy
            </button>
          </div>
        ) : (
          <span className="text-xs text-muted">
            Scheduled backups are unavailable in this API mode.
          </span>
        )}
        {capabilities.policies && entry.policies.length ? (
          <div className="grid gap-1">
            {entry.policies.map((policy) => (
              <div key={policy.id} className="flex flex-wrap items-center justify-between gap-2 rounded border border-line bg-bg-2 px-3 py-2 text-xs">
                <span>
                  <strong className="capitalize text-text">{policy.schedule_kind}</strong>{" "}
                  <span className="text-muted">· keep {Math.round(policy.retention_seconds / 86400)}d · next {formatDate(policy.next_run_at)}</span>
                </span>
                <button type="button" className="text-danger hover:underline" onClick={() => deletePolicy.mutate({ volumeId: entry.volume.id, policyId: policy.id })}>
                  Remove
                </button>
              </div>
            ))}
          </div>
        ) : capabilities.policies ? (
          <span className="text-xs text-muted">No automatic backup policy.</span>
        ) : null}
      </section>

      <section className="grid gap-2 border-t border-line pt-3">
        <strong className="text-xs uppercase tracking-wide text-subtle">Backup history</strong>
        {!capabilities.backups ? (
          <span className="text-xs text-muted">
            Backup execution is unavailable in this API mode. The volume record remains visible.
          </span>
        ) : entry.backups.length ? (
          entry.backups.map((run) => (
            <div key={run.id} className="flex flex-wrap items-center justify-between gap-2 rounded border border-line bg-bg-2 px-3 py-2 text-xs">
              <span>
                <StatusPill value={run.state} />{" "}
                <span className="font-mono text-muted">{run.id}</span>{" "}
                <span className="text-muted">· {run.trigger_kind} · {formatDate(run.created_at)}</span>
              </span>
              {run.state === "succeeded" ? (
                <button type="button" className={buttonClass} disabled={restore.isPending} onClick={() => restore.mutate({ backupId: run.id })}>
                  Restore to replacement
                </button>
              ) : run.failure_detail ? (
                <span className="text-danger">{run.failure_code || "Backup failed"}</span>
              ) : null}
            </div>
          ))
        ) : (
          <span className="text-xs text-muted">No backup has been queued.</span>
        )}
      </section>

      <section className="grid gap-2 border-t border-line pt-3">
        <strong className="text-xs uppercase tracking-wide text-subtle">Restore history</strong>
        <p className="m-0 text-xs text-muted">
          A restore writes to a new volume and leaves the original untouched. Applying it queues a redeploy, and the service switches over when that deployment finishes.
        </p>
        {!capabilities.restores ? (
          <span className="text-xs text-muted">
            Restore execution is unavailable in this API mode.
          </span>
        ) : entry.restores.length ? (
          entry.restores.map((run) => (
            <div key={run.id} className="flex flex-wrap items-center justify-between gap-2 rounded border border-line bg-bg-2 px-3 py-2 text-xs">
              <span>
                <StatusPill value={run.state} />{" "}
                <span className="font-mono text-muted">{run.id}</span>{" "}
                {run.replacement_volume_id ? <span className="text-muted">· replacement {run.replacement_volume_id}</span> : null}
              </span>
              {run.state === "staged" ? (
                <button type="button" className={buttonClass} disabled={apply.isPending} onClick={() => apply.mutate({ restoreId: run.id })}>
                  Apply & redeploy
                </button>
              ) : run.state === "applied" ? (
                <span className="text-accent">Source retained · redeploy handed off</span>
              ) : run.failure_detail ? (
                <span className="text-danger">{run.failure_code || "Restore failed"}</span>
              ) : null}
            </div>
          ))
        ) : (
          <span className="text-xs text-muted">No restore has been queued.</span>
        )}
      </section>

      {mutationError ? <MutationError error={mutationError} /> : null}
    </article>
  );
}

function StatusPill({ value }: { value: string }) {
  const danger = value === "failed" || value === "deleted";
  const success = value === "succeeded" || value === "ready" || value === "attached" || value === "applied";
  return (
    <span className={`inline-flex rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${danger ? "border-danger/40 bg-danger-soft text-danger" : success ? "border-accent/40 bg-accent-soft text-accent" : "border-line bg-surface-2 text-muted"}`}>
      {value.replaceAll("_", " ")}
    </span>
  );
}

function MutationError({ error }: { error: Error }) {
  const detail = presentError(error);
  return (
    <div role="alert" className="rounded border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger">
      <strong>{detail.message}</strong>
      {detail.recovery ? <span> · {detail.recovery}</span> : null}
    </div>
  );
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const exp = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** exp).toFixed(exp === 0 ? 0 : 1)} ${units[exp]}`;
}

function formatDate(value?: string) {
  if (!value) return "not scheduled";
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleString();
}

const buttonClass =
  "rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-3 py-1.5 text-xs font-semibold text-text hover:bg-surface-3 disabled:cursor-not-allowed disabled:opacity-50";
const labelClass = "grid gap-1 text-xs font-medium text-muted";
