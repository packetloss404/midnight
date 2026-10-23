import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  createHumanService: vi.fn(),
  createService: vi.fn(),
  deployHumanServiceImage: vi.fn(),
  deployServiceImage: vi.fn(),
  linkServiceGithub: vi.fn(),
  newIdempotencyKey: vi.fn(),
}));

vi.mock("./create/api", () => ({
  createHumanService: state.createHumanService,
  createService: state.createService,
  deployHumanServiceImage: state.deployHumanServiceImage,
  deployServiceImage: state.deployServiceImage,
  isImmutableImageReference: (value: string) =>
    /^[^\s@]+@sha256:[0-9a-f]{64}$/.test(value),
  newIdempotencyKey: state.newIdempotencyKey,
  slugify: (value: string) => value.toLowerCase(),
}));

vi.mock("./github/api", () => ({
  linkServiceGithub: state.linkServiceGithub,
}));

vi.mock("./github/RepoPicker", () => ({
  RepoPicker: () => <div data-testid="repo-picker" />,
}));

import { AddServiceForm } from "./AddServiceForm";

beforeEach(() => {
  vi.clearAllMocks();
  state.createService.mockResolvedValue({ id: "svc_created" });
  state.createHumanService.mockResolvedValue({ id: "svc_created" });
  state.deployHumanServiceImage.mockResolvedValue({
    id: "op_created",
    state: "queued",
  });
  let key = 0;
  state.newIdempotencyKey.mockImplementation(() => `stable-key-${++key}`);
});

describe("AddServiceForm tenant image admission", () => {
  it("keeps the global GitHub picker closed for an admin human", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AddServiceForm
          projectId="prj_1"
          environmentId="env_1"
          mutationAuthority="human"
          imageDeployAvailable={false}
          githubSourceAvailable
          installationAdminAvailable
        />
      </QueryClientProvider>,
    );

    expect(screen.queryByTestId("repo-picker")).not.toBeInTheDocument();
    expect(screen.queryByText("Container image")).not.toBeInTheDocument();
    expect(state.createService).not.toHaveBeenCalled();
    expect(state.createHumanService).not.toHaveBeenCalled();
  });

  it("creates only the service with human auth when image capability is unavailable", async () => {
    const user = userEvent.setup();
    const onCreated = vi.fn();
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AddServiceForm
          projectId="prj_1"
          environmentId="env_1"
          mutationAuthority="human"
          imageDeployAvailable={false}
          githubSourceAvailable={false}
          onCreated={onCreated}
        />
      </QueryClientProvider>,
    );

    expect(screen.queryByText("Container image")).not.toBeInTheDocument();
    expect(screen.queryByTestId("repo-picker")).not.toBeInTheDocument();
    const name = screen.getByPlaceholderText("api");
    await user.type(name, "API");
    await user.type(name, "{Enter}");

    expect(state.createHumanService).toHaveBeenCalledWith(
      {
        projectId: "prj_1",
        name: "API",
      },
      "stable-key-1",
    );
    expect(state.createService).not.toHaveBeenCalled();
    expect(state.deployHumanServiceImage).not.toHaveBeenCalled();
    expect(state.deployServiceImage).not.toHaveBeenCalled();
    expect(state.linkServiceGithub).not.toHaveBeenCalled();
    expect(onCreated).toHaveBeenCalledWith("svc_created");
  });

  it("retries the exact human deploy with one stable key and no operator mutation", async () => {
    const user = userEvent.setup();
    const onCreated = vi.fn();
    state.deployHumanServiceImage
      .mockRejectedValueOnce(new Error("temporarily unavailable"))
      .mockResolvedValueOnce({ id: "op_created", state: "queued" });
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AddServiceForm
          projectId="prj_1"
          environmentId="env_1"
          mutationAuthority="human"
          imageDeployAvailable
          githubSourceAvailable={false}
          onCreated={onCreated}
        />
      </QueryClientProvider>,
    );

    expect(
      screen.getByText(/Immutable image deployment is available/),
    ).toBeInTheDocument();
    expect(screen.queryByText("GitHub repo")).not.toBeInTheDocument();
    expect(screen.queryByTestId("repo-picker")).not.toBeInTheDocument();
    expect(
      screen.getByRole("checkbox", { name: /Generate a public URL/ }),
    ).not.toBeChecked();
    await user.type(screen.getByPlaceholderText("api"), "API");
    const image = `registry.example/team/api@sha256:${"a".repeat(64)}`;
    await user.type(
      screen.getByPlaceholderText("registry.example/app@sha256:…"),
      image,
    );
    await user.click(screen.getByRole("button", { name: "Create and deploy" }));
    expect(await screen.findByText("temporarily unavailable")).toBeVisible();
    expect(state.createHumanService).toHaveBeenCalledTimes(1);
    expect(state.deployHumanServiceImage).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Create and deploy" }));

    expect(state.createHumanService).toHaveBeenCalledTimes(1);
    expect(state.deployHumanServiceImage).toHaveBeenCalledTimes(2);
    expect(state.deployHumanServiceImage.mock.calls[0]).toEqual(
      state.deployHumanServiceImage.mock.calls[1],
    );
    expect(state.deployHumanServiceImage).toHaveBeenLastCalledWith(
      {
        serviceId: "svc_created",
        environmentId: "env_1",
        image,
      },
      "stable-key-2",
    );
    expect(state.createService).not.toHaveBeenCalled();
    expect(state.deployServiceImage).not.toHaveBeenCalled();
    expect(state.linkServiceGithub).not.toHaveBeenCalled();
    expect(onCreated).toHaveBeenCalledWith("svc_created");
  });

  it("sends the strict generated-route intent only after human opt-in", async () => {
    const user = userEvent.setup();
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AddServiceForm
          projectId="prj_1"
          environmentId="env_1"
          mutationAuthority="human"
          imageDeployAvailable
          githubSourceAvailable={false}
        />
      </QueryClientProvider>,
    );

    const image = `registry.example/team/api@sha256:${"a".repeat(64)}`;
    await user.type(
      screen.getByPlaceholderText("registry.example/app@sha256:…"),
      image,
    );
    await user.click(
      screen.getByRole("checkbox", { name: /Generate a public URL/ }),
    );
    await user.click(screen.getByRole("button", { name: "Create and deploy" }));

    expect(state.deployHumanServiceImage).toHaveBeenCalledWith(
      {
        serviceId: "svc_created",
        environmentId: "env_1",
        image,
        route: { public: true },
      },
      "stable-key-2",
    );
    expect(state.deployServiceImage).not.toHaveBeenCalled();
  });

  it("fails closed when the public URL choice changes after service creation", async () => {
    const user = userEvent.setup();
    state.deployHumanServiceImage.mockRejectedValueOnce(
      new Error("temporarily unavailable"),
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AddServiceForm
          projectId="prj_1"
          environmentId="env_1"
          mutationAuthority="human"
          imageDeployAvailable
          githubSourceAvailable={false}
        />
      </QueryClientProvider>,
    );

    await user.type(
      screen.getByPlaceholderText("registry.example/app@sha256:…"),
      `registry.example/team/api@sha256:${"a".repeat(64)}`,
    );
    await user.click(screen.getByRole("button", { name: "Create and deploy" }));
    expect(await screen.findByText("temporarily unavailable")).toBeVisible();

    await user.click(
      screen.getByRole("checkbox", { name: /Generate a public URL/ }),
    );
    await user.click(screen.getByRole("button", { name: "Create and deploy" }));

    expect(
      await screen.findByText(/different image-deploy request/),
    ).toBeVisible();
    expect(state.createHumanService).toHaveBeenCalledTimes(1);
    expect(state.deployHumanServiceImage).toHaveBeenCalledTimes(1);
  });

  it("fails closed when image input changes after the service was created", async () => {
    const user = userEvent.setup();
    state.deployHumanServiceImage.mockRejectedValueOnce(
      new Error("temporarily unavailable"),
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AddServiceForm
          projectId="prj_1"
          environmentId="env_1"
          mutationAuthority="human"
          imageDeployAvailable
          githubSourceAvailable={false}
        />
      </QueryClientProvider>,
    );

    const imageInput = screen.getByPlaceholderText(
      "registry.example/app@sha256:…",
    );
    fireEvent.change(imageInput, {
      target: { value: `registry.example/team/api@sha256:${"a".repeat(64)}` },
    });
    await user.click(screen.getByRole("button", { name: "Create and deploy" }));
    expect(await screen.findByText("temporarily unavailable")).toBeVisible();

    fireEvent.change(imageInput, {
      target: { value: `registry.example/team/api@sha256:${"b".repeat(64)}` },
    });
    await user.click(screen.getByRole("button", { name: "Create and deploy" }));

    expect(
      await screen.findByText(/different image-deploy request/),
    ).toBeVisible();
    expect(state.createHumanService).toHaveBeenCalledTimes(1);
    expect(state.deployHumanServiceImage).toHaveBeenCalledTimes(1);
    expect(state.deployServiceImage).not.toHaveBeenCalled();
  });

  it("does not create or deploy for a mutable image", async () => {
    const user = userEvent.setup();
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AddServiceForm
          projectId="prj_1"
          environmentId="env_1"
          mutationAuthority="human"
          imageDeployAvailable
          githubSourceAvailable={false}
        />
      </QueryClientProvider>,
    );

    await user.type(
      screen.getByPlaceholderText("registry.example/app@sha256:…"),
      "registry.example/team/api:latest",
    );
    await user.click(screen.getByRole("button", { name: "Create and deploy" }));

    expect(await screen.findByText(/Mutable tags are not accepted/)).toBeVisible();
    expect(state.createHumanService).not.toHaveBeenCalled();
    expect(state.deployHumanServiceImage).not.toHaveBeenCalled();
    expect(state.deployServiceImage).not.toHaveBeenCalled();
  });
});
