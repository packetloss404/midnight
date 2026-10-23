import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/client";

const sourceApi = vi.hoisted(() => ({
  getLink: vi.fn(),
  listRepositories: vi.fn(),
  listBranches: vi.fn(),
  putLink: vi.fn(),
  deleteLink: vi.fn(),
}));

vi.mock("./api", () => ({
  getHumanServiceGithubLink: sourceApi.getLink,
  listProjectGithubRepositories: sourceApi.listRepositories,
  listProjectGithubBranches: sourceApi.listBranches,
  putHumanServiceGithubLink: sourceApi.putLink,
  deleteHumanServiceGithubLink: sourceApi.deleteLink,
}));

import { ServiceSourcePanel } from "./ServiceSourcePanel";

const repository = {
  installation_id: 41,
  repository_id: 82,
  repository_full_name: "acme/web",
  account_login: "acme",
  private: true,
  default_branch: "main",
} as const;

const binding = {
  installation_id: 41,
  repository_id: 82,
  repository_full_name: "acme/web",
  branch: "main",
  binding_state: "bound" as const,
};

beforeEach(() => {
  vi.clearAllMocks();
  sourceApi.getLink.mockResolvedValue(null);
  sourceApi.listRepositories.mockResolvedValue([repository]);
  sourceApi.listBranches.mockResolvedValue([
    { name: "main" },
    { name: "release" },
  ]);
  sourceApi.putLink.mockResolvedValue(binding);
  sourceApi.deleteLink.mockResolvedValue(undefined);
});

function renderPanel() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <ServiceSourcePanel projectId="prj_one" serviceId="svc_one" />
    </QueryClientProvider>,
  );
}

async function reviewConnection(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    await screen.findByRole("button", { name: "Connect source" }),
  );
  expect(sourceApi.listRepositories).toHaveBeenCalledWith(
    "prj_one",
    expect.any(AbortSignal),
  );
  await user.click(
    await screen.findByRole("button", { name: /acme\/web/i }),
  );
  expect(sourceApi.listBranches).toHaveBeenCalledWith(
    "prj_one",
    82,
    expect.any(AbortSignal),
  );
  await waitFor(() =>
    expect(screen.getByLabelText("Branch")).toHaveValue("main"),
  );
  await user.click(
    screen.getByRole("button", { name: "Review connection" }),
  );
}

describe("ServiceSourcePanel", () => {
  it("requires explicit confirmation and never claims that linking deployed", async () => {
    const user = userEvent.setup();
    renderPanel();

    await reviewConnection(user);
    expect(
      screen.getByText(/source intent only; no deployment starts/i),
    ).toBeInTheDocument();
    expect(sourceApi.putLink).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole("button", { name: "Confirm connection" }),
    );

    await waitFor(() => expect(sourceApi.putLink).toHaveBeenCalledTimes(1));
    expect(sourceApi.putLink).toHaveBeenCalledWith(
      "svc_one",
      { installationId: 41, repositoryId: 82, branch: "main" },
      expect.stringMatching(/^github-link-/),
    );
    expect(
      await screen.findByText("Source linked. No deployment was started."),
    ).toBeInTheDocument();
  });

  it("reuses the same idempotency key when an exact connection is retried", async () => {
    const user = userEvent.setup();
    sourceApi.putLink
      .mockRejectedValueOnce(new Error("network interrupted"))
      .mockResolvedValueOnce(binding);
    renderPanel();

    await reviewConnection(user);
    await user.click(
      screen.getByRole("button", { name: "Confirm connection" }),
    );
    expect(await screen.findByText("network interrupted")).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Confirm connection" }),
    );

    await waitFor(() => expect(sourceApi.putLink).toHaveBeenCalledTimes(2));
    expect(sourceApi.putLink.mock.calls[0]?.[2]).toBe(
      sourceApi.putLink.mock.calls[1]?.[2],
    );
  });

  it("does not disconnect until confirmed and preserves deployment history", async () => {
    const user = userEvent.setup();
    sourceApi.getLink.mockResolvedValue(binding);
    renderPanel();

    await user.click(await screen.findByRole("button", { name: "Disconnect" }));
    expect(
      screen.getByText(/existing deployments and history remain/i),
    ).toBeInTheDocument();
    expect(sourceApi.deleteLink).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole("button", { name: "Confirm disconnect" }),
    );

    await waitFor(() => expect(sourceApi.deleteLink).toHaveBeenCalledTimes(1));
    expect(sourceApi.deleteLink).toHaveBeenCalledWith(
      "svc_one",
      expect.stringMatching(/^github-unlink-/),
    );
    expect(
      await screen.findByText(
        "Source disconnected. Existing deployments and their history were not removed.",
      ),
    ).toBeInTheDocument();
  });

  it("reuses the same idempotency key when an exact disconnect is retried", async () => {
    const user = userEvent.setup();
    sourceApi.getLink.mockResolvedValue(binding);
    sourceApi.deleteLink
      .mockRejectedValueOnce(new Error("network interrupted"))
      .mockResolvedValueOnce(undefined);
    renderPanel();

    await user.click(await screen.findByRole("button", { name: "Disconnect" }));
    await user.click(
      screen.getByRole("button", { name: "Confirm disconnect" }),
    );
    expect(await screen.findByText("network interrupted")).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Confirm disconnect" }),
    );

    await waitFor(() => expect(sourceApi.deleteLink).toHaveBeenCalledTimes(2));
    expect(sourceApi.deleteLink.mock.calls[0]?.[1]).toBe(
      sourceApi.deleteLink.mock.calls[1]?.[1],
    );
  });

  it("reports an unconfigured tenant integration without attempting a global fallback", async () => {
    const user = userEvent.setup();
    sourceApi.listRepositories.mockRejectedValue(
      new ApiError(501, {
        error: { code: "not_configured", message: "GitHub is unavailable" },
      }),
    );
    renderPanel();

    await user.click(
      await screen.findByRole("button", { name: "Connect source" }),
    );
    expect(
      await screen.findByText("GitHub source is not configured"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/no global installation lookup was attempted/i),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(
      await screen.findByText("No GitHub source is linked"),
    ).toBeInTheDocument();
  });
});
