import { describe, expect, it } from "vitest";
import { actionLabel, bindProductAction, productActionCatalog } from "./productActions";

describe("product action registry", () => {
  it("keeps stable labels and explicit disabled explanations", () => {
    expect(actionLabel("resource.add")).toBe("Add resource");
    expect(bindProductAction("deployment.restart", {
      available: false,
      disabledReason: "Deploy a service before restarting it.",
      setupAction: "resource.add",
    })).toMatchObject({
      id: "deployment.restart",
      disabled: true,
      disabledReason: "Deploy a service before restarting it.",
      setupAction: "resource.add",
    });
  });

  it("exports only safe presentation metadata", () => {
	const networking = productActionCatalog().find((action) => action.id === "service.networking.open");
	expect(networking).toMatchObject({
	  scope: "service",
	  capability: "domains.write",
	});
  });
});
