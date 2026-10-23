/**
 * Typed fetch wrapper for the Midnight agent API. Streaming uses the
 * authenticated fetch-based SSE reader in ./sse (not EventSource, which
 * cannot send the bearer header).
 *
 * Endpoints (v1alpha):
 *   POST   /api/v1alpha/agent/conversations
 *   GET    /api/v1alpha/agent/conversations
 *   GET    /api/v1alpha/agent/conversations/:id
 *   GET    /api/v1alpha/agent/conversations/:id/messages
 *   POST   /api/v1alpha/agent/conversations/:id/messages
 *   GET    /api/v1alpha/agent/conversations/:id/stream    (SSE)
 */

import {
  mockCreateConversation,
  mockGetConversation,
  mockListConversations,
  mockListMessages,
  mockOpenStream,
  mockPostMessage,
  shouldUseMock,
} from './agent-mock';
import { authHeader } from './client';
import { connectSSE } from './sse';

// ── Types ────────────────────────────────────────────────────────────────

export type Role = 'user' | 'assistant' | 'tool';

export type ContentBlock =
  | { type: 'text'; text: string }
  | { type: 'tool_use'; id: string; name: string; input: unknown }
  | { type: 'tool_result'; tool_use_id: string; content: string; is_error?: boolean };

export interface UsageBlock {
  model?: string;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens?: number;
  cache_read_tokens?: number;
  cost_usd_estimated?: number;
  iteration_count?: number;
}

export interface AgentMessage {
  id: string;
  role: Role;
  seq: number;
  content: ContentBlock[];
  usage?: UsageBlock;
  created_at: string;
}

export interface Conversation {
  id: string;
  title: string;
  project_id?: string | null;
  service_id?: string | null;
  created_at: string;
  model?: string;
}

export interface CreateConversationInput {
  title?: string;
  project_id?: string | null;
  service_id?: string | null;
}

export interface PostMessageInput {
  content: string;
  allow_mutations?: boolean;
  allowed_tools?: string[];
}

export interface PostMessageResponse {
  user_message: AgentMessage;
  assistant_message: AgentMessage;
  stop_reason?: string;
  stream_url: string;
}

export interface ListConversationsParams {
  project_id?: string | null;
  service_id?: string | null;
}

// ── SSE events ───────────────────────────────────────────────────────────

export type StreamEvent =
  | { type: 'delta'; text: string }
  | { type: 'tool_use'; id: string; name: string; input: unknown }
  | { type: 'tool_result'; tool_use_id: string; content: string; is_error?: boolean }
  | { type: 'usage'; usage: UsageBlock }
  | { type: 'done' }
  | { type: 'error'; message: string }
  | { type: 'heartbeat' };

export interface AgentStreamHandle {
  /** Resolves only after the authenticated SSE response is established. */
  ready: Promise<void>;
  close: () => void;
  readonly closed: boolean;
}

type PageEnvelope<T> = {
  data?: T[];
  page?: { next_cursor?: string };
};

type BackendContentBlock =
  | { kind: 'text'; text?: string }
  | { kind: 'tool_use'; tool_use?: { id: string; name: string; input?: unknown } }
  | { kind: 'tool_result'; tool_result?: { tool_use_id: string; output?: unknown; is_error?: boolean } }
  | ContentBlock;

type BackendAgentMessage = Omit<AgentMessage, 'content'> & {
  content?: BackendContentBlock[];
};

type BackendPostMessageResponse = Omit<
  PostMessageResponse,
  'user_message' | 'assistant_message'
> & {
  user_message: BackendAgentMessage;
  assistant_message: BackendAgentMessage;
};

type BackendStreamEvent = {
  kind?: string;
  delta?: string;
  tool_call?: { id: string; name: string; input?: unknown };
  tool_result?: { tool_use_id: string; output?: unknown; is_error?: boolean };
  usage?: UsageBlock;
};

// ── HTTP helpers ─────────────────────────────────────────────────────────

const API_BASE = '/api/v1alpha/agent';

/**
 * Error raised for non-2xx agent responses. When the server answered with an
 * RFC 7807 problem document the `code`, `title` and `detail` fields carry its
 * readable parts so the UI never has to show the raw JSON body.
 */
export class AgentRequestError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly title?: string;
  readonly detail?: string;

  constructor(status: number, problem?: AgentProblem) {
    const title = problem?.title?.trim() || undefined;
    const detail = problem?.detail?.trim() || undefined;
    const readable = title && detail ? `${title}: ${detail}` : title ?? detail;
    super(readable ?? `Agent request failed (${status})`);
    this.name = 'AgentRequestError';
    this.status = status;
    this.code = problem?.code || undefined;
    this.title = title;
    this.detail = detail;
  }
}

type AgentProblem = { code?: string; title?: string; detail?: string };

/** Extracts the readable parts of an RFC 7807 body; `undefined` when the body is not one. */
function parseAgentProblem(raw: string): AgentProblem | undefined {
  try {
    const body = JSON.parse(raw) as Record<string, unknown> | null;
    if (!body || typeof body !== 'object') return undefined;
    const nested =
      body.error && typeof body.error === 'object'
        ? (body.error as Record<string, unknown>)
        : undefined;
    const str = (v: unknown) => (typeof v === 'string' ? safeAgentErrorDetail(v) : '');
    const typeCode = str(body.type).split('#')[1] ?? '';
    const problem = {
      code: str(body.code) || str(nested?.code) || typeCode,
      title: str(body.title),
      detail: str(body.detail) || str(nested?.message) || str(body.message),
    };
    return problem.code || problem.title || problem.detail ? problem : undefined;
  } catch {
    return undefined;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const hasBody = init?.body !== undefined && init.body !== null;
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      accept: 'application/json',
      ...authHeader("operator"),
      ...(hasBody ? { 'content-type': 'application/json' } : {}),
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) {
    let raw = '';
    try {
      raw = await res.text();
    } catch {
      raw = res.statusText;
    }
    const problem = parseAgentProblem(raw);
    if (problem) throw new AgentRequestError(res.status, problem);
    const detail = safeAgentErrorDetail(raw);
    throw new AgentRequestError(
      res.status,
      detail ? { detail: `Agent request failed (${res.status}): ${detail}` } : undefined,
    );
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// ── Public API ───────────────────────────────────────────────────────────

export async function createConversation(input: CreateConversationInput): Promise<Conversation> {
  if (shouldUseMock()) return mockCreateConversation(input);
  return request<Conversation>('/conversations', {
    method: 'POST',
    body: JSON.stringify(input),
  });
}

export async function listConversations(params: ListConversationsParams = {}): Promise<Conversation[]> {
  if (shouldUseMock()) return mockListConversations(params);
  const qs = new URLSearchParams();
  if (params.project_id) qs.set('project_id', params.project_id);
  if (params.service_id) qs.set('service_id', params.service_id);
  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  const response = await request<Conversation[] | PageEnvelope<Conversation>>(`/conversations${suffix}`);
  return unwrapData(response);
}

export async function getConversation(id: string): Promise<Conversation> {
  if (shouldUseMock()) return mockGetConversation(id);
  return request<Conversation>(`/conversations/${encodeURIComponent(id)}`);
}

export async function listMessages(conversationId: string): Promise<AgentMessage[]> {
  if (shouldUseMock()) return mockListMessages(conversationId);
  const response = await request<BackendAgentMessage[] | PageEnvelope<BackendAgentMessage>>(
    `/conversations/${encodeURIComponent(conversationId)}/messages`,
  );
  return unwrapData(response).map(normalizeMessage);
}

export async function postMessage(
  conversationId: string,
  input: PostMessageInput,
): Promise<PostMessageResponse> {
  if (shouldUseMock()) return mockPostMessage(conversationId, input);
  const response = await request<BackendPostMessageResponse>(
    `/conversations/${encodeURIComponent(conversationId)}/messages`,
    {
      method: 'POST',
      body: JSON.stringify(input),
    },
  );
  return {
    ...response,
    user_message: normalizeMessage(response.user_message),
    assistant_message: normalizeMessage(response.assistant_message),
  };
}

/**
 * Open the SSE stream for a conversation. The returned handle lets callers
 * close the stream (e.g. on unmount or cancel). The authenticated fetch reader
 * is kept private — events flow only through the `onEvent` callback.
 *
 * If `VITE_AGENT_MOCK === '1'` the mock layer drives the stream instead.
 *
 * Closing stops local delivery; the server currently has no turn-cancel route.
 */
export function openAgentStream(
  conversationId: string,
  onEvent: (ev: StreamEvent) => void,
): AgentStreamHandle {
  if (shouldUseMock()) {
    return mockOpenStream(conversationId, onEvent);
  }

  // Authenticated SSE via fetch + ReadableStream (see api/sse.ts). EventSource
  // cannot attach the bearer header, so it 401s the moment a token is
  // configured. No reconnect: the agent stream is a live, replay-free pub/sub
  // tied to one synchronous turn; the final assistant message is recovered from
  // the POST response + refetch, so a dropped stream ends the turn, not loops.
  const state: {
    handle: ReturnType<typeof connectSSE> | null;
    sawDone: boolean;
  } = {
    handle: null,
    sawDone: false,
  };
  let readySettled = false;
  let resolveReady!: () => void;
  let rejectReady!: (error: Error) => void;
  const ready = new Promise<void>((resolve, reject) => {
    resolveReady = resolve;
    rejectReady = reject;
  });
  const openTimeout = setTimeout(() => {
    if (readySettled) return;
    readySettled = true;
    const error = new Error('agent stream did not open within 10 seconds');
    rejectReady(error);
    onEvent({ type: 'error', message: error.message });
    state.handle?.close();
  }, 10_000);

  const markReady = () => {
    if (readySettled) return;
    readySettled = true;
    clearTimeout(openTimeout);
    resolveReady();
  };

  const failReady = (error: Error) => {
    if (readySettled) return;
    readySettled = true;
    clearTimeout(openTimeout);
    rejectReady(error);
  };

  state.handle = connectSSE(`/agent/conversations/${encodeURIComponent(conversationId)}/stream`, {
    auth: "operator",
    maxRetries: 0,
    onOpen: markReady,
    onMessage: (msg) => {
      let payload: BackendStreamEvent = {};
      if (msg.data) {
        try {
          payload = JSON.parse(msg.data) as BackendStreamEvent;
        } catch (err) {
          onEvent({ type: 'error', message: `failed to parse ${msg.event}: ${(err as Error).message}` });
          return;
        }
      }
      const ev = normalizeStreamEvent(msg.event, payload);
      if (ev.type === 'done') state.sawDone = true;
      onEvent(ev);
      if (ev.type === 'done') state.handle?.close();
    },
    onError: (err) => {
      failReady(err);
      if (!state.sawDone) onEvent({ type: 'error', message: err.message });
    },
    onClose: () => {
      const error = new Error('stream closed before completion');
      failReady(error);
      if (!state.sawDone) onEvent({ type: 'error', message: error.message });
    },
  });

  return {
    ready,
    close: () => {
      failReady(new Error('agent stream closed before opening'));
      state.handle?.close();
    },
    get closed() {
      return state.handle?.closed ?? false;
    },
  };
}

function unwrapData<T>(value: T[] | PageEnvelope<T>): T[] {
  if (Array.isArray(value)) return value;
  if (Array.isArray(value?.data)) return value.data;
  return [];
}

function normalizeMessage(message: BackendAgentMessage): AgentMessage {
  return {
    ...message,
    content: (message.content ?? []).map(normalizeContentBlock).filter(Boolean) as ContentBlock[],
  };
}

function normalizeContentBlock(block: BackendContentBlock): ContentBlock | null {
  if ('type' in block) return block;
  switch (block.kind) {
    case 'text':
      return { type: 'text', text: block.text ?? '' };
    case 'tool_use':
      if (!block.tool_use) return null;
      return {
        type: 'tool_use',
        id: block.tool_use.id,
        name: block.tool_use.name,
        input: block.tool_use.input ?? {},
      };
    case 'tool_result':
      if (!block.tool_result) return null;
      return {
        type: 'tool_result',
        tool_use_id: block.tool_result.tool_use_id,
        content: stringifyToolOutput(block.tool_result.output),
        is_error: block.tool_result.is_error,
      };
    default:
      return null;
  }
}

function normalizeStreamEvent(eventName: string, payload: BackendStreamEvent): StreamEvent {
  switch (eventName) {
    case 'delta':
    case 'text_delta':
      return { type: 'delta', text: payload.delta ?? '' };
    case 'tool_use':
    case 'tool_call':
      return {
        type: 'tool_use',
        id: payload.tool_call?.id ?? '',
        name: payload.tool_call?.name ?? '',
        input: payload.tool_call?.input ?? {},
      };
    case 'tool_result':
      return {
        type: 'tool_result',
        tool_use_id: payload.tool_result?.tool_use_id ?? '',
        content: stringifyToolOutput(payload.tool_result?.output),
        is_error: payload.tool_result?.is_error,
      };
    case 'usage':
    case 'iteration_end':
      return { type: 'usage', usage: payload.usage ?? { input_tokens: 0, output_tokens: 0 } };
    case 'done':
      return { type: 'done' };
    case 'heartbeat':
      return { type: 'heartbeat' };
    default:
      return { type: 'error', message: `unknown stream event ${eventName}` };
  }
}

function stringifyToolOutput(value: unknown): string {
  if (typeof value === 'string') return value;
  if (value === undefined || value === null) return '';
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

const MAX_AGENT_ERROR_DETAIL = 280;
const HTML_ERROR_DETAIL = /<!doctype|<!--|<\/?[a-z][^>]*>/i;

function safeAgentErrorDetail(value: string): string {
  const normalized = value
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
  if (!normalized || HTML_ERROR_DETAIL.test(normalized)) return '';
  if (normalized.length <= MAX_AGENT_ERROR_DETAIL) return normalized;
  return `${normalized.slice(0, MAX_AGENT_ERROR_DETAIL - 1).trimEnd()}…`;
}
