// Draggable service node — the small card that appears on the canvas
// for each service. Drag is implemented with raw pointer events (we want
// pixel-level control without pulling in a DnD lib for the POC).

import clsx from "clsx";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Service } from "@/api/client";
import { useLatestActiveDeployment } from "@/api/hooks";
import { useDeploymentActions } from "@/api/deployActions";
import { useDeploymentMutationAuthority } from "@/components/panels/deployments/authority";
import { deploymentStatus } from "@/lib/deploymentStatus";
import {
  KIND_ACCENT,
  KIND_LABEL,
  serviceKind,
  type NodeKind,
} from "@/lib/serviceKind";
import { StatusPill } from "./StatusPill";
import { bindProductAction, type ProductActionID } from "@/lib/productActions";

export type { NodeKind };

export type ServiceNodePosition = {
  x: number;
  y: number;
};

type Props = {
  service: Service;
  position: ServiceNodePosition;
  selected: boolean;
  /** Active environment scope for the status query. */
  environmentId?: string;
  onMove: (next: ServiceNodePosition) => void;
  onSelect: () => void;
  /** Report the resolved kind up so the canvas can derive edges. */
  onKindResolved?: (serviceId: string, kind: NodeKind) => void;
  /** Open the service overlay on the logs tab (never forces a full drill-in). */
  onViewLogs?: (serviceId: string) => void;
  /** Open the service overlay (default tab). */
  onOpen?: (serviceId: string) => void;
  /** Logical-canvas zoom; pointer deltas are normalized to layout pixels. */
  scale?: number;
};

const NODE_W = 240;
const NODE_H = 132;

export function ServiceNode({
  service,
  position,
  selected,
  environmentId,
  onMove,
  onSelect,
  onKindResolved,
  onViewLogs,
  onOpen,
  scale = 1,
}: Props) {
  // Real status: the service's latest active (live-traffic) deployment,
  // mapped to a tone by the shared `deploymentStatus` mapper via StatusPill.
  const { data: deployment, isPending: statusPending } =
    useLatestActiveDeployment(service.id, environmentId);

  // Kind derives from BACKEND METADATA (image / declared source type) with a
  // slug fallback — not slug string-matching alone.
  const kind = useMemo(
    () => serviceKind(service, deployment),
    [service, deployment],
  );
  useEffect(() => {
    onKindResolved?.(service.id, kind);
  }, [onKindResolved, service.id, kind]);

  const statusSignal = deployment
    ? {
        status: deployment.status,
        operation_state: deployment.operation_state,
        active: deployment.active,
      }
    : { status: statusPending ? "deploying" : "unknown" };
  const view = deploymentStatus(statusSignal);
  const accent = KIND_ACCENT[kind];

  const { redeploy, restart } = useDeploymentActions(service.id);
  const deploymentId = deployment?.id;
  const authority = useDeploymentMutationAuthority(service.project_id, environmentId);
  const acting = redeploy.isPending || restart.isPending;

  const dragState = useRef<{
    pointerId: number;
    startX: number;
    startY: number;
    originX: number;
    originY: number;
    moved: boolean;
  } | null>(null);

  const handlePointerDown = useCallback(
    (e: React.PointerEvent<HTMLElement>) => {
      // Only drag on primary button.
      if (e.button !== 0) return;
      const target = e.currentTarget;
      target.setPointerCapture(e.pointerId);
      target.dataset.dragging = "true";
      dragState.current = {
        pointerId: e.pointerId,
        startX: e.clientX,
        startY: e.clientY,
        originX: position.x,
        originY: position.y,
        moved: false,
      };
    },
    [position.x, position.y],
  );

  const handlePointerMove = useCallback(
    (e: React.PointerEvent<HTMLElement>) => {
      const state = dragState.current;
      if (!state || state.pointerId !== e.pointerId) return;
      const dx = (e.clientX - state.startX) / scale;
      const dy = (e.clientY - state.startY) / scale;
      if (!state.moved && (Math.abs(dx) > 3 || Math.abs(dy) > 3)) {
        state.moved = true;
      }
      if (state.moved) {
        onMove({
          x: Math.max(0, state.originX + dx),
          y: Math.max(0, state.originY + dy),
        });
      }
    },
    [onMove, scale],
  );

  const finishDrag = useCallback(
    (e: React.PointerEvent<HTMLElement>) => {
      const state = dragState.current;
      const target = e.currentTarget;
      if (state && state.pointerId === e.pointerId) {
        if (!state.moved) {
          onSelect();
        }
        dragState.current = null;
      }
      target.dataset.dragging = "false";
      if (target.hasPointerCapture(e.pointerId)) {
        target.releasePointerCapture(e.pointerId);
      }
    },
    [onSelect],
  );

  return (
    <article
      className="canvas-node group"
      data-selected={selected ? "true" : "false"}
      data-kind={kind}
      data-tone={view.tone}
      style={{
        left: position.x,
        top: position.y,
        width: NODE_W,
        // Status-driven affordance: crashed → red ring, in-flight → soft pulse.
        ...(view.tone === "danger"
          ? { borderColor: "var(--danger)", boxShadow: "0 0 0 1px var(--danger-soft)" }
          : null),
      }}
      // A "group" (not "button"): the node contains its own interactive
      // controls (the actions menu button + status), so a nested button inside a
      // role="button" would be invalid ARIA. It stays keyboard-focusable and
      // Enter/Space opens it (handled in onKeyDown below).
      role="group"
      tabIndex={0}
      aria-current={selected ? "true" : undefined}
      aria-label={`${service.display_name || service.slug} service`}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={finishDrag}
      onPointerCancel={finishDrag}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect();
        }
      }}
    >
      <header className="flex items-center justify-between gap-2">
        <span
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-[11px] font-semibold tracking-wide"
          style={{
            color: accent,
            background: `color-mix(in oklab, ${accent} 18%, transparent)`,
          }}
        >
          <span
            className={clsx(
              "inline-block h-1.5 w-1.5 rounded-full",
              view.pending && "animate-[agent-pulse_1.1s_ease-in-out_infinite]",
            )}
            style={{ background: accent }}
          />
          {KIND_LABEL[kind]}
        </span>
        <div className="flex items-center gap-1.5">
          <StatusPill signal={statusSignal} />
          <NodeActionsMenu
            serviceName={service.display_name || service.slug}
            canReplay={Boolean(deploymentId) && authority.canReplay}
            canRestart={Boolean(deploymentId) && authority.canRuntime}
            hasDeployment={Boolean(deploymentId)}
            acting={acting}
            onRedeploy={() => deploymentId && redeploy.mutate(deploymentId)}
            onRestart={() => deploymentId && restart.mutate(deploymentId)}
            onViewLogs={() => onViewLogs?.(service.id)}
            onOpen={() => (onOpen ? onOpen(service.id) : onSelect())}
          />
        </div>
      </header>
      <h3
        className="m-0 mt-2.5 mb-1.5 truncate text-[0.94rem] font-semibold tracking-tight"
        title={service.display_name || service.slug}
      >
        {service.display_name || service.slug}
      </h3>
      <dl className="m-0 grid grid-cols-3 gap-1.5">
        <NodeMeta label="Slug" value={service.slug} mono />
        <NodeMeta label="Project" value={service.project_id} mono />
        <NodeMeta label="Updated" value={shortDate(service.updated_at)} />
      </dl>
    </article>
  );
}

// ── Inline tile actions (hover 3-dot menu) ────────────────────────
// Common actions (redeploy / restart / logs / open) are reachable straight
// from the tile — we never force a drill-in for these.

type MenuProps = {
  serviceName: string;
  canReplay: boolean;
  canRestart: boolean;
  hasDeployment: boolean;
  acting: boolean;
  onRedeploy: () => void;
  onRestart: () => void;
  onViewLogs: () => void;
  onOpen: () => void;
};

function NodeActionsMenu({
  serviceName,
  canReplay,
  canRestart,
  hasDeployment,
  acting,
  onRedeploy,
  onRestart,
  onViewLogs,
  onOpen,
}: MenuProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const itemsRef = useRef<(HTMLButtonElement | null)[]>([]);

  // Close on outside pointer + Escape.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onDoc, true);
    return () => document.removeEventListener("pointerdown", onDoc, true);
  }, [open]);

  // Focus the first item when opening.
  useEffect(() => {
    if (open) itemsRef.current[0]?.focus();
  }, [open]);

  const stop = (e: React.SyntheticEvent) => e.stopPropagation();

  const run = (fn: () => void) => (e: React.MouseEvent) => {
    e.stopPropagation();
    setOpen(false);
    fn();
  };

  const replay = bindProductAction("deployment.redeploy", {
    available: canReplay,
    disabledReason: hasDeployment
      ? "The current project role cannot trigger deployments."
      : "Deploy this service once before replaying its image.",
  });
  const restart = bindProductAction("deployment.restart", {
    available: canRestart,
    disabledReason: hasDeployment
      ? "The current project role cannot run service actions."
      : "Deploy this service once before restarting it.",
  });
  const logs = bindProductAction("service.logs.open");
  const openService = bindProductAction("service.open");
  const items: { id: ProductActionID; label: string; hint?: string; run: () => void; disabled?: boolean; disabledReason?: string }[] = [
    { id: replay.id, label: replay.label, run: onRedeploy, disabled: replay.disabled, disabledReason: replay.disabledReason },
    { id: restart.id, label: restart.label, run: onRestart, disabled: restart.disabled, disabledReason: restart.disabledReason },
    { id: logs.id, label: logs.label, run: onViewLogs },
    { id: openService.id, label: openService.label, run: onOpen },
  ];

  const focusItem = (dir: 1 | -1) => {
    const enabled = items
      .map((it, i) => ({ it, i }))
      .filter(({ it }) => !it.disabled);
    const active = document.activeElement;
    const currentIdx = enabled.findIndex(
      ({ i }) => itemsRef.current[i] === active,
    );
    const nextIdx =
      currentIdx === -1
        ? 0
        : (currentIdx + dir + enabled.length) % enabled.length;
    itemsRef.current[enabled[nextIdx].i]?.focus();
  };

  return (
    <div ref={rootRef} className="relative" onPointerDown={stop}>
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Actions for ${serviceName}`}
        className={clsx(
          "grid h-6 w-6 place-items-center rounded-md border border-line text-muted transition",
          "opacity-0 focus-visible:opacity-100 group-hover:opacity-100",
          "hover:border-line-strong hover:text-text",
          (open || acting) && "opacity-100",
        )}
        onPointerDown={stop}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((v) => !v);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            e.stopPropagation();
            setOpen(true);
          }
        }}
      >
        {acting ? (
          <span
            aria-hidden="true"
            className="h-3 w-3 animate-spin rounded-full border-2 border-line-strong border-t-accent"
          />
        ) : (
          <span aria-hidden="true" className="text-[15px] leading-none">
            ⋯
          </span>
        )}
      </button>

      {open ? (
        <div
          role="menu"
          aria-label={`${serviceName} actions`}
          className="absolute right-0 top-7 z-20 min-w-[220px] overflow-hidden rounded-lg border border-line-strong bg-surface p-1 shadow-lg"
          onPointerDown={stop}
          onKeyDown={(e) => {
            // Keep every key inside the menu — never let Enter/Space/arrows
            // bubble to the node's own select handler.
            e.stopPropagation();
            if (e.key === "Escape") {
              setOpen(false);
            } else if (e.key === "ArrowDown") {
              e.preventDefault();
              focusItem(1);
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              focusItem(-1);
            }
          }}
        >
          {items.map((it, i) => (
            <button
              key={it.label}
              ref={(el) => {
                itemsRef.current[i] = el;
              }}
              type="button"
              role="menuitem"
              disabled={it.disabled}
              title={it.disabledReason}
              data-action-id={it.id}
              className={clsx(
                "flex w-full items-center justify-between gap-3 rounded-md px-2.5 py-1.5 text-left text-[13px] transition",
                it.disabled
                  ? "cursor-not-allowed text-subtle"
                  : "text-text hover:bg-surface-2 focus-visible:bg-surface-2",
              )}
              onClick={run(it.run)}
            >
              <span className="grid min-w-0 gap-0.5">
                <span>{it.label}</span>
                {it.disabledReason ? (
                  <span className="max-w-[18rem] text-[10px] leading-tight text-subtle">
                    {it.disabledReason}
                  </span>
                ) : null}
              </span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function NodeMeta({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <dt className="font-mono text-[10px] uppercase tracking-wider text-subtle">
        {label}
      </dt>
      <dd
        className={clsx(
          "m-0 mt-0.5 truncate text-[12px] font-medium",
          mono ? "font-mono text-muted" : "text-text",
        )}
        title={value}
      >
        {value || "—"}
      </dd>
    </div>
  );
}

function shortDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const now = Date.now();
  const diff = Math.round((now - d.getTime()) / 1000);
  if (diff < 60) return `${diff}s`;
  if (diff < 3600) return `${Math.round(diff / 60)}m`;
  if (diff < 86400) return `${Math.round(diff / 3600)}h`;
  return `${Math.round(diff / 86400)}d`;
}

export const NODE_DIMENSIONS = { width: NODE_W, height: NODE_H };
