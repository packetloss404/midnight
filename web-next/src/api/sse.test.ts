import { afterEach, expect, test, vi } from "vitest";
import {
  setHumanSessionToken,
  setOperatorRecoveryToken,
} from "./credentials";
import { connectSSE, type SSEHandle } from "./sse";

afterEach(() => {
  window.sessionStorage.clear();
  vi.unstubAllGlobals();
});

test("connectSSE replays with opaque Last-Event-ID after clean disconnect", async () => {
  const encoder = new TextEncoder();
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => {
    const call = fetchMock.mock.calls.length;
    const token = call === 1 ? "opaque-one" : "opaque-two";
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(
          encoder.encode(`id: ${token}\nevent: log\ndata: {"message":"ready"}\n\n`),
        );
        controller.close();
      },
    });
    return new Response(body, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);

  const messages: string[] = [];
  let handle: SSEHandle;
  await new Promise<void>((resolve, reject) => {
    handle = connectSSE("/observability/logs/stream?project_id=prj_1", {
      reconnectOnClose: true,
      maxRetries: 3,
      backoffMs: 1,
      onMessage(message) {
        messages.push(message.id ?? "");
        if (messages.length === 2) {
          handle.close();
          resolve();
        }
      },
      onError: reject,
    });
  });

  expect(messages).toEqual(["opaque-one", "opaque-two"]);
  expect(fetchMock).toHaveBeenCalledTimes(2);
  const secondHeaders = fetchMock.mock.calls[1][1]?.headers as Record<string, string>;
  expect(secondHeaders["Last-Event-ID"]).toBe("opaque-one");
});

test("connectSSE uses product authority only when the caller requests it", async () => {
  setHumanSessionToken("midn_user_stream-reader");
  setOperatorRecoveryToken("operator-recovery");
  const encoder = new TextEncoder();
  const fetchMock = vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit) => {
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(
          encoder.encode('event: operation\ndata: {"state":"RUNNING"}\n\n'),
        );
        controller.close();
      },
    });
    return new Response(body, { status: 200 });
    },
  );
  vi.stubGlobal("fetch", fetchMock);

  await new Promise<void>((resolve, reject) => {
    const handle = connectSSE("/operations/op_1/events?stream=sse", {
      auth: "product",
      onMessage() {
        handle.close();
        resolve();
      },
      onError: reject,
    });
  });

  const headers = fetchMock.mock.calls[0]?.[1]?.headers as Record<
    string,
    string
  >;
  expect(headers.Authorization).toBe("Bearer midn_user_stream-reader");
});
