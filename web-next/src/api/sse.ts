// Reusable Server-Sent-Events abstraction.
//
// Why not `EventSource`? The browser `EventSource` cannot attach an
// `Authorization` header, so it 401s against Midnight's Bearer-protected
// SSE routes the moment a token is configured. This reader is built on
// `fetch` + a streamed `ReadableStream`, so it carries the same unified
// auth header as the rest of the client (`authHeader()`), reconnects with
// exponential backoff, and surfaces typed, named events.
//
// Frame format (matches `internal/api/*` SSE writers):
//   event: <name>\n
//   data: <json>\n\n
// `:` comment lines (heartbeats) are ignored.
//
// Used by the deploy-event stream (`/operations/{id}/events?stream=sse`,
// event name `operation`) and the log stream. Mirrors the lifecycle of the
// agent stream in `agent.ts` (single owner, explicit close on unmount).

import {
  API_PREFIX,
  authHeader,
  type RequestAuthPolicy,
} from "./client";

export type SSEMessage = {
  /** The SSE `event:` name; defaults to `"message"` when unnamed. */
  event: string;
  /** Raw `data:` payload (already joined across multi-line data fields). */
  data: string;
  /** Opaque/event-sequence replay token from the SSE `id:` field. */
  id?: string;
};

export type SSEHandle = {
  close: () => void;
  readonly closed: boolean;
};

export type SSEOptions = {
  /** Called for every non-heartbeat frame. */
  onMessage: (msg: SSEMessage) => void;
  /** Called after an authenticated stream response is established. */
  onOpen?: () => void;
  /** Called when the stream errors after exhausting reconnects. */
  onError?: (err: Error) => void;
  /** Called when the server closes the stream cleanly (EOF). */
  onClose?: () => void;
  /** Max reconnect attempts before giving up. Default 5. `0` disables. */
  maxRetries?: number;
  /** Base backoff in ms; doubles each attempt (capped at 10s). Default 500. */
  backoffMs?: number;
  /** Reconnect after a clean EOF. Useful for indefinite durable tails. */
  reconnectOnClose?: boolean;
  /** Credential authority for this route. Defaults to operator-only. */
  auth?: RequestAuthPolicy;
};

/**
 * Open an authenticated SSE stream at `${API_PREFIX}${path}`. Returns a
 * handle whose `close()` aborts the fetch and stops reconnection.
 */
export function connectSSE(path: string, opts: SSEOptions): SSEHandle {
  const maxRetries = opts.maxRetries ?? 5;
  const baseBackoff = opts.backoffMs ?? 500;
  let closed = false;
  let attempt = 0;
  let controller: AbortController | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let lastEventId = "";

  const scheduleReconnect = () => {
    if (closed || attempt >= maxRetries) {
      if (!closed) opts.onError?.(new Error("sse: reconnect attempts exhausted"));
      return;
    }
    const delay = Math.min(baseBackoff * 2 ** attempt, 10_000);
    attempt += 1;
    retryTimer = setTimeout(run, delay);
  };

  async function run() {
    if (closed) return;
    controller = new AbortController();
    try {
      const res = await fetch(`${API_PREFIX}${path}`, {
        method: "GET",
        headers: {
          Accept: "text/event-stream",
          ...authHeader(opts.auth ?? "operator"),
          ...(lastEventId ? { "Last-Event-ID": lastEventId } : {}),
        },
        signal: controller.signal,
      });
      if (!res.ok || !res.body) {
        throw new Error(`sse: HTTP ${res.status}`);
      }
      attempt = 0; // healthy connection resets backoff
      opts.onOpen?.();
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

      for (;;) {
        const { value, done } = await reader.read();
        if (done) {
          if (!closed) opts.onClose?.();
          if (!closed && opts.reconnectOnClose) scheduleReconnect();
          return;
        }
        buffer += decoder.decode(value, { stream: true });
        let sep: number;
        // Frames are separated by a blank line.
        while ((sep = buffer.indexOf("\n\n")) !== -1) {
          const rawFrame = buffer.slice(0, sep);
          buffer = buffer.slice(sep + 2);
          const parsed = parseFrame(rawFrame);
          if (parsed && !closed) {
            if (parsed.id) lastEventId = parsed.id;
            opts.onMessage(parsed);
          }
        }
      }
    } catch (err) {
      if (closed) return;
      if ((err as Error).name === "AbortError") return;
      scheduleReconnect();
    }
  }

  run();

  return {
    close() {
      if (closed) return;
      closed = true;
      if (retryTimer) clearTimeout(retryTimer);
      controller?.abort();
    },
    get closed() {
      return closed;
    },
  };
}

function parseFrame(frame: string): SSEMessage | null {
  let event = "message";
  let id: string | undefined;
  const dataLines: string[] = [];
  for (const line of frame.split("\n")) {
    if (line === "" || line.startsWith(":")) continue; // comment / heartbeat
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    const value = colon === -1 ? "" : line.slice(colon + 1).replace(/^ /, "");
    if (field === "event") event = value;
    else if (field === "id") id = value;
    else if (field === "data") dataLines.push(value);
  }
  if (dataLines.length === 0 && event === "message") return null;
  return { event, data: dataLines.join("\n"), id };
}
