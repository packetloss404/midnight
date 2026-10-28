import { afterEach, describe, expect, it, vi } from "vitest";
import type { SSEOptions } from "./sse";

const streamState = vi.hoisted(() => ({
  options: undefined as SSEOptions | undefined,
  path: "",
  closed: false,
}));

vi.mock("./sse", () => ({
  connectSSE: vi.fn((path: string, options: SSEOptions) => {
    streamState.path = path;
    streamState.options = options;
    streamState.closed = false;
    return {
      close: () => {
        streamState.closed = true;
      },
      get closed() {
        return streamState.closed;
      },
    };
  }),
}));

import { createConversation, getConversation, openAgentStream, postMessage, AgentRequestError } from "./agent";

afterEach(() => {
  vi.restoreAllMocks();
  streamState.options = undefined;
  streamState.path = "";
  streamState.closed = false;
});

describe("agent HTTP client", () => {
  it("sends the explicit project, service and environment conversation scope", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({id: "cnv_scoped"}), {status: 201}));
    const input = {project_id: "prj_one", service_id: "svc_one", environment_id: "env_one", title: "Inspect this environment"};
    await createConversation(input);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1alpha/agent/conversations", expect.objectContaining({method: "POST", body: JSON.stringify(input)}));
  });
  it("encodes opaque conversation IDs before constructing a route", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          id: "cnv/one",
          title: "Conversation",
          created_at: "2026-08-26T00:00:00Z",
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await getConversation("cnv/one");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1alpha/agent/conversations/cnv%2Fone",
      expect.any(Object),
    );
  });

  it("does not expose an upstream HTML error body", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("<!doctype html><title>Proxy failure</title><p>internal path</p>", {
        status: 502,
        statusText: "Bad Gateway",
      }),
    );

    const error = await getConversation("cnv_1").catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(Error);
    expect((error as Error).message).toContain("Agent request failed (502)");
    expect((error as Error).message).not.toContain("internal path");
  });

  it("exposes the readable parts of an RFC 7807 problem body", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          type: "https://midnight.dev/docs/errors#AGENT_NOT_CONFIGURED",
          title: "Agent not configured",
          status: 503,
          detail: "No agent provider is configured; set ANTHROPIC_API_KEY.",
          code: "AGENT_NOT_CONFIGURED",
        }),
        { status: 503, headers: { "content-type": "application/problem+json" } },
      ),
    );

    const error = await getConversation("cnv_1").catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(AgentRequestError);
    const failure = error as AgentRequestError;
    expect(failure.status).toBe(503);
    expect(failure.code).toBe("AGENT_NOT_CONFIGURED");
    expect(failure.title).toBe("Agent not configured");
    expect(failure.detail).toContain("ANTHROPIC_API_KEY");
    expect(failure.message).not.toContain("{");
  });

  it("derives the code from the problem type when no code field is present", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          type: "https://midnight.dev/docs/errors#INTERNAL_ERROR",
          title: "Internal error",
          status: 500,
          detail: "agent: anthropic API key not configured",
        }),
        { status: 500 },
      ),
    );

    const error = (await getConversation("cnv_1").catch((reason: unknown) => reason)) as AgentRequestError;
    expect(error.code).toBe("INTERNAL_ERROR");
    expect(error.message).toBe("Internal error: agent: anthropic API key not configured");
  });

  it("normalizes the actual synchronous send response", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          user_message: {
            id: "msg_user",
            conversation_id: "cnv_1",
            seq: 1,
            role: "user",
            content: [{ kind: "text", text: "Hello" }],
            created_at: "2026-08-26T00:00:00Z",
          },
          assistant_message: {
            id: "msg_assistant",
            conversation_id: "cnv_1",
            seq: 2,
            role: "assistant",
            content: [{ kind: "text", text: "Hi" }],
            created_at: "2026-08-26T00:00:01Z",
          },
          stop_reason: "end_turn",
          stream_url: "/api/v1alpha/agent/conversations/cnv_1/stream",
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const response = await postMessage("cnv_1", { content: "Hello" });

    expect(response.user_message.content).toEqual([
      { type: "text", text: "Hello" },
    ]);
    expect(response.assistant_message.content).toEqual([
      { type: "text", text: "Hi" },
    ]);
    expect(response.stop_reason).toBe("end_turn");
  });

  it("exposes stream readiness only after the authenticated SSE response opens", async () => {
    const onEvent = vi.fn();
    const handle = openAgentStream("cnv/one", onEvent);
    let opened = false;
    void handle.ready.then(() => {
      opened = true;
    });

    await Promise.resolve();
    expect(opened).toBe(false);
    expect(streamState.path).toBe(
      "/agent/conversations/cnv%2Fone/stream",
    );

    streamState.options?.onOpen?.();
    await expect(handle.ready).resolves.toBeUndefined();
    expect(opened).toBe(true);
    handle.close();
  });

  it("rejects stream readiness when opening fails", async () => {
    const onEvent = vi.fn();
    const handle = openAgentStream("cnv_1", onEvent);
    const failure = expect(handle.ready).rejects.toThrow("SSE unavailable");

    streamState.options?.onError?.(new Error("SSE unavailable"));

    await failure;
    expect(onEvent).toHaveBeenCalledWith({
      type: "error",
      message: "SSE unavailable",
    });
  });
});
