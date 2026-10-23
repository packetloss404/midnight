import { describe, expect, it } from "vitest";
import {
  databaseSourceCapability,
  NEW_PROJECT_SOURCES,
  templateSourceCapability,
} from "./NewProject";

describe("new project capability claims", () => {
  it("exposes immutable community discovery and the verified database flow", () => {
    const capabilities = Object.fromEntries(
      NEW_PROJECT_SOURCES.map((source) => [source.kind, source.capability]),
    );

    expect(capabilities.github).toBe("available");
    expect(capabilities.empty).toBe("available");
    expect(capabilities.template).toBe("available");
    expect(capabilities.database).toBe("setup-required");
  });

  it("keeps community discovery separate from built-in provisioning truth", () => {
    expect(templateSourceCapability(undefined)).toBe("available");
    expect(templateSourceCapability(undefined, true)).toBe("available");
    expect(
      templateSourceCapability([
        {
          slug: "node",
          name: "Node",
          version: "22",
          summary: "app",
          tags: ["application"],
          provisionable: true,
        },
      ]),
    ).toBe("available");
    expect(
      templateSourceCapability([
        {
          slug: "postgres",
          name: "Postgres",
          version: "16",
          summary: "database",
          tags: ["database"],
          provisionable: true,
        },
      ]),
    ).toBe("available");
  });

  it("advertises the database flow only when the API reports an executable provisioner", () => {
    expect(databaseSourceCapability(undefined)).toBe("setup-required");
    expect(
      databaseSourceCapability([
        {
          slug: "postgres",
          name: "PostgreSQL",
          version: "16",
          summary: "database",
          tags: ["database"],
          provisionable: false,
          capability_reason: "requires the durable runtime",
        },
      ]),
    ).toBe("setup-required");
    expect(
      databaseSourceCapability([
        {
          slug: "postgres",
          name: "PostgreSQL",
          version: "16",
          summary: "database",
          tags: ["database"],
          provisionable: true,
        },
      ]),
    ).toBe("available");
    expect(
      databaseSourceCapability([
        {
          slug: "redis",
          name: "Redis",
          version: "7.2.5",
          summary: "durable cache",
          tags: ["cache", "database"],
          provisionable: true,
        },
      ]),
    ).toBe("available");
    expect(
      databaseSourceCapability([
        {
          slug: "node",
          name: "Node",
          version: "22",
          summary: "application",
          tags: ["application"],
          provisionable: true,
        },
      ]),
    ).toBe("setup-required");
    expect(databaseSourceCapability(undefined, true)).toBe("unavailable");
  });
});
