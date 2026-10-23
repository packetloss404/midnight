import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/api/client";

const state = vi.hoisted(() => ({
  move: vi.fn(),
  replace: vi.fn(),
  reset: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("@/lib/canvasPositions", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/canvasPositions")>();
  return {
    ...actual,
    useNodePositions: () => ({
      positions: { svc_a: { x: 32, y: 56 }, svc_b: { x: 352, y: 56 } },
      move: state.move,
      replace: state.replace,
      reset: state.reset,
      persistence: "saved",
    }),
  };
});
vi.mock("./ServiceNode", () => ({
  NODE_DIMENSIONS: { width: 240, height: 132 },
  ServiceNode: ({ service }: { service: Service }) => <div>{service.display_name}</div>,
}));

import { Canvas } from "./Canvas";

const services: Service[] = ["svc_a", "svc_b"].map((id) => ({
  id,
  project_id: "prj_1",
  slug: id,
  display_name: id,
  created_at: "2026-08-13T00:00:00Z",
  updated_at: "2026-08-13T00:00:00Z",
}));

describe("Canvas topology controls", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  });

  it("exposes the complete accessible map toolbar and persisted arrange/reset actions", () => {
    render(<Canvas services={services} environmentId="env_1" onSelectService={vi.fn()} />);
    expect(screen.getByRole("region", { name: "Project topology map" })).toHaveAttribute("data-persistence", "saved");
    expect(screen.getByRole("toolbar", { name: "Topology layout controls" })).toBeInTheDocument();
    for (const name of ["Zoom out", "Zoom in", "Fit all services", "Center selected service", "Auto-layout services", "Reset saved layout"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.getByRole("button", { name: "Center selected service" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Auto-layout services" }));
    expect(state.replace).toHaveBeenCalledWith({ svc_a: { x: 32, y: 56 }, svc_b: { x: 32, y: 228 } });
    fireEvent.click(screen.getByRole("button", { name: "Reset saved layout" }));
    expect(state.reset).toHaveBeenCalledTimes(1);
  });
});
