import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AddResourcePalette } from "./AddResourcePalette";

const state = vi.hoisted(() => ({
  permissions: ["project.read", "service.write"],
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    state: {
      status: "human",
      me: { principal: { user_id: "usr_admin", instance_admin: true } },
    },
    operatorFeaturesAvailable: true,
  }),
}));

vi.mock("@/components/panels/automation-tokens/api", () => ({
  useProjectAccess: (projectId: string, environmentId?: string) => ({
    data: {
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: projectId,
      environment_id: environmentId,
      project_role: "editor",
      environment_restricted: false,
      permissions: state.permissions,
    },
    isPending: false,
  }),
}));

vi.mock("@/components/panels/AddServiceForm", () => ({
  AddServiceForm: () => <div data-testid="resource-flow">Executable resource form</div>,
}));

describe("AddResourcePalette authority changes", () => {
  afterEach(() => {
    state.permissions = ["project.read", "service.write"];
    window.localStorage.clear();
  });

  it("unmounts an active mutation flow as soon as its exact capability is revoked", async () => {
    const onClose = vi.fn();
    const view = render(
      <AddResourcePalette
        projectId="prj_1"
        installationAdminAuthorized
        onClose={onClose}
      />,
    );

    fireEvent.click(screen.getByRole("option", { name: /Empty service/i }));
    expect(screen.getByTestId("resource-flow")).toBeInTheDocument();

    state.permissions = ["project.read"];
    view.rerender(
      <AddResourcePalette
        projectId="prj_1"
        installationAdminAuthorized
        onClose={onClose}
      />,
    );

    expect(screen.queryByTestId("resource-flow")).not.toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("option", { name: /Empty service/i })).toBeDisabled(),
    );
  });

  it("uses dialog keyboard dismissal instead of leaving an unfocused overlay", () => {
    const onClose = vi.fn();
    render(
      <AddResourcePalette
        projectId="prj_1"
        installationAdminAuthorized
        onClose={onClose}
      />,
    );

    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("filters the categorized catalog and opens the active result with Enter", () => {
    render(
      <AddResourcePalette
        projectId="prj_1"
        installationAdminAuthorized
        onClose={vi.fn()}
      />,
    );

    const search = screen.getByRole("combobox", { name: "Search resources" });
    fireEvent.change(search, { target: { value: "blank manual" } });
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option", { name: /Empty service/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    fireEvent.keyDown(search, { key: "Enter" });
    expect(screen.getByTestId("resource-flow")).toBeInTheDocument();
    expect(window.localStorage.getItem("midnight.add-resource.recent")).toBe(
      '["empty"]',
    );
  });
});
