import { describe, expect, it } from "vitest";
import type { Service } from "@/api/client";
import { deriveEdges } from "./serviceKind";

const service = (id: string, spec?: Record<string, unknown>): Service => ({
  id,
  project_id: "prj_1",
  slug: id,
  display_name: id,
  created_at: "2026-08-13T00:00:00Z",
  updated_at: "2026-08-13T00:00:00Z",
  ...spec,
});

describe("verified topology edges", () => {
  it("draws only explicit references and attachments", () => {
    const services = [
      service("svc_web", { references: ["svc_db"] }),
      service("svc_worker", { spec: { attached_service_ids: ["svc_db"] } }),
      service("svc_db"),
    ];
    expect(deriveEdges(services, () => "api")).toEqual([
      { from: "svc_web", to: "svc_db", relation: "ref" },
      { from: "svc_worker", to: "svc_db", relation: "attach" },
    ]);
  });

  it("does not infer an edge from resource kinds", () => {
    expect(deriveEdges([service("svc_web"), service("svc_db")], (id) => id === "svc_db" ? "db" : "web")).toEqual([]);
  });
});
