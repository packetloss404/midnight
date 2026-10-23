import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AgentSetupDocs } from "./AgentSetupDocs";
import { CLIReferenceDocs } from "./CLIReferenceDocs";

describe("AgentSetupDocs", () => {
  it("separates the production credential path from development compatibility", () => {
    render(<AgentSetupDocs />);

    expect(screen.getByRole("heading", { name: "Set up a Midnight agent" })).toBeInTheDocument();
    expect(screen.getByText("Packaged single-host installation")).toBeInTheDocument();
    expect(screen.getByText("Local development compatibility")).toBeInTheDocument();
    expect(screen.getByText("/etc/midnight/agent.credential", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("/etc/midnight/agent-credentials.json", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("MIDNIGHT_AGENT_AUTH_DEVELOPMENT=1", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("MIDNIGHT_API_BEARER_TOKEN", { exact: false })).toBeInTheDocument();
    expect(screen.getAllByText("Developer only")).toHaveLength(1);
  });

  it("documents real boot checks and commands without overstating dry-run", () => {
    render(<AgentSetupDocs />);

    expect(screen.getByText("midnight-agent --dry-run", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("does not load the credential", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("systemctl enable --now", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("sha256sum --check", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("gpg --verify", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("untrusted placeholder", { exact: false })).toBeInTheDocument();
    expect(screen.getAllByText("cgroup v2", { exact: false }).length).toBeGreaterThan(0);
    expect(screen.getByText("heartbeats every five seconds", { exact: false })).toBeInTheDocument();
  });
});

describe("CLIReferenceDocs", () => {
  it("presents the durable human workflow and scoped automation path", () => {
    render(<CLIReferenceDocs />);

    expect(screen.getByRole("heading", { name: "Midnight CLI" })).toBeInTheDocument();
    expect(screen.getByText("midnight login --api-base", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("midnight link", { exact: false })).toBeInTheDocument();
    expect(screen.getAllByText("midnight up", { exact: false }).length).toBeGreaterThan(0);
    expect(screen.getByText("MIDNIGHT_TOKEN=midn_sa_...", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("MIDNIGHT_API_BASE=https://api.midnight.example", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("midnight logs -f --build", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("midnight admin authorize-cli", { exact: false })).toBeInTheDocument();
  });

  it("documents exact precedence and current global output formats", () => {
    render(<CLIReferenceDocs />);

    expect(screen.getByText("--token", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("MIDNIGHT_CONFIG_HOME", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("table (default), json, jsonl, or name.")).toBeInTheDocument();
    expect(screen.getByText("version · completion")).toBeInTheDocument();
    expect(screen.getByText("Homebrew, Scoop, and npm publication remain operator-gated.", { exact: false })).toBeInTheDocument();
  });
});
