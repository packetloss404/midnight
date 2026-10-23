import {
  forwardRef,
  type AnchorHTMLAttributes,
  type ReactNode,
} from "react";
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: forwardRef<
    HTMLAnchorElement,
    AnchorHTMLAttributes<HTMLAnchorElement> & {
      children: ReactNode;
      to: string;
      activeOptions?: unknown;
    }
  >(
    ({ children, to, activeOptions: _activeOptions, ...props }, ref) => (
      <a ref={ref} href={to} {...props}>{children}</a>
    ),
  ),
}));

import { Documentation } from "./Documentation";

describe("Documentation shell", () => {
  it("keeps every docs destination reachable from a reference page", () => {
    render(
      <Documentation section="api">
        <h1>Practical API reference</h1>
      </Documentation>,
    );

    expect(
      screen.getByRole("heading", { name: "Practical API reference" }),
    ).toBeInTheDocument();
    const navigation = screen.getByRole("navigation", {
      name: "Documentation",
    });
    expect(within(navigation).getByRole("link", { name: "Docs home" })).toHaveAttribute(
      "href",
      "/docs",
    );
    expect(within(navigation).getByRole("link", { name: "Agent Setup" })).toHaveAttribute(
      "href",
      "/docs/agent-setup",
    );
    expect(within(navigation).getByRole("link", { name: "API reference" })).toHaveAttribute(
      "href",
      "/docs/api",
    );
    expect(within(navigation).getByRole("link", { name: "API reference" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(within(navigation).getByRole("link", { name: "CLI reference" })).toHaveAttribute(
      "href",
      "/docs/cli",
    );
  });

  it("accepts owned documentation content without changing the shell", () => {
    render(
      <Documentation section="agent-setup">
        <article>Agent guide content</article>
      </Documentation>,
    );

    expect(screen.getByText("Agent guide content")).toBeInTheDocument();
  });
});
