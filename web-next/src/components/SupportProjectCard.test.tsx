import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { MIDNIGHT_SUPPORT_URL, SupportProjectCard } from "./SupportProjectCard";

describe("SupportProjectCard", () => {
  it("links to Ko-fi without presenting contributions as entitlement", () => {
    render(<SupportProjectCard />);
    const link = screen.getByRole("link", {
      name: "Support on Ko-fi (opens in a new tab)",
    });
    expect(link).toHaveAttribute("href", MIDNIGHT_SUPPORT_URL);
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(screen.getByText(/never change usage limits/i)).toBeInTheDocument();
  });
});
