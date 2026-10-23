import { describe, expect, it } from "vitest";
import type { Service } from "@/api/client";
import { autoLayout, rebasePositions, reconcile } from "./canvasPositions";

const service = (id: string): Service => ({
  id,
  project_id: "prj_1",
  slug: id,
  display_name: id,
  created_at: "2026-08-13T00:00:00Z",
  updated_at: "2026-08-13T00:00:00Z",
});

describe("topology positions", () => {
  it("lays out services deterministically for the active viewport columns", () => {
    expect(autoLayout([service("svc_a"), service("svc_b"), service("svc_c")], 2)).toEqual({
      svc_a: { x: 32, y: 56 },
      svc_b: { x: 352, y: 56 },
      svc_c: { x: 32, y: 228 },
    });
  });

  it("keeps current coordinates, adds missing nodes, and prunes deleted services", () => {
    expect(reconcile({ svc_a: { x: 90, y: 91 }, stale: { x: 1, y: 2 } }, [service("svc_a"), service("svc_b")])).toEqual({
      svc_a: { x: 90, y: 91 },
      svc_b: { x: 352, y: 56 },
    });
  });

  it("rebases only locally touched nodes after a CAS conflict", () => {
	const current = {
	  svc_a: { x: 500, y: 501 },
	  svc_b: { x: 600, y: 601 },
	  stale: { x: 1, y: 2 },
	};
	const local = {
	  svc_a: { x: 90, y: 91 },
	  svc_b: { x: 352, y: 56 },
	};
	expect(
	  rebasePositions(
		current,
		local,
		new Set(["svc_a"]),
		false,
		[service("svc_a"), service("svc_b")],
	  ),
	).toEqual({
	  svc_a: { x: 90, y: 91 },
	  svc_b: { x: 600, y: 601 },
	});
  });

  it("keeps explicit arrange/reset as a full replacement during rebase", () => {
	const local = autoLayout([service("svc_a"), service("svc_b")], 1);
	expect(
	  rebasePositions(
		{ svc_a: { x: 500, y: 501 }, svc_b: { x: 600, y: 601 } },
		local,
		new Set(),
		true,
		[service("svc_a"), service("svc_b")],
	  ),
	).toEqual(local);
  });
});
