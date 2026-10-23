import * as Popover from '@radix-ui/react-popover';
import { forwardRef } from 'react';
import type { ButtonHTMLAttributes, ReactNode } from 'react';
import type { Conversation } from '../../api/agent';

export interface ConversationHeaderProps {
  modelName: string;
  /** e.g. "web (production)" — the current page context */
  contextLabel: string | null;
  conversations: Conversation[];
  currentId: string | null;
  onSelect: (id: string) => void;
  onNew: () => void;
  onTogglePin: () => void;
  pinned: boolean;
  onCollapse: () => void;
}

export function ConversationHeader(props: ConversationHeaderProps) {
  return (
    <header
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        padding: '10px 12px',
        borderBottom: '1px solid var(--line)',
        background: 'var(--surface)',
      }}
    >
      <div style={{ display: 'flex', flexDirection: 'column', minWidth: 0, flex: 1 }}>
        <div
          style={{
            fontFamily: 'var(--font-mono)',
            fontSize: 11,
            color: 'var(--muted)',
            textTransform: 'lowercase',
            letterSpacing: 0.3,
          }}
        >
          {props.modelName}
        </div>
        {props.contextLabel && (
          <div
            data-testid="agent-context-chip"
            style={{
              display: 'inline-flex',
              alignSelf: 'flex-start',
              alignItems: 'center',
              gap: 4,
              marginTop: 2,
              padding: '1px 6px',
              borderRadius: 999,
              background: 'var(--info-soft)',
              color: 'var(--info)',
              fontSize: 11,
              fontFamily: 'var(--font-mono)',
            }}
          >
            <span aria-hidden style={{ width: 6, height: 6, borderRadius: 999, background: 'var(--info)' }} />
            {props.contextLabel}
          </div>
        )}
      </div>

      <HeaderButton onClick={props.onNew} title="New conversation" aria-label="New conversation">
        +
      </HeaderButton>

      <Popover.Root>
        <Popover.Trigger asChild>
          <HeaderButton title="Conversation history" aria-label="Conversation history">
            <HistoryIcon />
          </HeaderButton>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            align="end"
            sideOffset={4}
            style={{
              width: 280,
              maxHeight: 320,
              overflowY: 'auto',
              background: 'var(--surface-2)',
              border: '1px solid var(--line)',
              borderRadius: 'var(--r)',
              boxShadow: 'var(--shadow)',
              padding: 6,
              zIndex: 60,
            }}
          >
            {props.conversations.length === 0 && (
              <div style={{ padding: '8px 10px', color: 'var(--muted)', fontSize: 12 }}>
                No conversations yet.
              </div>
            )}
            {props.conversations.map((c) => (
              <button
                key={c.id}
                type="button"
                onClick={() => props.onSelect(c.id)}
                aria-current={c.id === props.currentId ? 'true' : undefined}
                style={{
                  display: 'block',
                  width: '100%',
                  textAlign: 'left',
                  padding: '6px 8px',
                  borderRadius: 'var(--r-sm)',
                  background:
                    c.id === props.currentId ? 'var(--surface-3)' : 'transparent',
                  border: 'none',
                  color: 'var(--text)',
                  cursor: 'pointer',
                  fontSize: 12,
                }}
              >
                <div style={{ fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {c.title}
                </div>
                <div style={{ color: 'var(--subtle)', fontSize: 11, fontFamily: 'var(--font-mono)' }}>
                  {c.id}
                </div>
              </button>
            ))}
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>

      <HeaderButton
        onClick={props.onTogglePin}
        title={props.pinned ? 'Unpin panel' : 'Pin panel'}
        aria-pressed={props.pinned}
        aria-label={props.pinned ? 'Unpin panel' : 'Pin panel'}
      >
        <PinIcon active={props.pinned} />
      </HeaderButton>

      <HeaderButton onClick={props.onCollapse} title="Collapse" aria-label="Collapse panel">
        »
      </HeaderButton>
    </header>
  );
}

const HeaderButton = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { children: ReactNode }
>(function HeaderButton({ children, ...rest }, ref) {
  return (
    <button
      ref={ref}
      type="button"
      {...rest}
      style={{
        width: 28,
        height: 28,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: 'transparent',
        border: '1px solid var(--line)',
        borderRadius: 'var(--r-sm)',
        color: 'var(--text)',
        cursor: 'pointer',
        fontSize: 14,
        lineHeight: 1,
      }}
    >
      {children}
    </button>
  );
});

function HistoryIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden>
      <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.4" />
      <path d="M8 5v3l2 1.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}

function PinIcon({ active }: { active: boolean }) {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill={active ? 'currentColor' : 'none'} aria-hidden>
      <path
        d="M10 1.5 14.5 6l-3 1-3.5 3.5L7 8 3.5 11.5l-1-1L6 7l-2-1 1-3z"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinejoin="round"
      />
    </svg>
  );
}
