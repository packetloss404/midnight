import { useEffect, useRef } from 'react';
import type { AgentMessage } from '../../api/agent';
import { Message } from './Message';
import type { StreamingTurn } from '../../api/agent-hooks';

export interface MessageListProps {
  messages: AgentMessage[];
  streamingTurn?: StreamingTurn;
  isStreaming: boolean;
}

/**
 * Scrolling message list. Auto-pins to bottom when new content arrives,
 * unless the user has scrolled away from the bottom.
 */
export function MessageList({ messages, streamingTurn, isStreaming }: MessageListProps) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const stuckToBottom = useRef(true);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el || !stuckToBottom.current) return;
    el.scrollTop = el.scrollHeight;
  }, [messages.length, streamingTurn?.text, streamingTurn?.toolCalls.length, isStreaming]);

  const handleScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
    stuckToBottom.current = dist < 32;
  };

  const inflightMsg: AgentMessage | null =
    isStreaming && streamingTurn
      ? {
          id: '__inflight__',
          role: 'assistant',
          seq: messages.length,
          created_at: new Date().toISOString(),
          content: [
            ...(streamingTurn.text ? [{ type: 'text' as const, text: streamingTurn.text }] : []),
            ...streamingTurn.toolCalls.flatMap((tc) => {
              const blocks: AgentMessage['content'] = [
                { type: 'tool_use', id: tc.id, name: tc.name, input: tc.input },
              ];
              if (tc.result !== undefined) {
                const block: AgentMessage['content'][number] = {
                  type: 'tool_result',
                  tool_use_id: tc.id,
                  content: tc.result,
                };
                if (tc.isError !== undefined) {
                  (block as { is_error?: boolean }).is_error = tc.isError;
                }
                blocks.push(block);
              }
              return blocks;
            }),
          ],
        }
      : null;

  return (
    <div
      ref={scrollRef}
      onScroll={handleScroll}
      data-testid="agent-message-list"
      style={{
        flex: 1,
        overflowY: 'auto',
        padding: '12px',
        display: 'flex',
        flexDirection: 'column',
        gap: 10,
      }}
    >
      {messages.length === 0 && !inflightMsg && (
        <div
          style={{
            color: 'var(--muted)',
            fontSize: 13,
            textAlign: 'center',
            margin: 'auto',
            maxWidth: 280,
          }}
        >
          Ask the agent about your services, deploys, or infrastructure. It can call read-only
          tools by default; enable mutations in the composer to allow actions.
        </div>
      )}
      {messages.map((m) => (
        <Message key={m.id} message={m} />
      ))}
      {inflightMsg && <Message message={inflightMsg} streaming />}
      {streamingTurn?.error && (
        <div
          role="alert"
          style={{
            color: 'var(--status-error)',
            background: 'var(--status-error-soft)',
            border: '1px solid var(--status-error)',
            borderRadius: 'var(--r-sm)',
            padding: '6px 10px',
            fontSize: 12,
          }}
        >
          {streamingTurn.error}
        </div>
      )}
    </div>
  );
}
