/**
 * Layout-level tests for the relaid-out AppShell.
 *
 *   - children render inside <main id="main-outlet">;
 *   - content is full-width by default (the dock floats, no reserved column);
 *   - a docked column is reserved ONLY when the dock is pinned AND open.
 *
 * The AgentDock talks to TanStack Query; we wrap the shell in a fresh
 * QueryClient so renders stay isolated.
 */

import { describe, it, expect, beforeEach } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AppShell } from './AppShell';
import {
  __resetAgentDockStateForTests,
  setAgentDockCollapsed,
  setAgentDockPinned,
} from './AgentPanel/agent-dock-state';

function renderShell(children: React.ReactNode = <div data-testid="probe">hi</div>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  return render(
    <QueryClientProvider client={client}>
      <AppShell>{children}</AppShell>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  window.localStorage.clear();
  __resetAgentDockStateForTests(true, false);
  window.history.replaceState(null, '', '/');
});

describe('AppShell', () => {
  it('renders children inside <main id="main-outlet">', () => {
    renderShell(<p data-testid="probe">canvas-here</p>);
    const main = document.getElementById('main-outlet');
    expect(main).not.toBeNull();
    expect(main?.tagName).toBe('MAIN');
    expect(main?.contains(screen.getByTestId('probe'))).toBe(true);
  });

  it('does not reserve a docked column by default (dock floats)', () => {
    const { container } = renderShell();
    const inner = container.querySelector('[data-dock-reserved]') as HTMLElement;
    expect(inner.dataset.dockReserved).toBe('false');
    expect(inner.style.paddingRight).toBe('');
  });

  it('reserves a docked column only when pinned AND open', () => {
    const { container } = renderShell();
    const inner = container.querySelector('[data-dock-reserved]') as HTMLElement;

    act(() => {
      setAgentDockCollapsed(false); // open
      setAgentDockPinned(true); // pinned
    });
    expect(inner.dataset.dockReserved).toBe('true');
    expect(inner).toHaveClass('app-shell-content');

    act(() => setAgentDockPinned(false)); // unpin → floats again
    expect(inner.dataset.dockReserved).toBe('false');
  });

  it('shows a floating action button when the dock is collapsed', () => {
    renderShell();
    expect(screen.getByTestId('agent-dock-fab')).toBeInTheDocument();
    act(() => setAgentDockCollapsed(false));
    expect(screen.queryByTestId('agent-dock-fab')).toBeNull();
    expect(screen.getByTestId('agent-dock')).toBeInTheDocument();
  });
});
