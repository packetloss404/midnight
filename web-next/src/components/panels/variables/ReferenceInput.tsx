// A text input with inline `${{ ... }}` reference autocomplete. Reused for both
// the variable NAME and VALUE fields. Renders a keyboard-navigable popover of
// suggestions whenever the caret sits inside an open `${{` token.

import { useEffect, useMemo, useRef, useState } from "react";
import {
  activeRefToken,
  applyRefSuggestion,
  suggestRefs,
  type RefContext,
  type RefSuggestion,
} from "./helpers";

type Props = {
  value: string;
  onChange: (next: string) => void;
  refContext: RefContext;
  placeholder?: string;
  mono?: boolean;
  ariaLabel: string;
  className?: string;
  onEnter?: () => void;
};

export function ReferenceInput({
  value,
  onChange,
  refContext,
  placeholder,
  mono = true,
  ariaLabel,
  className = "",
  onEnter,
}: Props) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [caret, setCaret] = useState(0);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);

  const token = useMemo(() => activeRefToken(value, caret), [value, caret]);
  const suggestions = useMemo<RefSuggestion[]>(
    () => (token ? suggestRefs(token.body, refContext) : []),
    [token, refContext],
  );

  useEffect(() => {
    setActive(0);
  }, [value]);

  const showPopover = open && Boolean(token) && suggestions.length > 0;

  function commit(s: RefSuggestion) {
    const { value: next, caret: nextCaret } = applyRefSuggestion(value, caret, s.insert);
    onChange(next);
    // Restore caret after React re-renders the controlled value.
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(nextCaret, nextCaret);
        setCaret(nextCaret);
      }
    });
  }

  function syncCaret() {
    const el = inputRef.current;
    if (el) setCaret(el.selectionStart ?? el.value.length);
  }

  return (
    <div className="relative">
      <input
        ref={inputRef}
        type="text"
        aria-label={ariaLabel}
        aria-autocomplete="list"
        aria-expanded={showPopover}
        value={value}
        placeholder={placeholder}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        className={
          "w-full rounded-[var(--r-sm)] border border-line bg-surface px-2 py-1.5 text-xs text-text " +
          "outline-none placeholder:text-subtle focus:border-line-strong focus:shadow-[var(--focus-ring)] " +
          (mono ? "font-mono " : "") +
          className
        }
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
          requestAnimationFrame(syncCaret);
        }}
        onClick={syncCaret}
        onKeyUp={(e) => {
          if (e.key === "ArrowLeft" || e.key === "ArrowRight" || e.key === "Home" || e.key === "End")
            syncCaret();
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => {
          // Delay so a mousedown on a suggestion can register first.
          window.setTimeout(() => setOpen(false), 120);
        }}
        onKeyDown={(e) => {
          if (showPopover) {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setActive((a) => (a + 1) % suggestions.length);
              return;
            }
            if (e.key === "ArrowUp") {
              e.preventDefault();
              setActive((a) => (a - 1 + suggestions.length) % suggestions.length);
              return;
            }
            if (e.key === "Enter" || e.key === "Tab") {
              e.preventDefault();
              commit(suggestions[active]);
              return;
            }
            if (e.key === "Escape") {
              e.preventDefault();
              setOpen(false);
              return;
            }
          }
          if (e.key === "Enter" && onEnter) {
            e.preventDefault();
            onEnter();
          }
        }}
      />
      {showPopover ? (
        <ul
          role="listbox"
          aria-label="Reference suggestions"
          className="absolute z-30 mt-1 max-h-56 w-full overflow-auto rounded-[var(--r-sm)] border border-line-strong bg-surface-2 py-1 shadow-[var(--shadow-lg)]"
        >
          {suggestions.map((s, i) => (
            <li key={s.label} role="option" aria-selected={i === active}>
              <button
                type="button"
                // onMouseDown (not onClick) so it fires before input blur.
                onMouseDown={(e) => {
                  e.preventDefault();
                  commit(s);
                }}
                className={
                  "flex w-full items-center justify-between gap-3 px-2.5 py-1 text-left text-xs " +
                  (i === active ? "bg-staged-soft text-text" : "text-muted hover:bg-surface-3")
                }
              >
                <span className="font-mono">{s.label}</span>
                <span className="text-[10px] uppercase tracking-wider text-subtle">{s.kind}</span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
