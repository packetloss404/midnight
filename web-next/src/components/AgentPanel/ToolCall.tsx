import { useState } from 'react';
import * as Tabs from '@radix-ui/react-tabs';

export interface ToolCallProps {
  id: string;
  name: string;
  input: unknown;
  result?: string;
  isError?: boolean;
  /** When true, render as collapsed initially. */
  defaultCollapsed?: boolean;
}

/**
 * Collapsible tool-call chip rendered inside an assistant message.
 * States:
 *   - collapsed: shows `▸ called <name> { …input preview… }`
 *   - expanded:  Tabs for input + result
 *   - error:     same as expanded, but with --status-error border on the chip
 */
export function ToolCall(props: ToolCallProps) {
  const [open, setOpen] = useState(!props.defaultCollapsed);

  const inputPreview = previewJSON(props.input);
  const hasResult = props.result !== undefined;
  const isError = Boolean(props.isError);

  return (
    <div
      data-testid={`tool-call-${props.id}`}
      data-error={isError ? 'true' : 'false'}
      data-open={open ? 'true' : 'false'}
      style={{
        marginTop: 8,
        border: `1px solid ${isError ? 'var(--status-error)' : 'var(--line)'}`,
        borderRadius: 'var(--r)',
        background: 'var(--agent-bubble-tool)',
        overflow: 'hidden',
        fontFamily: 'var(--font-mono)',
        fontSize: 12,
      }}
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-controls={`tool-call-body-${props.id}`}
        style={{
          width: '100%',
          textAlign: 'left',
          background: 'transparent',
          border: 'none',
          color: 'var(--text)',
          padding: '6px 10px',
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          cursor: 'pointer',
        }}
      >
        <span aria-hidden style={{ display: 'inline-block', width: 10, color: 'var(--muted)' }}>
          {open ? 'v' : '>'}
        </span>
        <span style={{ color: 'var(--muted)' }}>called</span>
        <span style={{ color: isError ? 'var(--status-error)' : 'var(--accent)' }}>{props.name}</span>
        <span
          style={{
            color: 'var(--subtle)',
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            flex: 1,
            minWidth: 0,
          }}
        >
          {inputPreview}
        </span>
        {isError && (
          <span
            style={{
              color: 'var(--status-error)',
              fontSize: 11,
              textTransform: 'uppercase',
              letterSpacing: 0.4,
            }}
          >
            error
          </span>
        )}
      </button>

      {open && (
        <div
          id={`tool-call-body-${props.id}`}
          style={{ borderTop: '1px solid var(--line)', padding: 8 }}
        >
          <Tabs.Root defaultValue={hasResult ? 'result' : 'input'}>
            <Tabs.List
              style={{
                display: 'flex',
                gap: 4,
                marginBottom: 6,
                borderBottom: '1px solid var(--line-soft)',
              }}
            >
              <ToolTab value="input">input</ToolTab>
              <ToolTab value="result" disabled={!hasResult}>
                {hasResult ? (isError ? 'error' : 'result') : 'result (pending)'}
              </ToolTab>
            </Tabs.List>
            <Tabs.Content value="input">
              <pre
                style={{
                  margin: 0,
                  padding: 6,
                  background: 'var(--surface)',
                  borderRadius: 'var(--r-sm)',
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                  color: 'var(--text)',
                  maxHeight: 200,
                  overflow: 'auto',
                }}
              >
                {prettyJSON(props.input)}
              </pre>
            </Tabs.Content>
            <Tabs.Content value="result">
              <pre
                style={{
                  margin: 0,
                  padding: 6,
                  background: 'var(--surface)',
                  borderRadius: 'var(--r-sm)',
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                  color: isError ? 'var(--status-error)' : 'var(--text)',
                  maxHeight: 200,
                  overflow: 'auto',
                }}
              >
                {hasResult ? props.result : 'waiting for result…'}
              </pre>
            </Tabs.Content>
          </Tabs.Root>
        </div>
      )}
    </div>
  );
}

function ToolTab({
  value,
  disabled,
  children,
}: {
  value: string;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <Tabs.Trigger
      value={value}
      disabled={disabled}
      style={{
        background: 'transparent',
        border: 'none',
        color: 'var(--muted)',
        padding: '4px 8px',
        cursor: disabled ? 'not-allowed' : 'pointer',
        fontFamily: 'inherit',
        fontSize: 12,
      }}
    >
      {children}
    </Tabs.Trigger>
  );
}

function previewJSON(v: unknown): string {
  try {
    const s = JSON.stringify(v);
    if (!s) return '';
    if (s.length <= 60) return s;
    return s.slice(0, 57) + '…';
  } catch {
    return String(v);
  }
}

function prettyJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}
