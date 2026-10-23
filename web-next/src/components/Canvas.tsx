// Project canvas: dot-grid background + draggable service nodes + relationship
// edges. Node positions persist per project+environment through the control
// plane with a browser-local outage fallback; edges require explicit backend
// relationship evidence.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import type { Service } from "@/api/client";
import { autoLayout, useNodePositions } from "@/lib/canvasPositions";
import { deriveEdges, serviceKind, type NodeKind } from "@/lib/serviceKind";
import { actionLabel, type ProductActionID } from "@/lib/productActions";
import { NODE_DIMENSIONS, ServiceNode } from "./ServiceNode";

const STAGE_PAD_X = 32;
const STAGE_PAD_Y = 56;
const MIN_ZOOM = 0.5;
const MAX_ZOOM = 1.5;

type Props = {
  services: Service[];
  selectedServiceId?: string;
  environmentId?: string;
  onSelectService: (id: string) => void;
};

export function Canvas({
  services,
  selectedServiceId,
  environmentId,
  onSelectService,
}: Props) {
  const navigate = useNavigate();
  const stageRef = useRef<HTMLDivElement>(null);
  const [viewport, setViewport] = useState({ width: 0, height: 0 });
  const [zoom, setZoom] = useState(1);
  // All services in a project share the same project_id — derive it rather
  // than threading a new prop (ProjectCanvas is not ours to touch).
  const projectId = services[0]?.project_id;

  const { positions, move, replace, reset, persistence } = useNodePositions(
    projectId,
    environmentId,
    services,
  );

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage || typeof ResizeObserver === "undefined") return;
    const update = () =>
      setViewport({ width: stage.clientWidth, height: stage.clientHeight });
    update();
    const observer = new ResizeObserver(update);
    observer.observe(stage);
    return () => observer.disconnect();
  }, []);

  // Nodes report their resolved kind (from backend metadata) so we can derive
  // edges here without refetching each service's deployment at the canvas level.
  const [kinds, setKinds] = useState<Record<string, NodeKind>>({});
  const onKindResolved = useCallback((id: string, kind: NodeKind) => {
    setKinds((prev) => (prev[id] === kind ? prev : { ...prev, [id]: kind }));
  }, []);

  const kindOf = useCallback(
    (id: string): NodeKind => {
      const known = kinds[id];
      if (known) return known;
      const svc = services.find((s) => s.id === id);
      return svc ? serviceKind(svc) : "api";
    },
    [kinds, services],
  );

  const edges = useMemo(
    () => deriveEdges(services, kindOf),
    [services, kindOf],
  );

  const viewLogs = useCallback(
    (serviceId: string) => {
      if (!projectId) return;
      navigate({
        to: "/projects/$projectId",
        params: { projectId },
        search: { env: environmentId, service: serviceId, tab: "logs" },
      });
    },
    [navigate, projectId, environmentId],
  );

  const stageBounds = useMemo(() => {
    let maxX = 0;
    let maxY = 0;
    for (const id of Object.keys(positions)) {
      const p = positions[id];
      maxX = Math.max(maxX, p.x + NODE_DIMENSIONS.width);
      maxY = Math.max(maxY, p.y + NODE_DIMENSIONS.height);
    }
    return {
      width: Math.max(maxX + STAGE_PAD_X, viewport.width, 360),
      height: Math.max(maxY + STAGE_PAD_Y, viewport.height, 480),
    };
  }, [positions, viewport.height, viewport.width]);

  const centerOn = useCallback((serviceId?: string, scale = zoom) => {
    const stage = stageRef.current;
    if (!stage) return;
    const position = serviceId ? positions[serviceId] : undefined;
    const x = position
      ? position.x + NODE_DIMENSIONS.width / 2
      : stageBounds.width / 2;
    const y = position
      ? position.y + NODE_DIMENSIONS.height / 2
      : stageBounds.height / 2;
    stage.scrollTo({
      left: Math.max(0, x * scale - stage.clientWidth / 2),
      top: Math.max(0, y * scale - stage.clientHeight / 2),
      behavior: "smooth",
    });
  }, [positions, stageBounds.height, stageBounds.width, zoom]);

  const fit = useCallback(() => {
    const stage = stageRef.current;
    if (!stage || services.length === 0) return;
    const xs = services.map((service) => positions[service.id]?.x ?? STAGE_PAD_X);
    const ys = services.map((service) => positions[service.id]?.y ?? STAGE_PAD_Y);
    const contentWidth = Math.max(NODE_DIMENSIONS.width, Math.max(...xs) - Math.min(...xs) + NODE_DIMENSIONS.width + STAGE_PAD_X * 2);
    const contentHeight = Math.max(NODE_DIMENSIONS.height, Math.max(...ys) - Math.min(...ys) + NODE_DIMENSIONS.height + STAGE_PAD_Y * 2);
    const next = Math.min(1, Math.max(MIN_ZOOM, Math.min(stage.clientWidth / contentWidth, stage.clientHeight / contentHeight)));
    setZoom(next);
    window.requestAnimationFrame(() => centerOn(selectedServiceId, next));
  }, [centerOn, positions, selectedServiceId, services]);

  const arrange = useCallback(() => {
    const columns = Math.max(1, Math.floor((viewport.width - STAGE_PAD_X * 2) / (NODE_DIMENSIONS.width + 80)));
    replace(autoLayout(services, columns));
    window.requestAnimationFrame(fit);
  }, [fit, replace, services, viewport.width]);

  useEffect(() => {
    if (services.length !== 1 || viewport.width === 0) return;
    const only = services[0];
    const position = positions[only.id];
    if (!position || position.x !== STAGE_PAD_X || position.y !== STAGE_PAD_Y) return;
    move(only.id, {
      x: Math.max(STAGE_PAD_X, Math.round((viewport.width - NODE_DIMENSIONS.width) / 2)),
      y: Math.max(STAGE_PAD_Y, Math.round((viewport.height - NODE_DIMENSIONS.height) / 2) - 24),
    });
  }, [move, positions, services, viewport.height, viewport.width]);

  // When the inspector opens/closes, ResizeObserver reports the new viewport.
  // Recenter by scrolling only; durable coordinates are never rewritten.
  useEffect(() => {
    if (!selectedServiceId || viewport.width === 0) return;
    centerOn(selectedServiceId);
  }, [centerOn, selectedServiceId, viewport.width]);

  return (
    <div className="relative h-full min-h-[30rem] w-full">
      <div
        className="absolute right-3 top-3 z-30 flex max-w-[calc(100%-1.5rem)] items-center gap-1 overflow-x-auto rounded-lg border border-line-strong bg-surface/95 p-1 shadow-lg backdrop-blur"
        role="toolbar"
        aria-label="Topology layout controls"
      >
        <CanvasControl actionId="topology.zoom.out" onClick={() => setZoom((value) => Math.max(MIN_ZOOM, Number((value - 0.1).toFixed(1))))} disabled={zoom <= MIN_ZOOM}>−</CanvasControl>
        <span className="min-w-11 text-center font-mono text-[10px] text-muted" aria-live="polite">{Math.round(zoom * 100)}%</span>
        <CanvasControl actionId="topology.zoom.in" onClick={() => setZoom((value) => Math.min(MAX_ZOOM, Number((value + 0.1).toFixed(1))))} disabled={zoom >= MAX_ZOOM}>+</CanvasControl>
        <span className="mx-0.5 h-5 w-px shrink-0 bg-line" aria-hidden="true" />
        <CanvasControl actionId="topology.fit" onClick={fit}>Fit</CanvasControl>
        <CanvasControl actionId="topology.center" onClick={() => centerOn(selectedServiceId)} disabled={!selectedServiceId}>Center</CanvasControl>
        <CanvasControl actionId="topology.arrange" onClick={arrange}>Arrange</CanvasControl>
        <CanvasControl actionId="topology.reset" onClick={() => { reset(); setZoom(1); window.requestAnimationFrame(() => centerOn(undefined, 1)); }}>Reset</CanvasControl>
        <span className="hidden px-1 text-[10px] text-subtle sm:inline" title={`Layout persistence: ${persistence}`}>{persistence === "saved" ? "Saved" : persistence === "saving" ? "Saving…" : persistence === "conflict" ? "Rebasing…" : "Local"}</span>
      </div>
      <div
        ref={stageRef}
        className="canvas-stage h-full w-full overflow-auto rounded border border-line"
        role="region"
        aria-label="Project topology map"
        data-persistence={persistence}
      >
        <div style={{ width: stageBounds.width * zoom, height: stageBounds.height * zoom }}>
          <div
            className="relative origin-top-left"
            style={{ width: stageBounds.width, height: stageBounds.height, transform: `scale(${zoom})` }}
          >
        {services.length === 0 ? (
          <div className="absolute inset-0 grid place-content-center text-center">
            <strong className="text-text">No services yet</strong>
            <span className="mt-1 text-muted">
              Services in this project will appear on the canvas.
            </span>
          </div>
        ) : null}

        {/* Edge layer — painted UNDER the nodes, non-interactive. */}
          <EdgeLayer
          edges={edges}
          positions={positions}
          width={stageBounds.width}
          height={stageBounds.height}
        />

          <div
          className="absolute left-3 top-3 z-[5] flex gap-1.5"
          aria-hidden="true"
        >
          <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-2 py-0.5 text-[11px] text-muted">
            <span className="font-mono">{services.length}</span>
            <span>{services.length === 1 ? "service" : "services"}</span>
          </span>
        </div>

          {services.map((service) => {
          const position =
            positions[service.id] ?? { x: STAGE_PAD_X, y: STAGE_PAD_Y };
          return (
            <ServiceNode
              key={service.id}
              service={service}
              position={position}
              selected={service.id === selectedServiceId}
              environmentId={environmentId}
              onMove={(next) => move(service.id, next)}
              onSelect={() => onSelectService(service.id)}
              onKindResolved={onKindResolved}
              onOpen={onSelectService}
              onViewLogs={viewLogs}
              scale={zoom}
            />
          );
          })}
          </div>
        </div>
      </div>
    </div>
  );
}

function CanvasControl({ actionId, onClick, disabled, children }: { actionId: ProductActionID; onClick: () => void; disabled?: boolean; children: React.ReactNode }) {
  const label = actionLabel(actionId);
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      data-action-id={actionId}
      onClick={onClick}
      className="shrink-0 rounded px-2 py-1 text-[11px] font-semibold text-muted hover:bg-surface-2 hover:text-text disabled:cursor-not-allowed disabled:opacity-40"
    >
      {children}
    </button>
  );
}

// ── Edge layer ────────────────────────────────────────────────────

function EdgeLayer({
  edges,
  positions,
  width,
  height,
}: {
  edges: { from: string; to: string; relation: "ref" | "attach" }[];
  positions: Record<string, { x: number; y: number }>;
  width: number;
  height: number;
}) {
  const { width: nw, height: nh } = NODE_DIMENSIONS;
  const paths = edges
    .map((e) => {
      const a = positions[e.from];
      const b = positions[e.to];
      if (!a || !b) return null;
      const x1 = a.x + nw / 2;
      const y1 = a.y + nh / 2;
      const x2 = b.x + nw / 2;
      const y2 = b.y + nh / 2;
      // Horizontal-biased cubic for a calm, routed look.
      const dx = Math.max(40, Math.abs(x2 - x1) / 2);
      const d = `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`;
      return { key: `${e.from}|${e.to}`, d, relation: e.relation };
    })
    .filter((p): p is { key: string; d: string; relation: "ref" | "attach" } => p !== null);

  if (paths.length === 0) return null;

  return (
    <svg
      className="pointer-events-none absolute inset-0 z-0"
      width={width}
      height={height}
      aria-hidden="true"
    >
      {paths.map((p) => (
        <path
          key={p.key}
          className="canvas-edge"
          d={p.d}
          strokeDasharray={p.relation === "attach" ? "4 4" : undefined}
        />
      ))}
    </svg>
  );
}
