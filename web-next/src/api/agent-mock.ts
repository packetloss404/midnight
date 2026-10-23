/**
 * Mock backend for the agent API.
 *
 * Activated only when `import.meta.env.VITE_AGENT_MOCK === '1'`. The mock
 * keeps an in-memory list of conversations and replays canned SSE chunks.
 * NEVER enabled by default.
 */

import type {
  AgentMessage,
  AgentStreamHandle,
  Conversation,
  CreateConversationInput,
  ListConversationsParams,
  PostMessageInput,
  PostMessageResponse,
  StreamEvent,
} from './agent';

export function shouldUseMock(): boolean {
  // Vite injects `import.meta.env`. Guarded for tests / SSR.
  try {
    return import.meta.env?.VITE_AGENT_MOCK === '1';
  } catch {
    return false;
  }
}

const store = {
  conversations: new Map<string, Conversation>(),
  messages: new Map<string, AgentMessage[]>(),
};

function nowIso(): string {
  return new Date().toISOString();
}

function id(prefix: string): string {
  return `${prefix}_${Math.random().toString(36).slice(2, 10)}`;
}

export async function mockCreateConversation(input: CreateConversationInput): Promise<Conversation> {
  const convo: Conversation = {
    id: id('cnv'),
    title: input.title?.trim() || 'New conversation',
    project_id: input.project_id ?? null,
    service_id: input.service_id ?? null,
    created_at: nowIso(),
    model: 'claude-sonnet-4-6',
  };
  store.conversations.set(convo.id, convo);
  store.messages.set(convo.id, []);
  return convo;
}

export async function mockListConversations(params: ListConversationsParams): Promise<Conversation[]> {
  return Array.from(store.conversations.values()).filter((c) => {
    if (params.project_id && c.project_id !== params.project_id) return false;
    if (params.service_id && c.service_id !== params.service_id) return false;
    return true;
  });
}

export async function mockGetConversation(cid: string): Promise<Conversation> {
  const c = store.conversations.get(cid);
  if (!c) throw new Error(`mock: conversation ${cid} not found`);
  return c;
}

export async function mockListMessages(cid: string): Promise<AgentMessage[]> {
  return store.messages.get(cid) ?? [];
}

export async function mockPostMessage(
  cid: string,
  input: PostMessageInput,
): Promise<PostMessageResponse> {
  const msgs = store.messages.get(cid) ?? [];
  const userMsg: AgentMessage = {
    id: id('msg'),
    role: 'user',
    seq: msgs.length,
    content: [{ type: 'text', text: input.content }],
    created_at: nowIso(),
  };
  msgs.push(userMsg);
  // The assistant turn is delivered live via the mock SSE stream. The
  // synchronous response still carries the empty persisted assistant row.
  const assistantMsg: AgentMessage = {
    id: id('msg'),
    role: 'assistant',
    seq: msgs.length,
    content: [{ type: 'text', text: '' }],
    created_at: nowIso(),
  };
  store.messages.set(cid, msgs);
  return {
    user_message: userMsg,
    assistant_message: assistantMsg,
    stop_reason: 'end_turn',
    stream_url: `/api/v1alpha/agent/conversations/${cid}/stream`,
  };
}

const CANNED_REPLY = [
  'Looking at the project, ',
  'I see two services attached. ',
  'Let me check their deploy status.',
];

export function mockOpenStream(
  _conversationId: string,
  onEvent: (ev: StreamEvent) => void,
): AgentStreamHandle {
  let closed = false;
  let i = 0;
  const timers: ReturnType<typeof setTimeout>[] = [];

  const schedule = (delay: number, fn: () => void) => {
    timers.push(setTimeout(fn, delay));
  };

  schedule(120, function emit() {
    if (closed) return;
    if (i < CANNED_REPLY.length) {
      onEvent({ type: 'delta', text: CANNED_REPLY[i]! });
      i += 1;
      schedule(160, emit);
      return;
    }
    onEvent({
      type: 'tool_use',
      id: 'tu_demo1',
      name: 'list_services',
      input: { project_id: 'prj_demo' },
    });
    schedule(220, () => {
      if (closed) return;
      onEvent({
        type: 'tool_result',
        tool_use_id: 'tu_demo1',
        content: '[{"name":"web","status":"running"},{"name":"worker","status":"crashed"}]',
      });
      schedule(160, () => {
        if (closed) return;
        onEvent({ type: 'delta', text: ' Worker is crashed; want me to redeploy it?' });
        schedule(180, () => {
          if (closed) return;
          onEvent({
            type: 'usage',
            usage: { input_tokens: 412, output_tokens: 88, cost_usd_estimated: 0.0021 },
          });
          onEvent({ type: 'done' });
          closed = true;
        });
      });
    });
  });

  return {
    ready: Promise.resolve(),
    close: () => {
      closed = true;
      for (const t of timers) clearTimeout(t);
    },
    get closed() {
      return closed;
    },
  };
}
