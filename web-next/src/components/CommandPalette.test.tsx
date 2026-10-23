import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CommandPalette } from "./CommandPalette";

vi.mock("@/api/hooks", () => ({
  useProjects: () => ({ data: [] }),
  useServices: () => ({ data: [] }),
  useEnvironmentResources: () => ({ data: { resources: [] } }),
}));

vi.mock("@/api/deployActions", () => ({
  useDeploymentActions: () => ({
    redeploy: { mutate: vi.fn() },
    restart: { mutate: vi.fn() },
  }),
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    state: { status: "operator" },
    operatorFeaturesAvailable: true,
  }),
}));

vi.mock("@/components/panels/automation-tokens/api", () => ({
  useProjectAccess: () => ({ data: undefined, isPending: false }),
}));

vi.mock("@/components/panels/deployments/authority", () => ({
  useDeploymentMutationAuthority: () => ({
    canReplay: true,
    canRuntime: true,
  }),
}));

describe("CommandPalette navigation shortcuts", () => {
  it("routes service G sequences and Add Resource to the canonical workspace URLs", () => {
    const navigate = vi.fn();
    render(
      <CommandPalette
        navigate={navigate as never}
        context={{
          projectId: "prj_1",
          environmentId: "env_1",
          serviceId: "svc_1",
        }}
      />,
    );

    fireEvent.keyDown(window, { key: "g" });
    fireEvent.keyDown(window, { key: "c" });
    expect(navigate).toHaveBeenLastCalledWith({
      to: "/projects/$projectId/services/$serviceId/$section",
      params: { projectId: "prj_1", serviceId: "svc_1", section: "configure" },
      search: { env: "env_1", configure: undefined },
    });

    fireEvent.keyDown(window, { key: "A", metaKey: true, shiftKey: true });
    expect(navigate).toHaveBeenLastCalledWith({
      to: "/projects/$projectId",
      params: { projectId: "prj_1" },
      search: { env: "env_1", tab: "add-service" },
    });
  });

  it("does not intercept service letter sequences in text-entry controls", () => {
    const navigate = vi.fn();
    render(
      <>
        <input aria-label="Draft value" />
        <CommandPalette
          navigate={navigate as never}
          context={{
            projectId: "prj_1",
            environmentId: "env_1",
            serviceId: "svc_1",
          }}
        />
      </>,
    );

    const input = screen.getByRole("textbox", { name: "Draft value" });
    fireEvent.keyDown(input, { key: "g" });
    fireEvent.keyDown(input, { key: "c" });
    expect(navigate).not.toHaveBeenCalled();
  });

  it("uses the shared resource catalog vocabulary in palette search", () => {
    render(
      <CommandPalette
        navigate={vi.fn() as never}
        open
        context={{ projectId: "prj_1", environmentId: "env_1" }}
      />,
    );

    fireEvent.change(screen.getByRole("combobox", { name: "Command palette search" }), {
      target: { value: "registry digest" },
    });
    expect(screen.getByRole("option", { name: /Add Container image/i })).toBeInTheDocument();
  });
});
