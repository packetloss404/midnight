import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, apiFetch } from "@/api/client";
import { stageChange } from "@/lib/stagedChanges";

export type VolumeRecord = {
  id: string;
  project_id: string;
  environment_id: string;
  service_id: string;
  name: string;
  size_bytes: number;
  status:
    | "provisioning"
    | "ready"
    | "attached"
    | "restore_staged"
    | "restore_retained"
    | "soft_deleted"
    | "deleted";
  mount_path?: string;
  created_at: string;
  updated_at: string;
  resource_version: number;
};

export type VolumeBackupRun = {
  id: string;
  volume_id: string;
  policy_id?: string;
  trigger_kind: "manual" | "daily" | "weekly" | "monthly";
  state: "pending" | "running" | "succeeded" | "failed" | "deleted";
  object_sha256?: string;
  source_sha256?: string;
  object_bytes: number;
  source_bytes: number;
  failure_code?: string;
  failure_detail?: string;
  created_at: string;
  completed_at?: string;
};

export type VolumeRestoreRun = {
  id: string;
  backup_id: string;
  source_volume_id: string;
  state: "pending" | "running" | "verified" | "staged" | "failed" | "applied";
  replacement_volume_id?: string;
  replacement_volume_name?: string;
  source_retained: boolean;
  attachment_swap_staged: boolean;
  restored_sha256?: string;
  restored_bytes: number;
  failure_code?: string;
  failure_detail?: string;
  created_at: string;
  completed_at?: string;
};

export type VolumeBackupPolicy = {
  id: string;
  volume_id: string;
  schedule_kind: "daily" | "weekly" | "monthly";
  retention_seconds: number;
  enabled: boolean;
  next_run_at: string;
  last_run_at?: string;
};

export type VolumeRecoveryEntry = {
  volume: VolumeRecord;
  backups: VolumeBackupRun[];
  restores: VolumeRestoreRun[];
  policies: VolumeBackupPolicy[];
  capabilities: {
    backups: boolean;
    restores: boolean;
    policies: boolean;
  };
};

export type VolumeRecoveryData = { entries: VolumeRecoveryEntry[] };

export type CreateVolumeInput = {
  name: string;
  mount_path: string;
  size_bytes: number;
};

const keys = {
  recovery: (projectId: string, environmentId: string, serviceId: string) =>
    ["volume-recovery", projectId, environmentId, serviceId] as const,
};

function mutationKey(prefix: string): string {
  try {
    return `${prefix}-${crypto.randomUUID()}`;
  } catch {
    return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

export async function fetchVolumeRecovery(
  projectId: string,
  environmentId: string,
  serviceId: string,
  signal?: AbortSignal,
): Promise<VolumeRecoveryData> {
  const query = new URLSearchParams({
    project_id: projectId,
    environment_id: environmentId,
    service_id: serviceId,
  });
  const volumes = await apiFetch<VolumeRecord[]>(`/volumes?${query}`, { signal });
  const entries = await Promise.all(
    volumes.map(async (volume) => {
      const id = encodeURIComponent(volume.id);
      const [backupList, restores, policies] = await Promise.all([
        optionalCapability(() =>
          apiFetch<{ data: VolumeBackupRun[] }>(`/volumes/${id}/backups`, { signal }),
        ),
        optionalCapability(() =>
          apiFetch<{ data: VolumeRestoreRun[] }>(`/volumes/${id}/restores`, { signal }),
        ),
        optionalCapability(() =>
          apiFetch<VolumeBackupPolicy[]>(`/volumes/${id}/backup-policies`, { signal }),
        ),
      ]);
      return {
        volume,
        backups: backupList.value?.data ?? [],
        restores: restores.value?.data ?? [],
        policies: policies.value ?? [],
        capabilities: {
          backups: backupList.available,
          restores: restores.available,
          policies: policies.available,
        },
      };
    }),
  );
  return { entries };
}

async function optionalCapability<T>(
  request: () => Promise<T>,
): Promise<{ available: boolean; value?: T }> {
  try {
    return { available: true, value: await request() };
  } catch (error) {
    if (error instanceof ApiError && error.status === 501) {
      return { available: false };
    }
    throw error;
  }
}

export function createVolumeBackup(
  volumeId: string,
  retentionDays: number,
  idempotencyKey: string,
): Promise<{ run: VolumeBackupRun; job_id: string }> {
  return apiFetch(`/volumes/${encodeURIComponent(volumeId)}/backups`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: { retention_days: retentionDays },
  });
}

export function applyVolumeRestore(
  restoreId: string,
  idempotencyKey: string,
): Promise<{
  restore: VolumeRestoreRun;
  deployment_action: { outbox_id: string; created: boolean };
  created: boolean;
}> {
  return apiFetch(`/volume-restores/${encodeURIComponent(restoreId)}/apply`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: {},
  });
}

export function useVolumeRecovery(
  projectId?: string,
  environmentId?: string,
  serviceId?: string,
) {
  return useQuery<VolumeRecoveryData, Error>({
    queryKey: keys.recovery(projectId ?? "", environmentId ?? "", serviceId ?? ""),
    enabled: Boolean(projectId && environmentId && serviceId),
    queryFn: ({ signal }) =>
      fetchVolumeRecovery(projectId!, environmentId!, serviceId!, signal),
    refetchInterval: (query) => {
      const active = query.state.data?.entries.some(
        (entry) =>
          entry.backups.some((run) => run.state === "pending" || run.state === "running") ||
          entry.restores.some((run) => run.state === "pending" || run.state === "running"),
      );
      return active ? 5_000 : false;
    },
  });
}

function useRecoveryMutation<TInput, TOutput>(
  projectId: string,
  environmentId: string,
  serviceId: string,
  mutationFn: (input: TInput) => Promise<TOutput>,
) {
  const client = useQueryClient();
  return useMutation<TOutput, Error, TInput>({
    mutationFn,
    onSuccess: () => {
      client.invalidateQueries({ queryKey: keys.recovery(projectId, environmentId, serviceId) });
      client.invalidateQueries({
        queryKey: ["environment-changeset", environmentId],
      });
      client.invalidateQueries({ queryKey: ["environment-resources", environmentId] });
      client.invalidateQueries({ queryKey: ["deployments", serviceId] });
    },
  });
}

export function useCreateVolume(
  projectId: string,
  environmentId: string,
  serviceId: string,
) {
  return useRecoveryMutation<CreateVolumeInput, void>(
    projectId,
    environmentId,
    serviceId,
    async (input) => {
      stageVolumeCreate(environmentId, serviceId, input);
    },
  );
}

export function stageVolumeCreate(
  environmentId: string,
  serviceId: string,
  input: CreateVolumeInput,
): void {
  stageChange({
    kind: "storage",
    serviceId,
    environmentId,
    name: input.name,
    op: "add",
    storage: {
      size_bytes: input.size_bytes,
      mount_path: input.mount_path,
    },
  });
}

export function useCreateVolumeBackup(projectId: string, environmentId: string, serviceId: string) {
  return useRecoveryMutation<{ volumeId: string; retentionDays: number }, { run: VolumeBackupRun; job_id: string }>(
    projectId,
    environmentId,
    serviceId,
    ({ volumeId, retentionDays }) =>
      createVolumeBackup(volumeId, retentionDays, mutationKey("volume-backup")),
  );
}

export function useCreateBackupPolicy(projectId: string, environmentId: string, serviceId: string) {
  return useRecoveryMutation<
    { volumeId: string; schedule: VolumeBackupPolicy["schedule_kind"]; retentionDays: number },
    VolumeBackupPolicy
  >(projectId, environmentId, serviceId, ({ volumeId, schedule, retentionDays }) =>
    apiFetch(`/volumes/${encodeURIComponent(volumeId)}/backup-policies`, {
      method: "POST",
      headers: { "Idempotency-Key": mutationKey("volume-policy") },
      body: { schedule_kind: schedule, retention_days: retentionDays },
    }),
  );
}

export function useDeleteBackupPolicy(projectId: string, environmentId: string, serviceId: string) {
  return useRecoveryMutation<{ volumeId: string; policyId: string }, void>(
    projectId,
    environmentId,
    serviceId,
    ({ volumeId, policyId }) =>
      apiFetch(
        `/volumes/${encodeURIComponent(volumeId)}/backup-policies/${encodeURIComponent(policyId)}`,
        {
          method: "DELETE",
          headers: { "Idempotency-Key": mutationKey("volume-policy-delete") },
        },
      ),
  );
}

export function useCreateVolumeRestore(projectId: string, environmentId: string, serviceId: string) {
  return useRecoveryMutation<{ backupId: string }, { run: VolumeRestoreRun; job_id: string }>(
    projectId,
    environmentId,
    serviceId,
    ({ backupId }) =>
      apiFetch(`/volume-backups/${encodeURIComponent(backupId)}/restores`, {
        method: "POST",
        headers: { "Idempotency-Key": mutationKey("volume-restore") },
        body: {},
      }),
  );
}

export function useApplyVolumeRestore(projectId: string, environmentId: string, serviceId: string) {
  return useRecoveryMutation<
    { restoreId: string },
    { restore: VolumeRestoreRun; deployment_action: { outbox_id: string; created: boolean }; created: boolean }
  >(projectId, environmentId, serviceId, ({ restoreId }) =>
    applyVolumeRestore(restoreId, mutationKey("volume-restore-apply")),
  );
}
