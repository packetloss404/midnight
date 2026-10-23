import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { RuntimeConsoleCapability } from "./RuntimeConsoleCapability";

describe("RuntimeConsoleCapability", () => {
  it("renders an explicit unavailable boundary without a command affordance", () => {
    render(<RuntimeConsoleCapability />);

    expect(screen.getByText("Runtime console")).toBeInTheDocument();
    expect(screen.getByText("Unavailable")).toBeInTheDocument();
    expect(
      screen.getByText(/no authenticated, project-scoped runtime exec API/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
