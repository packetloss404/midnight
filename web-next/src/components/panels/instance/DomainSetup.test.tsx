import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  query: {
    data: {
      configured: true,
      root_domain: "example.com",
      console_hostname: "midnight.example.com",
      apps_domain: "apps.example.com",
      ingress_target: "203.0.113.10",
      active_apps_domain: "apps.example.com",
      version: 1,
      restart_required: false,
    },
    isPending: false,
    error: null,
    refetch: vi.fn(),
  },
  save: {
    mutateAsync: vi.fn().mockResolvedValue({}),
    isPending: false,
    isSuccess: false,
    data: undefined,
    error: null,
  },
}));

vi.mock("./api", () => ({
  useInstanceDomains: () => state.query,
  useSaveInstanceDomains: () => state.save,
}));

import { DomainSetup } from "./DomainSetup";

describe("DomainSetup", () => {
  beforeEach(() => {
    state.save.mutateAsync.mockClear();
  });

  it("shows the console and wildcard DNS records and saves the owned domain", async () => {
    const user = userEvent.setup();
    render(<DomainSetup />);

    expect(screen.getByText("midnight.example.com")).toBeInTheDocument();
    expect(screen.getAllByText("*.apps.example.com")).toHaveLength(2);
    expect(screen.getAllByText("203.0.113.10")).toHaveLength(2);
    expect(screen.getByText(/opens a public Midnight page at/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Save domain setup" }));
    expect(state.save.mutateAsync).toHaveBeenCalledWith({
      rootDomain: "example.com",
      ingressTarget: "203.0.113.10",
      expectedVersion: 1,
    });
  });
});
