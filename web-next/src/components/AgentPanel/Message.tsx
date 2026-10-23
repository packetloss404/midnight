import type { AgentMessage, ContentBlock } from '../../api/agent';
import { ToolCall } from './ToolCall';

export interface MessageProps {
  message: AgentMessage;
  /** When true, render a pulsing cursor at the end (streaming assistant turn). */
  streaming?: boolean;
}

/**
 * Renders one message. Tool blocks are folded together: a `tool_use` block
 * with a matching `tool_result` block (by id) collapses into a single
 * `ToolCall` chip.
 */
export function Message({ message, streaming }: MessageProps) {
  const isUser = message.role === 'user';
  const bubble = isUser
    ? {
        background: 'var(--agent-bubble-user)',
        color: 'var(--agent-bubble-user-ink)',
        marginLeft: 'auto',
        border: '1px solid var(--accent-soft)',
      }
    : {
        background: 'var(--agent-bubble-assistant)',
        color: 'var(--agent-bubble-assistant-ink)',
        marginRight: 'auto',
        border: '1px solid var(--line)',
      };

  return (
    <div
      data-testid={`message-${message.id}`}
      data-role={message.role}
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
        alignItems: isUser ? 'flex-end' : 'flex-start',
      }}
    >
      <div
        style={{
          ...bubble,
          padding: '8px 12px',
          borderRadius: 'var(--r)',
          maxWidth: '92%',
          fontSize: 13,
          lineHeight: 1.5,
        }}
      >
        {renderBlocks(message.content, streaming)}
      </div>
    </div>
  );
}

function renderBlocks(blocks: ContentBlock[], streaming?: boolean) {
  // Index tool_results by tool_use_id so we can fold them.
  const results = new Map<string, Extract<ContentBlock, { type: 'tool_result' }>>();
  for (const b of blocks) {
    if (b.type === 'tool_result') results.set(b.tool_use_id, b);
  }

  const nodes: React.ReactNode[] = [];
  let lastTextNodeIndex = -1;
  blocks.forEach((b, i) => {
    if (b.type === 'text') {
      lastTextNodeIndex = nodes.length;
      nodes.push(
        <div
          key={`t-${i}`}
          style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontFamily: 'var(--font-ui)' }}
        >
          {renderInlineText(b.text)}
        </div>,
      );
    } else if (b.type === 'tool_use') {
      const r = results.get(b.id);
      nodes.push(
        <ToolCall
          key={`u-${b.id}`}
          id={b.id}
          name={b.name}
          input={b.input}
          {...(r?.content !== undefined ? { result: r.content } : {})}
          {...(r?.is_error !== undefined ? { isError: r.is_error } : {})}
          defaultCollapsed
        />,
      );
    }
    // tool_result handled via folding above
  });

  if (streaming) {
    if (lastTextNodeIndex === -1) {
      nodes.push(<span key="cursor" className="agent-cursor" aria-label="streaming" />);
    } else {
      // Append cursor to the last text node
      const last = nodes[lastTextNodeIndex] as React.ReactElement<{
        children?: React.ReactNode;
      }>;
      nodes[lastTextNodeIndex] = (
        <div
          key={`t-stream-${lastTextNodeIndex}`}
          style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontFamily: 'var(--font-ui)' }}
        >
          {last.props.children}
          <span className="agent-cursor" aria-label="streaming" />
        </div>
      );
    }
  }

  return nodes;
}

/**
 * Render inline text with triple-backtick code blocks promoted to <pre>.
 * Keeps the renderer dependency-free; intentionally minimal.
 */
function renderInlineText(text: string): React.ReactNode {
  const parts: React.ReactNode[] = [];
  const fenceRe = /```(\w+)?\n([\s\S]*?)```/g;
  let lastIdx = 0;
  let m: RegExpExecArray | null;
  let key = 0;
  while ((m = fenceRe.exec(text)) !== null) {
    if (m.index > lastIdx) {
      parts.push(<span key={`s-${key++}`}>{text.slice(lastIdx, m.index)}</span>);
    }
    parts.push(
      <pre
        key={`c-${key++}`}
        style={{
          margin: '6px 0',
          padding: 8,
          background: 'var(--surface)',
          border: '1px solid var(--line)',
          borderRadius: 'var(--r-sm)',
          fontFamily: 'var(--font-mono)',
          fontSize: 12,
          overflowX: 'auto',
        }}
      >
        {m[2]}
      </pre>,
    );
    lastIdx = m.index + m[0].length;
  }
  if (lastIdx < text.length) {
    parts.push(<span key={`s-${key++}`}>{text.slice(lastIdx)}</span>);
  }
  return parts;
}
