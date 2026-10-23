import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DeploymentResource } from "@/api/client";

const state = vi.hoisted(() => ({
  authority: {
    state: "unavailable",
    canReplay: false,
    canRuntime: false,
    canCancel: false,
    detail:
      "Human-session deployment mutation routes are not installed yet.",
  },
  actionMutate: vi.fn(),
  cancelMutate: vi.fn(),
}));

vi.mock("@/api/hooks", () => ({
  useOperation: () => ({
    data: {
      id: "op_1",
      state: "SUCCEEDED",
      created_at: "2026-07-28T12:00:00Z",
      updated_at: "2026-07-28T12:01:00Z",
    },
  }),
}));

vi.mock("./deployments/authority", () => ({
  useDeploymentMutationAuthority: () => state.authority,
}));

vi.mock("./deployments/api", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("./deployments/api")>();
  return {
    ...actual,
    useDeploymentActions: () => ({
      action: {
        mutate: state.actionMutate,
        isPending: false,
        isSuccess: false,
        data: undefined,
        error: null,
      },
      cancel: {
        mutate: state.cancelMutate,
        isPending: false,
        error: null,
      },
    }),
  };
});

vi.mock("@/components/panels/LogViewer", () => ({
  LogViewer: () => <div data-testid="log-viewer" />,
}));

import { DeploymentDetail } from "./DeploymentDetail";

const deployment = {
  id: "dpl_1",
  deployment_id: "dpl_1",
  service_id: "svc_1",
  project_id: "prj_1",
  environment_id: "env_1",
  operation_id: "op_1",
  operation_state: "SUCCEEDED",
  status: "ACTIVE",
  active: true,
  runtime_desired_state: "running",
  image: `registry.example/app@sha256:${"a".repeat(64)}`,
  action: "deploy_source",
  source_type: "github",
  source_repository: "https://github.com/acme/widget.git",
  source_ref: "refs/heads/main",
  commit_sha: "b".repeat(40),
  trigger_actor_type: "provider_automation",
  trigger_actor_label: "GitHub App",
  created_at: "2026-07-28T12:00:00Z",
  started_at: "2026-07-28T12:00:10Z",
  completed_at: "2026-07-28T12:01:00Z",
} satisfies DeploymentResource;

beforeEach(() => {
  vi.clearAllMocks();
  state.authority = {
    state: "unavailable",
    canReplay: false,
    canRuntime: false,
    canCancel: false,
    detail:
      "Human-session deployment mutation routes are not installed yet.",
  };
});

describe("DeploymentDetail mutation authority", () => {
  it("shows pending route intent without presenting it as a public URL", () => {
    render(
      <DeploymentDetail
        deployment={{
          ...deployment,
          route_intent: "api-prj.midnight.test",
          route_apply_state: "pending",
          route_reachable: false,
        }}
      />,
    );

    expect(screen.getByText("Requested hostname")).toBeInTheDocument();
    expect(screen.getByText("api-prj.midnight.test")).toBeInTheDocument();
    expect(screen.getByText("pending")).toBeInTheDocument();
    expect(screen.getByText("Trusted ingress not ready")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "api-prj.midnight.test" }),
    ).not.toBeInTheDocument();
  });

  it("keeps a trusted-ingress-ready route URL non-clickable", () => {
    render(
      <DeploymentDetail
        deployment={{
          ...deployment,
          route_intent: "api-prj.midnight.test",
          route: "https://api-prj.midnight.test",
          route_apply_state: "ready",
          route_reachable: true,
        }}
      />,
    );

    expect(
      screen.getByText("https://api-prj.midnight.test"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("link", {
        name: "https://api-prj.midnight.test",
      }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("Trusted ingress ready")).toBeInTheDocument();
  });

  it("does not expose state-valid actions without server-backed authority", () => {
    render(<DeploymentDetail deployment={deployment} />);

    expect(
      screen.getByRole("button", { name: "Deployment actions" }),
    ).toBeDisabled();
    expect(
      screen.getByText(
        "Human-session deployment mutation routes are not installed yet.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("Logs for this deployment")).toBeInTheDocument();
    expect(
      screen.getByText(/Every line keeps the build, runtime/),
    ).toBeInTheDocument();
    expect(screen.getByText("https://github.com/acme/widget.git")).toBeInTheDocument();
    expect(screen.getByText("GitHub App")).toBeInTheDocument();
    expect(screen.getByText("b".repeat(40))).toBeInTheDocument();
    expect(screen.getByText("Runtime console")).toBeInTheDocument();
    expect(screen.getByText(/no authenticated, project-scoped runtime exec API/i)).toBeInTheDocument();
  });

  it("enables only actions admitted by both state and authority", async () => {
    state.authority = {
      state: "available",
      canReplay: true,
      canRuntime: false,
      canCancel: true,
      detail: "Server permissions and transport are available.",
    };
    const user = userEvent.setup();
    render(<DeploymentDetail deployment={deployment} />);

    await user.click(
      screen.getByRole("button", { name: "Deployment actions" }),
    );
    expect(screen.getByRole("menuitem", { name: "Redeploy" })).toBeEnabled();
    expect(
      screen.getByRole("menuitem", { name: /Restart/ }),
    ).toBeDisabled();

    await user.click(screen.getByRole("menuitem", { name: "Redeploy" }));
    expect(state.actionMutate).toHaveBeenCalledWith("redeploy");
    expect(state.cancelMutate).not.toHaveBeenCalled();
  });

  it("runs tenant cancellation only for an in-flight deployment", async () => {
    state.authority = {
      state: "available",
      canReplay: true,
      canRuntime: false,
      canCancel: true,
      detail: "Server permissions and transport are available.",
    };
    const user = userEvent.setup();
    render(
      <DeploymentDetail
        deployment={{
          ...deployment,
          active: false,
          status: "BUILDING",
          operation_state: "RUNNING",
          runtime_desired_state: undefined,
        }}
      />,
    );

    expect(
      screen.getByText(/withdraws unleased work immediately/),
    ).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Deployment actions" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "Cancel" }));

    expect(state.cancelMutate).toHaveBeenCalledOnce();
    expect(state.actionMutate).not.toHaveBeenCalled();
  });
});
