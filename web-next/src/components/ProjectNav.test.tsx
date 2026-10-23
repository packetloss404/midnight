import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
}));

import { ProjectNav } from "./ProjectNav";

describe("ProjectNav capability boundary", () => {
  it("keeps tenant project administration visible while hiding fleet observability", () => {
    render(
      <ProjectNav
        projectId="prj_1"
        environmentId="env_1"
        observabilityAvailable={false}
      />,
    );

    expect(screen.getByRole("link", { name: "Topology" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Deployments" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings" })).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Observability" }),
    ).not.toBeInTheDocument();
  });
});
