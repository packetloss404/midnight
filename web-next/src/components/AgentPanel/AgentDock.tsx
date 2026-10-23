import { useEffect, useMemo, useRef, useState } from 'react';
import { ConversationHeader } from './ConversationHeader';
import { MessageList } from './MessageList';
import { Composer } from './Composer';
import { CostFooter } from './CostFooter';
import { AGENT_SETUP_DOCS_PATH, describeAgentFailure } from './agent-error';
import {
  openAgentDock,
  setAgentDockCollapsed,
  setAgentDockPinned,
  useAgentDockCollapsed,
  useAgentDockPinned,
} from './agent-dock-state';
import {
  useConversations,
  useMessages,
  useCreateConversation,
  usePostMessage,
  useAgentStream,
} from '../../api/agent-hooks';

export interface AgentRouteContext {
  /** Current route id (for pinning context across navigation). */
  routeId: string;
  /** Project the user is currently viewing, if any. */
  projectId?: string | null;
  /** Service the user is currently viewing, if any. */
  serviceId?: string | null;
  /** Active environment (`?env=`) the user is viewing, if any. */
  environmentId?: string | null;
  /** Pretty label like "web (production)" or "prj_abc canvas". */
  contextLabel?: string | null;
}

export interface AgentDockProps {
  context: AgentRouteContext;
  /** Optional storage key prefix so dock state persists across reloads. */
  storageKeyPrefix?: string;
  /** Default model name displayed in the header. */
  modelName?: string;
}

const DOCK_WIDTH = 440;

/**
 * Persistent right-rail agent dock.
 *
 * Dimensions
 *   - expanded:  width 440px, full viewport height, fixed right side.
 *   - collapsed: width 48px, single vertical strip with a "✦" toggle and an
 *                unread dot when a stream completed while collapsed.
 *
 * Persistence
 *   - `pinned` and `collapsed` are stored in localStorage so the dock state
 *     survives reloads.
 *
 * Streams DO NOT abort on route changes — `useAgentStream` is owned by this
 * dock component, which is mounted once at the AppShell level.
 */
export function AgentDock({ context, storageKeyPrefix = 'midnight.agent', modelName = 'Midnight agent' }: AgentDockProps) {
  // `collapsed` is owned by the shared module store (agent-dock-state.ts)
  // so AppShell + TopNav can subscribe to it without a context wrapper.
  // The store persists to the same `midnight.agent.collapsed` localStorage
  // key the dock used historically.
  const collapsed = useAgentDockCollapsed();
  const setCollapsed = setAgentDockCollapsed;
  // `pinned` now lives in the shared store so AppShell can reserve a docked
  // column when the panel is pinned (opt-in). Unpinned = floating overlay.
  const pinned = useAgentDockPinned();
  const [currentId, setCurrentId] = useState<string | null>(() =>
    readString(`${storageKeyPrefix}.currentId`),
  );
  const [unread, setUnread] = useState(false);

  // Persist state.
  useEffect(() => writeString(`${storageKeyPrefix}.currentId`, currentId), [currentId, storageKeyPrefix]);

  const conversationsQ = useConversations({
    project_id: context.projectId ?? null,
    service_id: context.serviceId ?? null,
  });
  const messagesQ = useMessages(currentId);
  const createConvo = useCreateConversation();
  const postMsg = usePostMessage();
  const stream = useAgentStream();

  // Clear unread when the dock is expanded.
  useEffect(() => {
    if (!collapsed) setUnread(false);
  }, [collapsed]);

  // Flag unread when a stream finishes while collapsed.
  const wasStreaming = useRefPrev(stream.isStreaming);
  useEffect(() => {
    if (wasStreaming && !stream.isStreaming && collapsed) {
      setUnread(true);
    }
  }, [stream.isStreaming, wasStreaming, collapsed]);

  const handleNew = async () => {
    if (createConvo.isPending) return;
    stream.cancel();
    try {
      const c = await createConvo.mutateAsync({
        project_id: context.projectId ?? null,
        service_id: context.serviceId ?? null,
        title: 'New conversation',
      });
      setCurrentId(c.id);
    } catch {
      // The mutation error is rendered below.
    }
  };

  const handleSelect = (id: string) => {
    if (id === currentId) return;
    stream.cancel();
    setCurrentId(id);
  };

  // Resolves `true` once the message is accepted; `false` lets the composer
  // keep the draft so a failed send can be retried without retyping.
  const handleSend = async (input: {
    content: string;
    allow_mutations: boolean;
    allowed_tools: string[];
  }): Promise<boolean> => {
    try {
      let cid = currentId;
      if (!cid) {
        const c = await createConvo.mutateAsync({
          project_id: context.projectId ?? null,
          service_id: context.serviceId ?? null,
          title: input.content.slice(0, 48),
        });
        cid = c.id;
        setCurrentId(cid);
      }
      // Open the SSE stream BEFORE posting so token-level deltas from the
      // synchronous send are captured (see agent.go#sendMessageResponse). The
      // POST resolves with the authoritative user/assistant messages, and its
      // onSuccess invalidation reconciles the cache with server truth.
      await stream.start(cid);
      await postMsg.mutateAsync({ conversationId: cid, input });
      return true;
    } catch {
      stream.cancel();
      // Swallow: the failure is surfaced to the user via createConvo.isError /
      // postMsg.isError (rendered below). Catching here also prevents an
      // unhandled rejection from the onSend caller.
      return false;
    }
  };

  const messages = messagesQ.data ?? [];
  const conversations = conversationsQ.data ?? [];

  // Cost footer: prefer in-flight usage, else the last assistant message's usage.
  const usage = useMemo(() => {
    if (stream.turn.usage) return stream.turn.usage;
    for (let i = messages.length - 1; i >= 0; i -= 1) {
      const m = messages[i];
      if (m && m.role === 'assistant' && m.usage) return m.usage;
    }
    return undefined;
  }, [stream.turn.usage, messages]);

  if (collapsed) {
    // Collapsed = a floating action button pinned to the bottom-right corner
    // (the existing ✦ glyph). It floats over content and reserves no layout.
    return (
      <button
        type="button"
        onClick={openAgentDock}
        aria-label="Open agent panel"
        data-testid="agent-dock-fab"
        className="fixed bottom-5 right-5 z-50 inline-flex h-12 w-12 items-center justify-center rounded-full border border-line-strong bg-surface-2 text-lg text-accent shadow-lg transition-colors hover:bg-surface-3"
      >
        <span aria-hidden="true">✦</span>
        {unread && (
          <span
            aria-label="unread"
            data-testid="agent-unread-dot"
            className="absolute right-1.5 top-1.5 h-2 w-2 rounded-full"
            style={{
              background: 'var(--agent-unread-dot)',
              boxShadow: '0 0 0 2px var(--surface-2)',
            }}
          />
        )}
        {stream.isStreaming && (
          <span
            aria-label="streaming"
            className="agent-cursor absolute bottom-1.5 right-1.5"
            data-testid="agent-collapsed-streaming"
          />
        )}
      </button>
    );
  }

  return (
    <aside
      data-testid="agent-dock"
      aria-label="Agent panel"
      style={{ width: `min(${DOCK_WIDTH}px, 100vw)` }}
      className="fixed right-0 top-0 z-50 flex h-[100dvh] max-w-full flex-col border-l border-line bg-surface shadow-lg"
    >
      <ConversationHeader
        modelName={modelName}
        contextLabel={context.contextLabel ?? null}
        conversations={conversations}
        currentId={currentId}
        onSelect={handleSelect}
        onNew={handleNew}
        onTogglePin={() => setAgentDockPinned(!pinned)}
        pinned={pinned}
        onCollapse={() => setCollapsed(true)}
      />
      <MessageList
        messages={messages}
        streamingTurn={stream.turn}
        isStreaming={stream.isStreaming}
      />
      {(createConvo.isError || postMsg.isError) && (
        <AgentFailureNotice error={postMsg.error ?? createConvo.error} />
      )}
      <Composer
        isStreaming={stream.isStreaming}
        onSend={handleSend}
        onCancel={stream.cancel}
        availableTools={[]}
        disabled={createConvo.isPending || postMsg.isPending}
      />
      <CostFooter usage={usage} />
    </aside>
  );
}

function AgentFailureNotice({ error }: { error: unknown }) {
  const failure = describeAgentFailure(error);
  return (
    <div
      role="alert"
      data-testid="agent-send-error"
      className="mx-3 mb-1 grid gap-1 rounded-md border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger"
    >
      <strong className="font-semibold">{failure.title}</strong>
      {failure.detail ? <span className="text-text">{failure.detail}</span> : null}
      {failure.notConfigured ? (
        <a href={AGENT_SETUP_DOCS_PATH} className="w-fit font-medium text-accent underline">
          Agent setup guide
        </a>
      ) : null}
      {failure.code ? (
        <span className="font-mono text-[10px] text-muted">{failure.code}</span>
      ) : null}
    </div>
  );
}

// ── Small helpers ───────────────────────────────────────────────────────

function readString(key: string): string | null {
  if (typeof window === 'undefined') return null;
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}
function writeString(key: string, v: string | null) {
  if (typeof window === 'undefined') return;
  try {
    if (v === null) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, v);
  } catch {
    // The dock remains usable when browser policy disables local storage.
  }
}

// Captures the previous render's value of `v` (lags by one render).
function useRefPrev<T>(v: T): T | undefined {
  const ref = useRef<T | undefined>(undefined);
  const prev = ref.current;
  ref.current = v;
  return prev;
}
