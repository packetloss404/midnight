import { useEffect, useRef, useState } from 'react';
import * as Popover from '@radix-ui/react-popover';
import * as Switch from '@radix-ui/react-switch';
import * as Checkbox from '@radix-ui/react-checkbox';

export interface ComposerProps {
  disabled?: boolean;
  isStreaming: boolean;
  /** Resolve/return `false` (or reject) to keep the draft in the textarea. */
  onSend: (input: {
    content: string;
    allow_mutations: boolean;
    allowed_tools: string[];
  }) => void | boolean | Promise<void | boolean>;
  onCancel: () => void;
  availableTools: string[];
}

const DEFAULT_TOOLS = [
  'list_projects',
  'list_services',
  'get_service',
  'get_deployment',
  'get_logs',
  'get_metrics',
];

/**
 * Chat composer:
 *   - Enter submits, Shift+Enter inserts newline.
 *   - Textarea autogrows up to 8 lines (~176px).
 *   - "Tools" popover lets the user toggle which tools the agent may call.
 *   - "Allow mutations" switch is off by default; the backend gates writes on it.
 *   - During streaming the send button becomes Cancel.
 *   - The draft is cleared only once `onSend` succeeds, so a failed send
 *     leaves the message in place for a retry.
 */
export function Composer(props: ComposerProps) {
  const [text, setText] = useState('');
  const [allowMutations, setAllowMutations] = useState(false);
  const tools = props.availableTools.length ? props.availableTools : DEFAULT_TOOLS;
  const [enabledTools, setEnabledTools] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(tools.map((t) => [t, true])),
  );
  const taRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    const el = taRef.current;
    if (!el) return;
    el.style.height = 'auto';
    const lineHeight = 20;
    const maxHeight = lineHeight * 8 + 16; // 8 lines + padding
    el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`;
  }, [text]);

  const send = async () => {
    const content = text.trim();
    if (!content || props.disabled || props.isStreaming) return;
    const allowed = Object.entries(enabledTools)
      .filter(([, v]) => v)
      .map(([k]) => k);
    let accepted: void | boolean;
    try {
      accepted = await props.onSend({
        content,
        allow_mutations: allowMutations,
        allowed_tools: allowed,
      });
    } catch {
      accepted = false;
    }
    if (accepted !== false) setText('');
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  };

  return (
    <div
      style={{
        borderTop: '1px solid var(--line)',
        background: 'var(--surface)',
        padding: 10,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
      }}
    >
      <div
        style={{
          display: 'flex',
          alignItems: 'flex-end',
          gap: 6,
          border: '1px solid var(--line)',
          borderRadius: 'var(--r)',
          background: 'var(--surface-2)',
          padding: '6px 8px',
        }}
      >
        <textarea
          ref={taRef}
          data-testid="agent-composer-input"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="Ask the agent..."
          rows={1}
          disabled={props.disabled}
          style={{
            flex: 1,
            background: 'transparent',
            border: 'none',
            outline: 'none',
            color: 'var(--text)',
            font: 'inherit',
            resize: 'none',
            lineHeight: '20px',
            maxHeight: 176,
            overflowY: 'auto',
          }}
        />
        {props.isStreaming ? (
          <button
            type="button"
            onClick={props.onCancel}
            data-testid="agent-composer-cancel"
            style={{
              background: 'var(--status-error-soft)',
              color: 'var(--status-error)',
              border: '1px solid var(--status-error)',
              borderRadius: 'var(--r-sm)',
              padding: '4px 10px',
              cursor: 'pointer',
              fontWeight: 500,
            }}
          >
            Cancel
          </button>
        ) : (
          <button
            type="button"
            onClick={() => void send()}
            data-testid="agent-composer-send"
            disabled={!text.trim() || props.disabled}
            style={{
              background: 'var(--accent)',
              color: 'var(--accent-ink)',
              border: 'none',
              borderRadius: 'var(--r-sm)',
              padding: '4px 12px',
              cursor: text.trim() && !props.disabled ? 'pointer' : 'not-allowed',
              opacity: text.trim() && !props.disabled ? 1 : 0.5,
              fontWeight: 600,
            }}
          >
            Send
          </button>
        )}
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <Popover.Root>
          <Popover.Trigger asChild>
            <button
              type="button"
              data-testid="agent-tools-button"
              style={pillStyle}
            >
              Tools ({Object.values(enabledTools).filter(Boolean).length})
            </button>
          </Popover.Trigger>
          <Popover.Portal>
            <Popover.Content
              side="top"
              sideOffset={6}
              align="start"
              style={{
                width: 240,
                background: 'var(--surface-2)',
                border: '1px solid var(--line)',
                borderRadius: 'var(--r)',
                padding: 8,
                boxShadow: 'var(--shadow)',
                zIndex: 60,
              }}
            >
              <div style={{ color: 'var(--muted)', fontSize: 11, marginBottom: 6 }}>
                Tools available to this conversation
              </div>
              {tools.map((t) => {
                const id = `tool-${t}`;
                return (
                  <label
                    key={t}
                    htmlFor={id}
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 8,
                      padding: '4px 2px',
                      cursor: 'pointer',
                      fontSize: 12,
                      fontFamily: 'var(--font-mono)',
                    }}
                  >
                    <Checkbox.Root
                      id={id}
                      checked={enabledTools[t] !== false}
                      onCheckedChange={(v) =>
                        setEnabledTools((prev) => ({ ...prev, [t]: v === true }))
                      }
                      style={{
                        width: 14,
                        height: 14,
                        border: '1px solid var(--line-strong)',
                        borderRadius: 3,
                        background: 'var(--surface)',
                      }}
                    >
                      <Checkbox.Indicator>
                        <CheckIcon />
                      </Checkbox.Indicator>
                    </Checkbox.Root>
                    {t}
                  </label>
                );
              })}
            </Popover.Content>
          </Popover.Portal>
        </Popover.Root>

        <label
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 6,
            fontSize: 12,
            color: 'var(--muted)',
            cursor: 'pointer',
          }}
        >
          <Switch.Root
            checked={allowMutations}
            onCheckedChange={setAllowMutations}
            data-testid="agent-allow-mutations"
            style={{
              width: 28,
              height: 16,
              background: allowMutations ? 'var(--accent)' : 'var(--surface-3)',
              borderRadius: 999,
              position: 'relative',
              border: '1px solid var(--line)',
            }}
          >
            <Switch.Thumb
              style={{
                display: 'block',
                width: 12,
                height: 12,
                background: 'var(--text)',
                borderRadius: 999,
                transform: allowMutations ? 'translateX(13px)' : 'translateX(1px)',
                transition: 'transform 120ms ease',
                marginTop: 1,
              }}
            />
          </Switch.Root>
          Allow mutations
        </label>
      </div>
    </div>
  );
}

const pillStyle: React.CSSProperties = {
  background: 'var(--surface-2)',
  color: 'var(--text)',
  border: '1px solid var(--line)',
  borderRadius: 999,
  padding: '3px 10px',
  fontSize: 12,
  cursor: 'pointer',
};

function CheckIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden>
      <path d="M1 5 4 8 9 2" stroke="var(--accent)" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
