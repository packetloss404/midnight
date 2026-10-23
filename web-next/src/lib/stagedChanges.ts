// Tab-local input buffer for one atomic environment changeset. Variable,
// runtime, networking, and storage editors all accumulate here before the
// Operations tray performs one explicit durable save.
//
// Shape follows `AgentPanel/agent-dock-state.ts`: a module-scope store read
// through `useSyncExternalStore`, no context provider, no prop-drilling. The
// Resource editors write changes; the floating StagedChangesBanner (mounted
// once on the canvas) reads the aggregate across every service in the
// environment and drives the commit.
//
// IN-MEMORY ONLY. Staged values include plaintext for SECRET/SEALED
// variables, so nothing here is persisted to localStorage/sessionStorage.

import { useMemo, useSyncExternalStore } from "react";
import type {
  EnvironmentNetworkingSpec,
  EnvironmentRuntimeSpec,
  EnvironmentStorageSpec,
} from "@/api/client";

// Lowercase scope matches the REST `VariableMeta.scope` contract.
export type VarScope = "plain" | "secret" | "sealed";

export type StagedOp = "add" | "edit" | "remove";

/** Variable metadata snapshot captured when a change is staged. */
export type StagedBaseline = {
  id: string;
  workspaceId: string;
  projectId: string;
  environmentId: string;
  serviceId: string;
  scope: VarScope;
  version: number;
  lengthBucket: string;
  /** True when the variable is write-only (SEALED) — no old value to show. */
  sealed: boolean;
};

type StagedChangeBase = {
  serviceId: string;
  /** Normalised: empty string means "no environment scope". */
  environmentId: string;
  name: string;
  op: StagedOp;
};

export type StagedVariableChange = StagedChangeBase & {
  kind?: "variable";
  scope: VarScope;
  /** New plaintext value. Empty for `remove`. */
  value: string;
  /** Present for `edit`/`remove` (variable existed server-side); absent for `add`. */
  base?: StagedBaseline;
};

export type StagedRuntimeChange = StagedChangeBase & {
  kind: "runtime";
  op: "add" | "edit";
  targetId?: string;
  expectedVersion?: number;
  runtime: EnvironmentRuntimeSpec;
};

export type StagedNetworkingChange = StagedChangeBase & {
  kind: "networking";
  targetId?: string;
  expectedVersion?: number;
  networking: EnvironmentNetworkingSpec;
};

export type StagedStorageChange = StagedChangeBase & {
  kind: "storage";
  targetId?: string;
  expectedVersion?: number;
  storage?: EnvironmentStorageSpec;
};

export type StagedChange =
  | StagedVariableChange
  | StagedRuntimeChange
  | StagedNetworkingChange
  | StagedStorageChange;

export function stagedChangeKind(change: StagedChange): NonNullable<StagedChange["kind"]> {
  return change.kind ?? "variable";
}

export function isStagedVariableChange(
  change: StagedChange,
): change is StagedVariableChange {
  return change.kind === undefined || change.kind === "variable";
}

// ── store internals ────────────────────────────────────────────────

const SEP = "\u0000";

function normEnv(env?: string): string {
  return env ?? "";
}

function composeKey(
  serviceId: string,
  environmentId: string,
  kind: string,
  name: string,
): string {
  return `${serviceId}${SEP}${environmentId}${SEP}${kind}${SEP}${name}`;
}

/** Stable grouping key for a service+env "cache slot" (used for equality). */
export function stagedScopeKey(serviceId: string, environmentId?: string): string {
  return `${serviceId}${SEP}${normEnv(environmentId)}`;
}

const store = new Map<string, StagedChange>();
let version = 0;

type Listener = () => void;
const listeners = new Set<Listener>();

function emit() {
  version += 1;
  for (const fn of listeners) fn();
}

export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

function getVersion(): number {
  return version;
}

// ── mutations ──────────────────────────────────────────────────────

/**
 * Stage (or replace) one change. Keyed by service+environment+kind+name, so
 * restaging the same resource overwrites the previous edit rather than
 * stacking while different resource kinds can share a display name.
 */
export function stageChange(change: StagedChange): void {
  const key = composeKey(
    change.serviceId,
    change.environmentId,
    stagedChangeKind(change),
    change.name,
  );
  store.set(key, change);
  emit();
}

/** Discard a single pending change, reverting the row to its server state. */
export function discardChange(
  serviceId: string,
  environmentId: string | undefined,
  name: string,
  kind = "variable",
): void {
  const key = composeKey(serviceId, normEnv(environmentId), kind, name);
  if (store.delete(key)) emit();
}

/** Discard every pending change for one service+env. */
export function clearStagedForService(
  serviceId: string,
  environmentId?: string,
): void {
  const env = normEnv(environmentId);
  let changed = false;
  for (const [key, c] of store) {
    if (c.serviceId === serviceId && c.environmentId === env) {
      store.delete(key);
      changed = true;
    }
  }
  if (changed) emit();
}

/** Discard a specific set of changes (used after a successful commit). */
export function clearStagedChanges(changes: readonly StagedChange[]): void {
  let changed = false;
  for (const c of changes) {
    const key = composeKey(
      c.serviceId,
      c.environmentId,
      stagedChangeKind(c),
      c.name,
    );
    // A user can keep editing while a network save is in flight. Only clear
    // the exact object that was submitted; a newer draft at the same key must
    // survive the older request's completion.
    if (store.get(key) === c && store.delete(key)) changed = true;
  }
  if (changed) emit();
}

/**
 * Drop every tab-local draft when mutation authority is lost. Drafts can hold
 * secret plaintext, so they must not survive a transition into human-only
 * mode where the save transport is intentionally unavailable.
 */
export function clearAllStagedChanges(): void {
  if (store.size === 0) return;
  store.clear();
  emit();
}

// ── reads ──────────────────────────────────────────────────────────

export function getStagedForService(
  serviceId: string,
  environmentId?: string,
): StagedChange[] {
  const env = normEnv(environmentId);
  const out: StagedChange[] = [];
  for (const c of store.values()) {
    if (c.serviceId === serviceId && c.environmentId === env) out.push(c);
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** All pending changes across every service in one environment. */
export function getStagedForEnv(environmentId?: string): StagedChange[] {
  const env = normEnv(environmentId);
  const out: StagedChange[] = [];
  for (const c of store.values()) {
    if (c.environmentId === env) out.push(c);
  }
  return out.sort(
    (a, b) => a.serviceId.localeCompare(b.serviceId) || a.name.localeCompare(b.name),
  );
}

export function getAllStaged(): StagedChange[] {
  return Array.from(store.values());
}

// ── hooks ──────────────────────────────────────────────────────────

/** Pending changes for every editor in a single service and environment. */
export function useStagedForService(
  serviceId: string,
  environmentId?: string,
): StagedChange[] {
  const v = useSyncExternalStore(subscribe, getVersion, getVersion);
  return useMemo(
    () => getStagedForService(serviceId, environmentId),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [v, serviceId, environmentId],
  );
}

/** Aggregate pending changes for an environment (the StagedChangesBanner). */
export function useStagedForEnv(environmentId?: string): StagedChange[] {
  const v = useSyncExternalStore(subscribe, getVersion, getVersion);
  return useMemo(
    () => getStagedForEnv(environmentId),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [v, environmentId],
  );
}

// ── test seam ──────────────────────────────────────────────────────

export function __resetStagedChangesForTests(): void {
  clearAllStagedChanges();
}
