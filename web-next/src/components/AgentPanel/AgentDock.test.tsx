import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  createConversation: vi.fn(),
  postMessage: vi.fn(),
  streamStart: vi.fn(),
  streamCancel: vi.fn(),
  postError: null as unknown,
}));

vi.mock("../../api/agent-hooks", () => ({
  useConversations: () => ({ data: [] }),
  useMessages: () => ({ data: [] }),
  useCreateConversation: () => ({
    mutateAsync: state.createConversation,
    isPending: false,
    isError: false,
    error: null,
  }),
  usePostMessage: () => ({
    mutateAsync: state.postMessage,
    isPending: false,
    isError: state.postError !== null,
    error: state.postError,
  }),
  useAgentStream: () => ({
    isStreaming: false,
    turn: { text: "", toolCalls: [] },
    start: state.streamStart,
    cancel: state.streamCancel,
  }),
}));

import { AgentRequestError } from "../../api/agent";
import { AgentDock } from "./AgentDock";
import { __resetAgentDockStateForTests } from "./agent-dock-state";

const context = {
  routeId: "/projects/prj_1",
  projectId: "prj_1",
  serviceId: null,
  environmentId: "env_prod",
  contextLabel: "project prj_1",
};

describe("AgentDock conversation submission", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    state.postError = null;
    window.localStorage.clear();
    __resetAgentDockStateForTests(false, false);
    state.createConversation.mockResolvedValue({
      id: "cnv_new",
      title: "Inspect production",
      created_at: "2026-08-26T00:00:00Z",
    });
    state.postMessage.mockResolvedValue({
      user_message: {},
      assistant_message: {},
      stream_url: "/stream",
    });
    state.streamStart.mockResolvedValue(undefined);
  });

  it("posts the first message to the conversation it just created", async () => {
    const user = userEvent.setup();
    render(
      <AgentDock
        context={{
          routeId: "/projects/prj_1",
          projectId: "prj_1",
          serviceId: null,
          environmentId: "env_prod",
          contextLabel: "project prj_1",
        }}
      />,
    );

    await user.type(
      screen.getByTestId("agent-composer-input"),
      "Inspect production",
    );
    await user.click(screen.getByTestId("agent-composer-send"));

    await waitFor(() =>
      expect(state.postMessage).toHaveBeenCalledWith({
        conversationId: "cnv_new",
        input: {
          content: "Inspect production",
          allow_mutations: false,
          allowed_tools: expect.any(Array),
        },
      }),
    );
    expect(state.streamStart).toHaveBeenCalledWith("cnv_new");
    expect(state.createConversation).toHaveBeenCalledWith({ project_id: "prj_1", service_id: null, environment_id: "env_prod", title: "Inspect production" });
  });

  it("waits for the SSE connection before posting and stops if opening fails", async () => {
    const user = userEvent.setup();
    let markOpen!: () => void;
    state.streamStart.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        markOpen = resolve;
      }),
    );
    const { unmount } = render(<AgentDock context={{ routeId: "/" }} />);

    await user.type(screen.getByTestId("agent-composer-input"), "Check status");
    await user.click(screen.getByTestId("agent-composer-send"));
    await waitFor(() => expect(state.streamStart).toHaveBeenCalledWith("cnv_new"));
    expect(state.postMessage).not.toHaveBeenCalled();

    markOpen();
    await waitFor(() => expect(state.postMessage).toHaveBeenCalledTimes(1));
    unmount();

    vi.clearAllMocks();
    window.localStorage.clear();
    __resetAgentDockStateForTests(false, false);
    state.createConversation.mockResolvedValue({ id: "cnv_failed" });
    state.streamStart.mockRejectedValueOnce(new Error("SSE unavailable"));
    render(<AgentDock context={{ routeId: "/" }} />);

    await user.type(screen.getByTestId("agent-composer-input"), "Try again");
    await user.click(screen.getByTestId("agent-composer-send"));
    await waitFor(() => expect(state.streamCancel).toHaveBeenCalledTimes(1));
    expect(state.postMessage).not.toHaveBeenCalled();
  });

  it("shows a denied stream handshake and preserves the unsent draft", async () => {
    const user = userEvent.setup();
    state.streamStart.mockRejectedValueOnce(new AgentRequestError(403, { detail: "Conversation access was denied." }));
    render(<AgentDock context={context} />);
    await user.type(screen.getByTestId("agent-composer-input"), "Keep this draft");
    await user.click(screen.getByTestId("agent-composer-send"));
    expect(await screen.findByTestId("agent-send-error")).toHaveTextContent("Conversation access was denied");
    expect(screen.getByTestId("agent-composer-input")).toHaveValue("Keep this draft");
    expect(state.postMessage).not.toHaveBeenCalled();
  });

  it("cancels an in-flight stream before opening a new conversation", async () => {
    const user = userEvent.setup();
    render(<AgentDock context={{ routeId: "/" }} />);

    await user.click(screen.getByRole("button", { name: "New conversation" }));

    expect(state.streamCancel).toHaveBeenCalledTimes(1);
    expect(state.createConversation).toHaveBeenCalledTimes(1);
  });

  it("clears the selected conversation and local stream when environment changes", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<AgentDock context={context} />);
    await user.click(screen.getByRole("button", { name: "New conversation" }));
    expect(state.createConversation).toHaveBeenLastCalledWith({ project_id: "prj_1", service_id: null, environment_id: "env_prod", title: "New conversation" });
    await waitFor(() => expect(localStorage.getItem("midnight.agent.currentId")).toBe("cnv_new"));
    const calls = state.streamCancel.mock.calls.length;
    rerender(<AgentDock context={{ ...context, environmentId: "env_staging", contextLabel: "staging" }} />);
    await waitFor(() => expect(state.streamCancel.mock.calls.length).toBe(calls + 1));
    expect(localStorage.getItem("midnight.agent.currentId")).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent("Start a conversation for this context");
  });

  it("does not submit a message whose conversation creation completed after navigation", async () => {
    const user = userEvent.setup();
    let resolve!: (value: {id: string}) => void;
    state.createConversation.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    const { rerender } = render(<AgentDock context={context} />);
    await user.type(screen.getByTestId("agent-composer-input"), "Inspect this service");
    await user.click(screen.getByTestId("agent-composer-send"));
    rerender(<AgentDock context={{ ...context, serviceId: "svc_other" }} />);
    resolve({id: "cnv_old_scope"});
    await waitFor(() => expect(state.streamCancel).toHaveBeenCalled());
    expect(state.postMessage).not.toHaveBeenCalled();
    expect(state.streamStart).not.toHaveBeenCalled();
  });

  it("keeps the draft in the composer when the send fails", async () => {
    const user = userEvent.setup();
    state.postMessage.mockRejectedValue(new Error("boom"));
    render(<AgentDock context={context} />);

    const input = screen.getByTestId("agent-composer-input");
    await user.type(input, "Inspect production");
    await user.click(screen.getByTestId("agent-composer-send"));

    await waitFor(() => expect(state.postMessage).toHaveBeenCalledTimes(1));
    expect(input).toHaveValue("Inspect production");
    expect(state.streamCancel).toHaveBeenCalled();
  });

  it("clears the draft once the send succeeds", async () => {
    const user = userEvent.setup();
    render(<AgentDock context={context} />);

    const input = screen.getByTestId("agent-composer-input");
    await user.type(input, "Inspect production");
    await user.click(screen.getByTestId("agent-composer-send"));

    await waitFor(() => expect(input).toHaveValue(""));
  });
});

describe("AgentDock send failures", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    __resetAgentDockStateForTests(false, false);
  });

  it("explains a missing agent provider instead of showing the raw problem body", () => {
    state.postError = new AgentRequestError(503, {
      code: "AGENT_NOT_CONFIGURED",
      title: "Agent not configured",
      detail: "No agent provider is configured.",
    });
    render(<AgentDock context={context} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(
      "The AI agent isn't configured on this server. Set ANTHROPIC_API_KEY (or another provider) and restart midnight-server.",
    );
    expect(alert).not.toHaveTextContent("{");
    expect(screen.getByRole("link", { name: "Agent setup guide" })).toHaveAttribute(
      "href",
      "/docs/agent-setup",
    );
  });

  it("recognises the legacy 500 whose detail mentions the API key", () => {
    state.postError = new AgentRequestError(500, {
      code: "INTERNAL_ERROR",
      title: "Internal error",
      detail: "agent: anthropic API key not configured",
    });
    render(<AgentDock context={context} />);

    expect(screen.getByRole("alert")).toHaveTextContent("Set ANTHROPIC_API_KEY");
    expect(screen.getByRole("link", { name: "Agent setup guide" })).toBeInTheDocument();
  });

  it("shows title, detail and code for other problem responses", () => {
    state.postError = new AgentRequestError(429, {
      code: "RATE_LIMITED",
      title: "Too many requests",
      detail: "Retry in a few seconds.",
    });
    render(<AgentDock context={context} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Too many requests");
    expect(alert).toHaveTextContent("Retry in a few seconds.");
    expect(alert).toHaveTextContent("RATE_LIMITED");
    expect(screen.queryByRole("link", { name: "Agent setup guide" })).not.toBeInTheDocument();
  });
});
