import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { EnvironmentServiceResource } from "@/api/client";
import { ResourceGrid } from "./ResourceGrid";

const resources: EnvironmentServiceResource[] = [
  {
    service: {
      id: "svc_worker",
      project_id: "prj_midnight",
      slug: "worker",
      display_name: "Worker",
    },
    latest_deployment: {
      id: "dep_worker",
      status: "FAILED",
      active: false,
      source_type: "github",
      source_repository: "acme/worker",
      source_ref: "main",
      commit_sha: "abc123",
      replicas: {
        desired: 2,
        ready: 0,
        running: 0,
        health: "unhealthy",
        evidence_at: "2026-07-27T12:30:00Z",
        truth_source: "deployment-instances",
      },
      operation: {
        id: "op_worker",
        state: "FAILED",
        failure_code: "IMAGE_PULL_FAILED",
        failure_reason: "Registry rejected the image.",
      },
      updated_at: "2026-07-27T12:30:00Z",
      truth_source: "canonical",
    },
    source: {
      type: "github",
      repository: "acme/worker",
      ref: "main",
      revision: "abc123",
      auto_deploy: true,
      truth_source: "canonical-github-link",
    },
    private_endpoint: {
      host: "worker.midnight.internal",
      truth_source: "derived-service-dns",
    },
  },
  {
    service: {
      id: "svc_api",
      project_id: "prj_midnight",
      slug: "api",
      display_name: "API",
    },
    endpoint: {
      deployment_id: "dep_api_active",
      host: "api.midnight.test",
      url: "https://api.midnight.test",
      tls: true,
      evidence_at: "2026-07-27T12:00:00Z",
    },
  },
  {
    service: {
      id: "svc_queue",
      project_id: "prj_midnight",
      slug: "queue",
      display_name: "Queue",
    },
    latest_deployment: {
      id: "dep_queue",
      status: "QUEUED",
      active: false,
      operation: {
        id: "op_queue",
        state: "QUEUED",
      },
      truth_source: "legacy-operation",
    },
  },
];

describe("ResourceGrid", () => {
  it("renders deterministic, honest states using only the summary contract", () => {
    const { container } = render(
      <ResourceGrid
        resources={resources}
        onSelectService={vi.fn()}
        onViewLogs={vi.fn()}
      />,
    );

    const cards = Array.from(container.querySelectorAll(".resource-card"));
    expect(cards).toHaveLength(3);
    expect(cards.map((card) => card.textContent)).toEqual([
      expect.stringContaining("API"),
      expect.stringContaining("Queue"),
      expect.stringContaining("Worker"),
    ]);
    expect(screen.getByText("not deployed")).toBeInTheDocument();
    expect(screen.getAllByText("queued")).toHaveLength(2);
    expect(screen.getAllByText("failed")).toHaveLength(2);
    expect(
      screen.getByText("Registry rejected the image."),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Nothing is serving traffic. dep_worker is failed.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "api.midnight.test" }),
    ).toHaveAttribute("href", "https://api.midnight.test");

    expect(screen.getAllByText(/^source$/i)).toHaveLength(3);
    expect(screen.getAllByText(/^current revision$/i)).toHaveLength(3);
    expect(screen.getByText("acme/worker · main")).toBeInTheDocument();
    expect(screen.getByText("abc123")).toBeInTheDocument();
    expect(screen.getByText("unhealthy · 0/2 ready")).toBeInTheDocument();
    expect(
      screen.queryByText("worker.midnight.internal"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Record source")).not.toBeInTheDocument();
  });

  it("supports arrow-key selection and the logs shortcut", async () => {
    const user = userEvent.setup();
    const select = vi.fn();
    const logs = vi.fn();
    render(
      <ResourceGrid
        resources={resources}
        onSelectService={select}
        onViewLogs={logs}
      />,
    );

    const api = screen.getByRole("button", { name: "Open API" });
    const queue = screen.getByRole("button", { name: "Open Queue" });
    api.focus();

    await user.keyboard("{ArrowDown}");
    expect(queue).toHaveFocus();
    await user.keyboard("l");
    expect(logs).toHaveBeenCalledWith("svc_queue");
    await user.keyboard("{Enter}");
    expect(select).toHaveBeenCalledWith("svc_queue", "keyboard");
  });

  it("routes the connect-source action to Source configuration", async () => {
    const user = userEvent.setup();
    const configure = vi.fn();
    render(
      <ResourceGrid
        resources={[resources[1]]}
        onSelectService={vi.fn()}
        onConfigureService={configure}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "Connect source" }),
    );
    expect(configure).toHaveBeenCalledWith("svc_api", "source");
  });

  it("does not turn absent operator evidence into a not-deployed claim", async () => {
    const user = userEvent.setup();
    const select = vi.fn();
    render(
      <ResourceGrid
        resources={[{ service: resources[0].service }]}
        deploymentEvidenceAvailable={false}
        scopeLabel="in this project"
        onSelectService={select}
      />,
    );

    expect(
      screen.getByText("1 service in this project"),
    ).toBeInTheDocument();
    expect(screen.getByText("deployment unavailable")).toBeInTheDocument();
    expect(
      screen.getByText("Not available with this session"),
    ).toBeInTheDocument();
    expect(screen.queryByText("not deployed")).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "Deployment state is unavailable for this session.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Logs" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText("Arrow keys move · Enter opens"),
    ).toBeInTheDocument();

    const worker = screen.getByRole("button", { name: "Open Worker" });
    worker.focus();
    await user.keyboard("l");
    expect(select).not.toHaveBeenCalled();
  });

  it("explains a crashed rollout in plain language and keeps the worker's exact reason", () => {
    const crashed: EnvironmentServiceResource = {
      ...resources[0],
      latest_deployment: {
        ...resources[0].latest_deployment!,
        operation: {
          id: "op_worker",
          state: "FAILED",
          failure_code: "DEPLOY_CANDIDATE_EXITED",
          failure_reason:
            "the container stopped before it was ready; container exited with code 1; last log lines: FATAL: no config file",
        },
      },
    };
    render(
      <ResourceGrid resources={[crashed]} onSelectService={vi.fn()} />,
    );

    expect(
      screen.getByText("The container stopped before it was ready."),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/FATAL: no config file/),
    ).toBeInTheDocument();
  });

  it("names a readiness timeout without leaking the raw failure code", () => {
    const stuck: EnvironmentServiceResource = {
      ...resources[0],
      latest_deployment: {
        ...resources[0].latest_deployment!,
        operation: {
          id: "op_worker",
          state: "FAILED",
          failure_code: "RECONCILE_STEP_FAILED",
          failure_reason: "runtime status is STARTING",
        },
      },
    };
    render(<ResourceGrid resources={[stuck]} onSelectService={vi.fn()} />);

    expect(
      screen.getByText("The service never became ready in time."),
    ).toBeInTheDocument();
    expect(screen.getByText("runtime status is STARTING")).toBeInTheDocument();
    expect(screen.queryByText("RECONCILE_STEP_FAILED")).toBeNull();
  });

  it("shows the failed rollout's reason while an older release still serves", () => {
    const serving: EnvironmentServiceResource = {
      ...resources[0],
      active_deployment: {
        id: "dep_worker_active",
        status: "SUCCEEDED",
        active: true,
        truth_source: "canonical",
      },
    };
    render(<ResourceGrid resources={[serving]} onSelectService={vi.fn()} />);

    expect(
      screen.getByText("Registry rejected the image."),
    ).toBeInTheDocument();
  });

  it("shows deduplicated New, Edited, and Removed draft impact on each service card", () => {
    render(
      <ResourceGrid
        resources={[resources[0]]}
        serviceDraftOperations={{
          svc_worker: ["add", "edit", "remove"],
        }}
        onSelectService={vi.fn()}
      />,
    );

    const draft = screen.getByLabelText("Draft changes");
    expect(draft).toHaveTextContent("DraftNewEditedRemoved");
  });
});
