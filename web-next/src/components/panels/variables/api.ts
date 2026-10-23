// Durable environment-changeset transport for the Variables editor. The local
// store is only an input buffer: Save encrypts and persists the complete draft
// through revision CAS, then clears plaintext from the tab. Apply is a separate
// canonical transaction driven by the environment Changes bar.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type EnvironmentChangesetInput } from "@/api/client";
import { queryKeys } from "@/api/hooks";
import {
  clearStagedChanges,
  stagedChangeKind,
  type StagedChange,
} from "@/lib/stagedChanges";

export type CommitInput = {
  changes: StagedChange[];
  /** Operator note used only as non-secret local context. */
  message?: string;
};

export type CommitResult = {
  applied: StagedChange[];
  affectedServices: string[];
};

/**
 * Persist the complete input buffer as one durable draft. Every update/delete
 * carries the version captured by the editor. No desired state changes here.
 */
export async function commitStagedChanges(
  input: CommitInput,
  _projectId?: string,
): Promise<CommitResult> {
  const ordered = [...input.changes].sort(
    (a, b) =>
      a.serviceId.localeCompare(b.serviceId) ||
      stagedChangeKind(a).localeCompare(stagedChangeKind(b)) ||
      a.name.localeCompare(b.name),
  );
  const environmentId = ordered[0]?.environmentId;
  if (!environmentId || ordered.some((change) => change.environmentId !== environmentId)) {
    throw new Error("A draft can only cover one environment at a time.");
  }
  const current = await api.getEnvironmentChangeset(environmentId);
  if (current.changeset?.state === "draft") {
    throw new Error(
      "This environment already has a saved draft. Apply or discard it before saving another.",
    );
  }
  const changes: EnvironmentChangesetInput[] = ordered.map(toChangesetInput);
  await api.putEnvironmentChangeset(
    environmentId,
    current.changeset?.revision ?? 0,
    changes,
    input.message,
  );

  return {
    applied: ordered,
    affectedServices: Array.from(new Set(ordered.map((c) => c.serviceId))),
  };
}

function toChangesetInput(change: StagedChange): EnvironmentChangesetInput {
  const kind = stagedChangeKind(change);
  if (change.kind === undefined || change.kind === "variable") {
    if (change.op !== "add" && (!change.base?.id || change.base.version < 1)) {
      throw new Error(
        `Cannot stage ${change.name}: its server version is unavailable. Refresh and try again.`,
      );
    }
    return {
      kind: "variable",
      operation: change.op,
      service_id: change.serviceId,
      target_id: change.base?.id,
      name: change.name,
      scope: change.scope,
      expected_version: change.base?.version,
      value: change.op === "remove" ? undefined : change.value,
    };
  }
  if (change.kind === "runtime") {
    return {
      kind,
      operation: change.op,
      service_id: change.serviceId,
      target_id: change.targetId,
      name: change.name,
      expected_version: change.expectedVersion,
      runtime: change.runtime,
    };
  }
  if (change.kind === "networking") {
    return {
      kind,
      operation: change.op,
      service_id: change.serviceId,
      target_id: change.targetId,
      name: change.name,
      expected_version: change.expectedVersion,
      networking: change.networking,
    };
  }
  if (change.kind === "storage") {
    return {
      kind,
      operation: change.op,
      service_id: change.serviceId,
      target_id: change.targetId,
      name: change.name,
      expected_version: change.expectedVersion,
      storage: change.storage,
    };
  }
  throw new Error("Unsupported staged change kind.");
}

function invalidateApplied(
  queryClient: ReturnType<typeof useQueryClient>,
  changes: readonly StagedChange[],
) {
  const slots = new Set<string>();
  for (const change of changes) {
    const slot = `${change.serviceId}\u0000${change.environmentId}`;
    if (slots.has(slot)) continue;
    slots.add(slot);
    const environmentId = change.environmentId || undefined;
    void queryClient.invalidateQueries({
      queryKey: queryKeys.variables(change.serviceId, environmentId),
    });
  }
}

export function useCommitStagedChanges(projectId?: string) {
  const queryClient = useQueryClient();
  return useMutation<CommitResult, Error, CommitInput>({
    mutationFn: (input) => commitStagedChanges(input, projectId),
    onSuccess: (result) => {
      clearStagedChanges(result.applied);
      invalidateApplied(queryClient, result.applied);
      const environmentId = result.applied[0]?.environmentId;
      if (environmentId) {
        void queryClient.invalidateQueries({
          queryKey: queryKeys.environmentChangeset(environmentId),
        });
      }
    },
  });
}
