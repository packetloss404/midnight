// Durable per-project/environment topology coordinates with a local fallback.
// The server is authoritative when available; localStorage keeps the current
// editing session useful during an outage and migrates older browser layouts.

import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, api, type Service, type TopologyLayout } from "@/api/client";

export type NodePosition = { x: number; y: number };
export type PositionMap = Record<string, NodePosition>;
export type LayoutPersistenceState = "loading" | "saved" | "saving" | "local" | "conflict";

const STAGE_PAD_X = 32;
const STAGE_PAD_Y = 56;
const GUTTER_X = 80;
const GUTTER_Y = 40;
const COLUMNS = 4;
const NODE_W = 240;
const NODE_H = 132;
const SAVE_DEBOUNCE_MS = 450;

function storageKey(projectId: string | undefined, envId: string | undefined): string {
  return `positions:${projectId ?? "unknown"}:${envId ?? "all"}`;
}

function autoSlot(index: number, columns = COLUMNS): NodePosition {
  const col = index % columns;
  const row = Math.floor(index / columns);
  return {
    x: STAGE_PAD_X + col * (NODE_W + GUTTER_X),
    y: STAGE_PAD_Y + row * (NODE_H + GUTTER_Y),
  };
}

/** Deterministic row-major layout; callers choose columns for viewport size. */
export function autoLayout(services: Service[], columns = COLUMNS): PositionMap {
  return Object.fromEntries(
    services.map((service, index) => [service.id, autoSlot(index, Math.max(1, columns))]),
  );
}

function loadPositions(projectId: string | undefined, envId: string | undefined): PositionMap {
  if (typeof window === "undefined") return {};
  try {
    const raw = window.localStorage.getItem(storageKey(projectId, envId));
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, { x?: unknown; y?: unknown }>;
    const out: PositionMap = {};
    for (const [id, position] of Object.entries(parsed ?? {})) {
      if (typeof position?.x === "number" && typeof position?.y === "number") {
        out[id] = { x: position.x, y: position.y };
      }
    }
    return out;
  } catch {
    return {};
  }
}

function savePositions(projectId: string | undefined, envId: string | undefined, positions: PositionMap) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(storageKey(projectId, envId), JSON.stringify(positions));
  } catch {
    // Browser storage is an optional fallback; in-memory editing still works.
  }
}

function fromLayout(layout: TopologyLayout): PositionMap {
  return Object.fromEntries(layout.nodes.map((node) => [node.service_id, { x: node.x, y: node.y }]));
}

function toNodes(positions: PositionMap) {
  return Object.entries(positions).map(([service_id, position]) => ({
    service_id,
    x: Math.round(position.x),
    y: Math.round(position.y),
  }));
}

export function useNodePositions(
  projectId: string | undefined,
  envId: string | undefined,
  services: Service[],
): {
  positions: PositionMap;
  move: (serviceId: string, next: NodePosition) => void;
  replace: (next: PositionMap) => void;
  reset: () => void;
  persistence: LayoutPersistenceState;
} {
  const key = storageKey(projectId, envId);
  const enabled = Boolean(projectId && envId);
  const remote = useQuery({
    queryKey: ["topology-layout", projectId ?? null, envId ?? null],
    queryFn: ({ signal }) => api.getTopologyLayout(projectId!, envId!, signal),
    enabled,
    retry: false,
    staleTime: 30_000,
  });
  const [positions, setPositions] = useState<PositionMap>(() =>
    reconcile(loadPositions(projectId, envId), services),
  );
  const [persistence, setPersistence] = useState<LayoutPersistenceState>(enabled ? "loading" : "local");
  const versionRef = useRef(0);
  const dirtyRef = useRef(false);
	const dirtyServiceIDsRef = useRef(new Set<string>());
	const replaceAllRef = useRef(false);
  const revisionRef = useRef(0);
  const keyRef = useRef(key);

  useEffect(() => {
    if (keyRef.current === key) return;
    keyRef.current = key;
    dirtyRef.current = false;
	dirtyServiceIDsRef.current.clear();
	replaceAllRef.current = false;
    revisionRef.current = 0;
    versionRef.current = 0;
    setPositions(reconcile(loadPositions(projectId, envId), services));
    setPersistence(enabled ? "loading" : "local");
  }, [enabled, envId, key, projectId, services]);

  useEffect(() => {
    if (!remote.data || dirtyRef.current) return;
	if (remote.data.resource_version < versionRef.current) return;
    versionRef.current = remote.data.resource_version;
    setPositions(reconcile(fromLayout(remote.data), services));
    setPersistence("saved");
  }, [remote.data, services]);

  useEffect(() => {
    if (remote.error && !dirtyRef.current) setPersistence("local");
  }, [remote.error]);

  useEffect(() => {
    setPositions((previous) => reconcile(previous, services));
  }, [services]);

  useEffect(() => {
    savePositions(projectId, envId, positions);
    if (!enabled || !dirtyRef.current) return;
    const revision = revisionRef.current;
	const dirtyServiceIDs = new Set(dirtyServiceIDsRef.current);
	const replaceAll = replaceAllRef.current;
    const timer = window.setTimeout(async () => {
      setPersistence("saving");
      try {
        let expected = versionRef.current;
        // A user may move a node before the initial GET has completed.
        if (!remote.data && expected === 0) {
          const current = await api.getTopologyLayout(projectId!, envId!);
          expected = current.resource_version;
        }
        let saved: TopologyLayout;
		let savedPositions = positions;
        try {
          saved = await api.putTopologyLayout(projectId!, envId!, toNodes(positions), expected);
        } catch (error) {
          if (!(error instanceof ApiError) || error.status !== 412) throw error;
          setPersistence("conflict");
          const current = await api.getTopologyLayout(projectId!, envId!);
		  savedPositions = rebasePositions(
			fromLayout(current),
			positions,
			dirtyServiceIDs,
			replaceAll,
			services,
		  );
		  saved = await api.putTopologyLayout(
			projectId!,
			envId!,
			toNodes(savedPositions),
			current.resource_version,
		  );
        }
        versionRef.current = saved.resource_version;
        if (revisionRef.current === revision) {
          dirtyRef.current = false;
		  dirtyServiceIDsRef.current.clear();
		  replaceAllRef.current = false;
		  setPositions(savedPositions);
          setPersistence("saved");
        }
      } catch {
        if (revisionRef.current === revision) setPersistence("local");
      }
    }, SAVE_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [enabled, envId, positions, projectId, remote.data, services]);

	const commit = useCallback((
		next: PositionMap | ((previous: PositionMap) => PositionMap),
		touchedServiceID?: string,
	) => {
    dirtyRef.current = true;
	if (touchedServiceID) dirtyServiceIDsRef.current.add(touchedServiceID);
	else replaceAllRef.current = true;
    revisionRef.current += 1;
    setPersistence(enabled ? "saving" : "local");
    setPositions(next);
  }, [enabled]);

  const move = useCallback((serviceId: string, next: NodePosition) => {
	commit((previous) => ({ ...previous, [serviceId]: next }), serviceId);
  }, [commit]);
  const replace = useCallback((next: PositionMap) => commit(reconcile(next, services)), [commit, services]);
  const reset = useCallback(() => commit(autoLayout(services)), [commit, services]);

  return { positions, move, replace, reset, persistence };
}

/** Merge saved positions with slots for current services and prune stale IDs. */
export function reconcile(previous: PositionMap, services: Service[]): PositionMap {
  const currentIDs = new Set(services.map((service) => service.id));
  let changed = Object.keys(previous).some((id) => !currentIDs.has(id));
  const next: PositionMap = {};
  services.forEach((service, index) => {
    next[service.id] = previous[service.id] ?? autoSlot(index);
    if (!previous[service.id]) changed = true;
  });
  return changed ? next : previous;
}

/**
 * Reapply only coordinates changed by this browser on top of a newer server
 * version. Explicit arrange/reset operations intentionally replace every
 * current node; a single drag must not overwrite another user's other moves.
 */
export function rebasePositions(
	remote: PositionMap,
	local: PositionMap,
	touchedServiceIDs: ReadonlySet<string>,
	replaceAll: boolean,
	services: Service[],
): PositionMap {
	if (replaceAll) return reconcile(local, services);
	const currentIDs = new Set(services.map((service) => service.id));
	const rebased = { ...reconcile(remote, services) };
	for (const serviceID of touchedServiceIDs) {
		if (currentIDs.has(serviceID) && local[serviceID]) {
			rebased[serviceID] = local[serviceID];
		}
	}
	return rebased;
}
