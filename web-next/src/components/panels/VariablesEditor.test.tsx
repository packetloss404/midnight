import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { VariableMeta } from "@/api/client";
import {
  __resetStagedChangesForTests,
  clearStagedForService,
  getStagedForService,
} from "@/lib/stagedChanges";
import { VariablesEditor } from "./VariablesEditor";

const hooks = vi.hoisted(() => ({
  variables: [] as VariableMeta[],
  refetch: vi.fn(),
}));

vi.mock("@/api/hooks", () => ({
  useVariables: () => ({
    data: hooks.variables,
    error: null,
    isPending: false,
    refetch: hooks.refetch,
  }),
  useServices: () => ({
    data: [],
    error: null,
    isPending: false,
  }),
}));

afterEach(() => {
  act(() => {
    __resetStagedChangesForTests();
  });
  vi.clearAllMocks();
});

describe("VariablesEditor write-only drafts", () => {
  it("removes committed plaintext from the DOM when its staged entry is cleared", () => {
    hooks.variables = [
      {
        id: "var-token",
        scope: "sealed",
        workspace_id: "",
        project_id: "project-1",
        environment_id: "env-1",
        service_id: "service-1",
        name: "API_TOKEN",
        version: 1,
        last_rotated_at: "2026-07-27T00:00:00Z",
        length_bucket: "17-64",
        kek_fingerprint: "test-fingerprint",
      },
    ];

    render(
      <VariablesEditor
        serviceId="service-1"
        projectId="project-1"
        environmentId="env-1"
      />,
    );

    const input = screen.getByRole("textbox", { name: "Value for API_TOKEN" });
    fireEvent.change(input, { target: { value: "replacement-secret" } });
    expect(input).toHaveValue("replacement-secret");
    expect(getStagedForService("service-1", "env-1")).toHaveLength(1);

    act(() => {
      clearStagedForService("service-1", "env-1");
    });

    expect(input).toHaveValue("");
    expect(document.body.textContent).not.toContain("replacement-secret");
  });
});
