import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Project } from "@/api/client";

const state = vi.hoisted(() => ({
  projects: [] as Project[],
  environments: [{ id: "env_1" }, { id: "env_2" }],
  services: [
    { id: "svc_1", project_id: "prj_1", slug: "api", display_name: "API" },
    { id: "svc_2", project_id: "prj_1", slug: "worker", display_name: "Worker" },
  ],
  feed: [] as Array<{ service: { id: string }; deployment: Record<string, unknown> }>,
  environmentCalls: [] as string[],
  serviceCalls: [] as string[],
}));

vi.mock("@/api/hooks", () => ({
  useProjects: () => ({ data: state.projects, error: null, isPending: false, refetch: vi.fn() }),
  useEnvironments: (projectId: string) => {
    state.environmentCalls.push(projectId);
    return { data: state.environments, error: null, isPending: false, refetch: vi.fn() };
  },
  useServices: (projectId: string) => {
    state.serviceCalls.push(projectId);
    return { data: state.services, error: null, isPending: false, refetch: vi.fn() };
  },
}));

vi.mock("./projects/portfolio-api", () => ({
  usePortfolioDeploymentEvidence: () => ({
    deployments: state.feed.map((item) => item.deployment),
    sampledServiceCount: state.services.length,
    totalServiceCount: state.services.length,
    truncated: false,
    error: null,
    isPending: false,
    refetchAll: vi.fn(),
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ to, className, children }: { to: string; className?: string; children: ReactNode }) => <a href={to} className={className}>{children}</a>,
}));

import {
  filterAndSortProjects,
  ProjectsIndex,
  summarizeProjectDeployments,
} from "./ProjectsIndex";

const PROJECTS: Project[] = [
  { id: "prj_1", workspace_id: "wrk_1", slug: "zulu", display_name: "Zulu", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z" },
  { id: "prj_2", workspace_id: "wrk_1", slug: "alpha", display_name: "Alpha", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-08-12T00:00:00Z" },
];

beforeEach(() => {
  window.history.replaceState({}, "", "/");
  state.projects = [];
  state.feed = [];
  state.environmentCalls = [];
  state.serviceCalls = [];
});

describe("ProjectsIndex", () => {
  it("confirms a verified GitHub setup once and removes the callback marker", () => {
    window.history.replaceState({}, "", "/?github_setup=connected");
    render(<ProjectsIndex />);

    expect(
      screen.getByRole("status"),
    ).toHaveTextContent("GitHub App connected");
    expect(window.location.search).toBe("");
  });

  it("routes every visible New project action to the canonical /new flow", () => {
    render(<ProjectsIndex />);
    const links = screen.getAllByRole("link", { name: "New project" });
    expect(links).toHaveLength(2);
    for (const link of links) expect(link).toHaveAttribute("href", "/new");
  });

  it("renders tenant inventory, explicit deployment signals, search, sort, and list view", async () => {
    const user = userEvent.setup();
    state.projects = PROJECTS;
    state.feed = [
      { service: state.services[0], deployment: { id: "dep_1", service_id: "svc_1", operation_state: "FAILED", status: "failed" } },
      { service: state.services[1], deployment: { id: "dep_2", service_id: "svc_2", operation_state: "RUNNING", status: "deploying" } },
    ];
    render(<ProjectsIndex />);

    expect(state.environmentCalls).toEqual([]);
    expect(state.serviceCalls).toEqual([]);
    await user.click(screen.getByRole("button", { name: "Show Alpha status" }));
    expect(state.environmentCalls).toEqual(["prj_2"]);
    expect(state.serviceCalls).toEqual(["prj_2"]);
    expect(screen.getAllByText("2", { selector: "dd" })).toHaveLength(2);
    expect(screen.getByText(/1 failed · 1 in flight/)).toBeInTheDocument();
    await user.type(screen.getByRole("searchbox", { name: "Search projects" }), "alpha");
    const alphaLink = screen.getByRole("link", { name: /Alpha/ });
    expect(alphaLink).toBeInTheDocument();
    expect(alphaLink.getAttribute("href")).not.toContain("view=map");
    expect(screen.queryByRole("link", { name: /Zulu/ })).not.toBeInTheDocument();
    await user.clear(screen.getByRole("searchbox", { name: "Search projects" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Sort" }), "name");
    const projectLinks = screen.getAllByRole("link").filter((link) => /Alpha|Zulu/.test(link.textContent ?? ""));
    expect(projectLinks.map((link) => link.textContent)).toEqual([expect.stringContaining("Alpha"), expect.stringContaining("Zulu")]);
    await user.click(screen.getByRole("button", { name: "List" }));
    expect(screen.getByRole("list")).toHaveAttribute("data-view", "list");
  });
});

describe("portfolio derivation", () => {
  it("filters and sorts without mutating API order", () => {
    const original = [...PROJECTS];
    expect(filterAndSortProjects(PROJECTS, "prj_2", "name").map((project) => project.id)).toEqual(["prj_2"]);
    expect(filterAndSortProjects(PROJECTS, "", "recent").map((project) => project.id)).toEqual(["prj_2", "prj_1"]);
    expect(PROJECTS).toEqual(original);
  });

  it("classifies only the latest reported deployment per service", () => {
    expect(summarizeProjectDeployments(["svc_1", "svc_2", "svc_3"], [
      { id: "dep_latest", service_id: "svc_1", operation_id: "op_1", operation_state: "SUCCEEDED", status: "active", active: true },
      { id: "dep_old", service_id: "svc_1", operation_id: "op_old", operation_state: "FAILED", status: "failed", active: false },
      { id: "dep_running", service_id: "svc_2", operation_id: "op_2", operation_state: "RUNNING", status: "deploying", active: false },
    ])).toEqual({ successful: 1, inFlight: 1, failed: 0, other: 0, notReported: 1 });
  });
});
