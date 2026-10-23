import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BrandMark } from "./BrandMark";

describe("BrandMark", () => {
  it("is decorative by default and exposes a name only when requested", () => {
    const { rerender } = render(<BrandMark />);
    expect(screen.queryByRole("img")).not.toBeInTheDocument();

    rerender(<BrandMark title="Midnight" />);
    expect(screen.getByRole("img", { name: "Midnight" })).toBeInTheDocument();
  });
});
