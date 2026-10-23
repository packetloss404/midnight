/**
 * TopNav (top bar) tests:
 *   - breadcrumb cases;
 *   - the environment switcher renders (enabled) when a project + env data
 *     are provided, and is wired to the callback;
 *   - the "Open agent" affordance shows only while the dock is collapsed.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { TopNav, breadcrumbFor } from './TopNav';
import {
  __resetAgentDockStateForTests,
  setAgentDockCollapsed,
} from './AgentPanel/agent-dock-state';
import type { Environment } from '@/api/client';

const envs: Environment[] = [
  {
    id: 'env_prod',
    project_id: 'prj_abc',
    slug: 'production',
    name: 'Production',
    tier: 'prod',
    is_protected: true,
    resource_version: 1,
    created_at: '',
    updated_at: '',
  },
];

beforeEach(() => {
  __resetAgentDockStateForTests(true, false);
});

describe('TopNav', () => {
  it('renders "Midnight" when the route is /', () => {
    render(<TopNav pathname="/" />);
    expect(screen.getByTestId('crumb-root')).toHaveTextContent('Midnight');
    expect(screen.queryByTestId('crumb-1')).toBeNull();
  });

  it('renders "Midnight / <projectId>" when the route is /projects/<id>', () => {
    render(<TopNav pathname="/projects/prj_abc123" />);
    expect(screen.getByTestId('crumb-root')).toHaveTextContent('Midnight');
    expect(screen.getByTestId('crumb-1')).toHaveTextContent('prj_abc123');
  });

  it('prefers the tenant-visible project label over the opaque id', () => {
    render(
      <TopNav
        pathname="/projects/prj_abc123"
        projectId="prj_abc123"
        projectLabel="Midnight API"
      />,
    );
    expect(screen.getByTestId('crumb-1')).toHaveTextContent('Midnight API');
  });

  it('breadcrumbFor handles canvas + nested routes', () => {
    expect(breadcrumbFor('/')).toEqual(['Midnight']);
    expect(breadcrumbFor('/projects/prj_x')).toEqual(['Midnight', 'prj_x']);
    expect(breadcrumbFor('/projects/prj_x/deployments')).toEqual([
      'Midnight',
      'prj_x',
      'Deployments',
    ]);
    expect(breadcrumbFor('/settings')).toEqual(['Midnight', 'Instance settings']);
  });

  it("builds stable documentation breadcrumbs", () => {
    expect(breadcrumbFor("/docs")).toEqual(["Midnight", "Docs"]);
    expect(breadcrumbFor("/docs/agent-setup")).toEqual([
      "Midnight",
      "Docs",
      "Agent Setup",
    ]);
    expect(breadcrumbFor("/docs/api")).toEqual([
      "Midnight",
      "Docs",
      "API reference",
    ]);
    expect(breadcrumbFor("/docs/cli")).toEqual([
      "Midnight",
      "Docs",
      "CLI reference",
    ]);
  });

  it('renders an enabled environment switcher wired to the callback', () => {
    const onSelectEnvironment = vi.fn();
    render(
      <TopNav
        pathname="/projects/prj_abc"
        projectId="prj_abc"
        environmentId="env_prod"
        environments={envs}
        onSelectEnvironment={onSelectEnvironment}
      />,
    );
    const sw = screen.getByTestId('env-switcher') as HTMLSelectElement;
    expect(sw).toBeEnabled();
    expect(sw).toHaveValue('env_prod');
    expect(screen.getByText('Production')).toBeInTheDocument();
    expect(screen.getByText('Compare environments')).toBeInTheDocument();

    fireEvent.change(sw, { target: { value: 'all' } });
    expect(onSelectEnvironment).toHaveBeenCalledWith('all');
  });

  it('shows the "Open agent" button only while the dock is collapsed', () => {
    render(<TopNav pathname="/" />);
    expect(screen.getByTestId('open-agent-button')).toBeInTheDocument();
    act(() => setAgentDockCollapsed(false));
    expect(screen.queryByTestId('open-agent-button')).toBeNull();
  });

  it("offers an explicit local-only logout after revoke failure", () => {
    const onLocalLogout = vi.fn();
    render(
      <TopNav
        pathname="/"
        authMode="human"
        identity={{ displayName: "Person", email: "person@example.com" }}
        logoutError="The control plane is unavailable."
        onLocalLogout={onLocalLogout}
      />,
    );

    fireEvent.click(screen.getByLabelText("Open account menu"));
    expect(screen.getByRole("alert")).toHaveTextContent(
      "The control plane is unavailable. The credential is still present in this tab.",
    );
    fireEvent.click(
      screen.getByRole("button", {
        name: "Sign out locally without revoking",
      }),
    );
    expect(onLocalLogout).toHaveBeenCalledTimes(1);
  });
});
