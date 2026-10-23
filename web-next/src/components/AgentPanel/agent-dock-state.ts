// Shared subscribable state for the agent dock's layout flags.
//
// Two flags, both persisted to localStorage and readable by AppShell / the
// top bar without prop-drilling or a context provider:
//
//   - collapsed: default TRUE. Collapsed = a floating action button in the
//     bottom-right corner. Expanded = the panel is visible.
//   - pinned:    default FALSE. When pinned AND expanded, the shell reserves
//     a docked column (adds right padding). When unpinned, the expanded
//     panel floats OVER the content as an overlay with no layout reflow.
//
// The dock is rendered exactly once at the AppShell level, so a module-scope
// store keeps the shell and dock synchronized without another provider.

import { useSyncExternalStore } from 'react';

const COLLAPSED_KEY = 'midnight.agent.collapsed';
const PINNED_KEY = 'midnight.agent.pinned';

type Listener = () => void;
const listeners = new Set<Listener>();

function readBool(key: string, fallback: boolean): boolean {
  if (typeof window === 'undefined') return fallback;
  try {
    const v = window.localStorage.getItem(key);
    return v === null ? fallback : v === '1';
  } catch {
    return fallback;
  }
}

function writeBool(key: string, v: boolean) {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(key, v ? '1' : '0');
  } catch {
    // Storage may be unavailable (private mode / mocked tests); the
    // in-memory value still drives the UI.
  }
}

// Default collapsed → the dock starts as a bottom-right FAB.
let collapsed = readBool(COLLAPSED_KEY, true);
let pinned = readBool(PINNED_KEY, false);

function emit() {
  for (const fn of listeners) fn();
}

// ── collapsed ─────────────────────────────────────────────────────────

export function getAgentDockCollapsed(): boolean {
  return collapsed;
}

export function setAgentDockCollapsed(next: boolean): void {
  if (next === collapsed) return;
  collapsed = next;
  writeBool(COLLAPSED_KEY, next);
  emit();
}

/** Open the agent as the sole right-side utility pane. */
export function openAgentDock(): void {
  if (typeof window !== "undefined") {
    window.dispatchEvent(
      new CustomEvent("midnight:utility-pane-open", {
        detail: "agent",
      }),
    );
  }
  setAgentDockCollapsed(false);
}

export function useAgentDockCollapsed(): boolean {
  return useSyncExternalStore(subscribe, getAgentDockCollapsed, getAgentDockCollapsed);
}

// ── pinned ────────────────────────────────────────────────────────────

export function getAgentDockPinned(): boolean {
  return pinned;
}

export function setAgentDockPinned(next: boolean): void {
  if (next === pinned) return;
  pinned = next;
  writeBool(PINNED_KEY, next);
  emit();
}

export function useAgentDockPinned(): boolean {
  return useSyncExternalStore(subscribe, getAgentDockPinned, getAgentDockPinned);
}

// ── shared subscription ────────────────────────────────────────────────

export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/** @deprecated use `subscribe`. Retained for older call sites. */
export const subscribeAgentDockCollapsed = subscribe;

// ── test seam ──────────────────────────────────────────────────────────
export function __resetAgentDockStateForTests(collapsedValue = true, pinnedValue = false): void {
  collapsed = collapsedValue;
  pinned = pinnedValue;
  writeBool(COLLAPSED_KEY, collapsedValue);
  writeBool(PINNED_KEY, pinnedValue);
  emit();
}
