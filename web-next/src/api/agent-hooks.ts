/**
 * TanStack Query hooks + `useAgentStream` for the agent panel.
 *
 * The streaming response intentionally bypasses TanStack Query: the SSE
 * connection is owned by `useAgentStream`, which accumulates token deltas
 * and tool events into local state and merges the final assistant turn
 * back into the messages cache when `done` arrives.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from '@tanstack/react-query';
import {
  createConversation,
  getConversation,
  listConversations,
  listMessages,
  openAgentStream,
  postMessage,
  type AgentMessage,
  type AgentStreamHandle,
  type Conversation,
  type ContentBlock,
  type CreateConversationInput,
  type ListConversationsParams,
  type PostMessageInput,
  type StreamEvent,
  type UsageBlock,
} from './agent';

// ── Query keys ──────────────────────────────────────────────────────────

export const agentKeys = {
  all: ['agent'] as const,
  conversations: (params: ListConversationsParams = {}) =>
    [...agentKeys.all, 'conversations', params] as const,
  conversation: (id: string) => [...agentKeys.all, 'conversation', id] as const,
  messages: (id: string) => [...agentKeys.all, 'conversation', id, 'messages'] as const,
};

// ── Conversation queries ────────────────────────────────────────────────

export function useConversations(
  params: ListConversationsParams = {},
): UseQueryResult<Conversation[]> {
  return useQuery({
    queryKey: agentKeys.conversations(params),
    queryFn: () => listConversations(params),
  });
}

export function useConversation(id: string | null | undefined): UseQueryResult<Conversation> {
  return useQuery({
    queryKey: agentKeys.conversation(id ?? '__none__'),
    queryFn: () => getConversation(id!),
    enabled: Boolean(id),
  });
}

export function useMessages(id: string | null | undefined): UseQueryResult<AgentMessage[]> {
  return useQuery({
    queryKey: agentKeys.messages(id ?? '__none__'),
    queryFn: () => listMessages(id!),
    enabled: Boolean(id),
  });
}

export function useCreateConversation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateConversationInput) => createConversation(input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [...agentKeys.all, 'conversations'] });
    },
  });
}

export function usePostMessage() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      conversationId,
      input,
    }: {
      conversationId: string;
      input: PostMessageInput;
    }) => {
      if (!conversationId) throw new Error('postMessage requires a conversationId');
      return postMessage(conversationId, input);
    },
    onSuccess: (_response, { conversationId }) => {
      qc.invalidateQueries({ queryKey: agentKeys.messages(conversationId) });
    },
  });
}

// ── Streaming hook ──────────────────────────────────────────────────────

export interface PendingToolCall {
  id: string;
  name: string;
  input: unknown;
  result?: string;
  isError?: boolean;
}

export interface StreamingTurn {
  text: string;
  toolCalls: PendingToolCall[];
  usage?: UsageBlock;
  error?: string;
}

export interface UseAgentStreamResult {
  /** True while an EventSource is open for this conversation. */
  isStreaming: boolean;
  /** Accumulated assistant turn so far (resets on each start). */
  turn: StreamingTurn;
  /** Open a new stream and resolve once the server has accepted it. */
  start: (conversationId: string) => Promise<void>;
  /** Close the current stream and discard the in-flight turn. */
  cancel: () => void;
}

const emptyTurn = (): StreamingTurn => ({ text: '', toolCalls: [] });

/**
 * Opens an SSE stream and accumulates the assistant turn into local state.
 * Lifecycle:
 *   - `start(id)` opens an EventSource and resets accumulators.
 *   - Each `delta` appends to `turn.text`.
 *   - `tool_use` pushes a pending call; `tool_result` attaches the result.
 *   - `usage` records the final token/cost block.
 *   - `done` closes the source and merges the turn into the messages cache.
 *   - On unmount, the EventSource is always closed.
 */
export function useAgentStream(): UseAgentStreamResult {
  const qc = useQueryClient();
  const handleRef = useRef<AgentStreamHandle | null>(null);
  const conversationIdRef = useRef<string | null>(null);
  const turnRef = useRef<StreamingTurn>(emptyTurn());
  const [turn, setTurn] = useState<StreamingTurn>(emptyTurn());
  const [isStreaming, setIsStreaming] = useState(false);

  const commit = useCallback(() => {
    // Snapshot ref to state so React re-renders.
    setTurn({
      text: turnRef.current.text,
      toolCalls: turnRef.current.toolCalls.map((t) => ({ ...t })),
      usage: turnRef.current.usage,
      error: turnRef.current.error,
    });
  }, []);

  const closeHandle = useCallback(() => {
    if (handleRef.current && !handleRef.current.closed) {
      handleRef.current.close();
    }
    handleRef.current = null;
  }, []);

  const mergeIntoCache = useCallback(
    (conversationId: string) => {
      const finalTurn = turnRef.current;
      if (!finalTurn.text && finalTurn.toolCalls.length === 0) return;

      const content: ContentBlock[] = [];
      if (finalTurn.text) content.push({ type: 'text', text: finalTurn.text });
      for (const tc of finalTurn.toolCalls) {
        content.push({ type: 'tool_use', id: tc.id, name: tc.name, input: tc.input });
        if (tc.result !== undefined) {
          content.push({
            type: 'tool_result',
            tool_use_id: tc.id,
            content: tc.result,
            is_error: tc.isError,
          });
        }
      }

      const assistantMsg: AgentMessage = {
        id: `local_${Date.now()}`,
        role: 'assistant',
        seq: 0, // server will reconcile on next refetch
        content,
        usage: finalTurn.usage,
        created_at: new Date().toISOString(),
      };

      qc.setQueryData<AgentMessage[]>(agentKeys.messages(conversationId), (prev) => {
        const list = prev ?? [];
        return [...list, assistantMsg];
      });
      qc.invalidateQueries({ queryKey: agentKeys.messages(conversationId) });
    },
    [qc],
  );

  const handleEvent = useCallback(
    (ev: StreamEvent) => {
      const t = turnRef.current;
      switch (ev.type) {
        case 'delta':
          t.text += ev.text;
          commit();
          return;
        case 'tool_use':
          t.toolCalls.push({ id: ev.id, name: ev.name, input: ev.input });
          commit();
          return;
        case 'tool_result': {
          const tc = t.toolCalls.find((x) => x.id === ev.tool_use_id);
          if (tc) {
            tc.result = ev.content;
            tc.isError = ev.is_error;
          }
          commit();
          return;
        }
        case 'usage':
          t.usage = ev.usage;
          commit();
          return;
        case 'heartbeat':
          return;
        case 'error':
          t.error = ev.message;
          commit();
          // Terminal: stop the spinner and release the stream so a failed
          // (e.g. 401'd) stream doesn't leave the composer stuck "streaming".
          closeHandle();
          setIsStreaming(false);
          return;
        case 'done': {
          const cid = conversationIdRef.current;
          if (cid) mergeIntoCache(cid);
          closeHandle();
          setIsStreaming(false);
          return;
        }
      }
    },
    [commit, mergeIntoCache, closeHandle],
  );

  const start = useCallback(
    async (conversationId: string) => {
      // Close any in-flight stream first.
      closeHandle();
      turnRef.current = emptyTurn();
      conversationIdRef.current = conversationId;
      setTurn(emptyTurn());
      setIsStreaming(true);
      const handle = openAgentStream(conversationId, handleEvent);
      handleRef.current = handle;
      try {
        await handle.ready;
        if (handleRef.current !== handle || handle.closed) {
          throw new Error('agent stream closed before opening');
        }
      } catch (error) {
        if (handleRef.current === handle) {
          closeHandle();
          setIsStreaming(false);
        }
        throw error;
      }
    },
    [closeHandle, handleEvent],
  );

  const cancel = useCallback(() => {
    closeHandle();
    turnRef.current = emptyTurn();
    setTurn(emptyTurn());
    setIsStreaming(false);
  }, [closeHandle]);

  // Always close on unmount.
  useEffect(() => {
    return () => {
      closeHandle();
    };
  }, [closeHandle]);

  return { isStreaming, turn, start, cancel };
}
