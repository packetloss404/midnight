import type { AnchorHTMLAttributes } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...props }: AnchorHTMLAttributes<HTMLAnchorElement> & { to: string }) => (
    <a href={to} {...props}>{children}</a>
  ),
}));

import { DocsHome } from "./DocsHome";

describe("DocsHome", () => {
  it("presents the canonical alpha contract and honest deployment boundary", () => {
    render(<DocsHome />);

    expect(screen.getByRole("heading", { level: 1, name: /Ship software/ })).toBeInTheDocument();
    expect(screen.getByText("v1alpha")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Explore the API/ })).toHaveAttribute("href", "/docs/api");
    expect(screen.getByRole("link", { name: /Download OpenAPI/ })).toHaveAttribute(
      "href",
      "/api/v1alpha/openapi.yaml",
    );
    expect(screen.getByText(/A 202 response means queued/)).toBeInTheDocument();
    expect(screen.getByText(/does not silently fall back/)).toBeInTheDocument();
  });

  it("supports navigation-owned link overrides and copyable examples", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    render(<DocsHome apiReferenceHref="/reference" canonicalSpecHref="/contract.yaml" />);
    expect(screen.getByRole("link", { name: /Explore the API/ })).toHaveAttribute("href", "/reference");
    expect(screen.getByRole("link", { name: /Download OpenAPI/ })).toHaveAttribute("href", "/contract.yaml");

    await user.click(screen.getByRole("button", { name: "Copy Verify your API session" }));
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining("/api/v1alpha/auth/me"));
    expect(screen.getByText("Copied")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Verify your API session copied to clipboard",
    );
  });
});
