import type { ReactNode } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    title,
    "aria-label": ariaLabel,
  }: {
    children: ReactNode;
    to: string;
    title?: string;
    "aria-label"?: string;
  }) => (
    <a href={to} title={title} aria-label={ariaLabel}>
      {children}
    </a>
  ),
}));

import {
  AppSidebar,
  SIDEBAR_COMPACT_STORAGE_KEY,
} from "./AppSidebar";

describe("AppSidebar documentation navigation", () => {
  beforeEach(() => {
    window.localStorage.removeItem(SIDEBAR_COMPACT_STORAGE_KEY);
  });

  it("renders the bottom Docs group with every stable destination", () => {
    render(<AppSidebar />);

    const docs = screen.getByRole("region", { name: "Docs" });
    expect(
      within(docs).getByText("DOCS", { exact: true }),
    ).toBeInTheDocument();
    expect(
      within(docs).getByRole("link", { name: "Docs home" }),
    ).toHaveAttribute("href", "/docs");
    expect(
      within(docs).getByRole("link", { name: "Agent Setup" }),
    ).toHaveAttribute("href", "/docs/agent-setup");
    expect(
      within(docs).getByRole("link", { name: "API reference" }),
    ).toHaveAttribute("href", "/docs/api");
    expect(
      within(docs).getByRole("link", { name: "CLI reference" }),
    ).toHaveAttribute("href", "/docs/cli");
  });

  it("keeps one book entry in the compact rail", () => {
    render(<AppSidebar compact />);

    const docs = screen.getByRole("region", { name: "Docs" });
    expect(
      within(docs).getByRole("link", { name: "Docs home" }),
    ).toBeInTheDocument();
    expect(
      within(docs).queryByRole("link", { name: "Agent Setup" }),
    ).not.toBeInTheDocument();
    expect(
      within(docs).queryByRole("link", { name: "API reference" }),
    ).not.toBeInTheDocument();
    expect(
      within(docs).queryByRole("link", { name: "CLI reference" }),
    ).not.toBeInTheDocument();
  });

  it("lets the user expand a route-default compact rail and persists the choice", async () => {
    const user = userEvent.setup();
    const { unmount } = render(<AppSidebar compact />);

    const primary = screen.getByRole("navigation", { name: "Primary" });
    expect(primary).toHaveAttribute("data-compact", "true");
    const expand = screen.getByRole("button", { name: "Expand sidebar" });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    expect(expand).toHaveAttribute("aria-controls", primary.id);
    await user.click(expand);
    expect(primary).toHaveAttribute("data-compact", "false");
    expect(screen.getByRole("button", { name: "Collapse sidebar" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(
      within(screen.getByRole("region", { name: "Docs" })).getByRole("link", {
        name: "CLI reference",
      }),
    ).toBeInTheDocument();
    expect(window.localStorage.getItem(SIDEBAR_COMPACT_STORAGE_KEY)).toBe(
      "false",
    );

    unmount();
    render(<AppSidebar compact />);
    expect(screen.getByRole("navigation", { name: "Primary" })).toHaveAttribute(
      "data-compact",
      "false",
    );
  });

  it("collapses the wide rail and restores it on demand", async () => {
    const user = userEvent.setup();
    render(<AppSidebar />);

    const primary = screen.getByRole("navigation", { name: "Primary" });
    await user.click(screen.getByRole("button", { name: "Collapse sidebar" }));
    expect(primary).toHaveAttribute("data-compact", "true");
    await user.click(screen.getByRole("button", { name: "Expand sidebar" }));
    expect(primary).toHaveAttribute("data-compact", "false");
  });
});
