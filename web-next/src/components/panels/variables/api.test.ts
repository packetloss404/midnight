import { beforeEach, describe, expect, it, vi } from "vitest";
import type { StagedChange } from "@/lib/stagedChanges";

const client = vi.hoisted(() => ({
  getEnvironmentChangeset: vi.fn(),
  putEnvironmentChangeset: vi.fn(),
}));

vi.mock("@/api/client", () => ({ api: client }));

import {
  commitStagedChanges,
} from "./api";

beforeEach(() => {
  vi.clearAllMocks();
  client.getEnvironmentChangeset.mockResolvedValue({ changeset: null });
  client.putEnvironmentChangeset.mockResolvedValue({
    changeset: { id: "chg-1", revision: 1, state: "draft" },
  });
});

function baseline() {
  return {
    id: "var-existing",
    workspaceId: "wsp-1",
    projectId: "project-1",
    environmentId: "env-1",
    serviceId: "service-1",
    scope: "sealed" as const,
    version: 4,
    lengthBucket: "17-64",
    sealed: true,
  };
}

describe("durable variable changeset lifecycle", () => {
  it("persists one encrypted server-side draft instead of sequential variable writes", async () => {
    const changes: StagedChange[] = [
      {
        serviceId: "service-1",
        environmentId: "env-1",
        name: "NEW_TOKEN",
        op: "add",
        scope: "secret",
        value: "new-plaintext",
      },
      {
        serviceId: "service-1",
        environmentId: "env-1",
        name: "ROTATED_TOKEN",
        op: "edit",
        scope: "sealed",
        value: "replacement-plaintext",
        base: baseline(),
      },
      {
        serviceId: "service-1",
        environmentId: "env-1",
        name: "OLD_TOKEN",
        op: "remove",
        scope: "sealed",
        value: "",
        base: { ...baseline(), id: "var-old", version: 2 },
      },
    ];

    const result = await commitStagedChanges({ changes }, "project-1");

    expect(client.putEnvironmentChangeset).toHaveBeenCalledWith(
      "env-1",
      0,
      [
        expect.objectContaining({ operation: "add", name: "NEW_TOKEN", value: "new-plaintext" }),
        expect.objectContaining({ operation: "remove", target_id: "var-old", value: undefined }),
        expect.objectContaining({ operation: "edit", target_id: "var-existing", expected_version: 4 }),
      ],
      undefined,
    );
    expect(result.applied).toHaveLength(3);
  });

  it("refuses to overwrite an existing durable draft", async () => {
    client.getEnvironmentChangeset.mockResolvedValueOnce({
      changeset: { id: "chg-existing", revision: 4, state: "draft" },
    });
    const changes: StagedChange[] = [
      {
        serviceId: "service-1",
        environmentId: "env-1",
        name: "A_NEW",
        op: "add",
        scope: "secret",
        value: "one",
      },
      {
        serviceId: "service-1",
        environmentId: "env-1",
        name: "B_EXISTING",
        op: "edit",
        scope: "sealed",
        value: "two",
        base: baseline(),
      },
    ];

    await expect(commitStagedChanges({ changes }, "project-1")).rejects.toThrow(
      "already has a saved draft",
    );
    expect(client.putEnvironmentChangeset).not.toHaveBeenCalled();
  });

  it("saves variable, runtime, networking, and storage edits in one request", async () => {
    const runtime = {
      root_directory: "",
      builder: "auto" as const,
      dockerfile_path: "",
      build_command: "",
      start_command: "npm start",
      predeploy_command: "",
      target_port: 3000,
      health_kind: "http" as const,
      health_path: "/health",
      health_port: 3000,
      health_timeout_seconds: 120,
      health_interval_seconds: 2,
      restart_policy: "unless-stopped" as const,
      restart_retries: 0,
      replicas: 1,
      overlap_enabled: true,
      drain_grace_seconds: 20,
      runtime_kind: "service" as const,
      cron_schedule: "",
      cron_timeout_seconds: 600,
      cron_max_attempts: 1,
      cron_retry_delay_seconds: 60,
    };
    const changes: StagedChange[] = [
      {
        serviceId: "service-1",
        environmentId: "env-1",
        name: "LOG_LEVEL",
        op: "add",
        scope: "plain",
        value: "info",
      },
      {
        kind: "runtime",
        serviceId: "service-1",
        environmentId: "env-1",
        name: "service configuration",
        op: "edit",
        targetId: "service-1",
        expectedVersion: 4,
        runtime,
      },
      {
        kind: "networking",
        serviceId: "service-1",
        environmentId: "env-1",
        name: "generated public domain",
        op: "add",
        networking: { kind: "generated", public: true },
      },
      {
        kind: "storage",
        serviceId: "service-1",
        environmentId: "env-1",
        name: "data",
        op: "add",
        storage: { size_bytes: 1024, mount_path: "/data" },
      },
    ];

    await commitStagedChanges({ changes, message: "Production desired state" });

    expect(client.putEnvironmentChangeset).toHaveBeenCalledWith(
      "env-1",
      0,
      expect.arrayContaining([
        expect.objectContaining({ kind: "variable", name: "LOG_LEVEL" }),
        expect.objectContaining({ kind: "runtime", runtime }),
        expect.objectContaining({ kind: "networking", networking: { kind: "generated", public: true } }),
        expect.objectContaining({ kind: "storage", storage: { size_bytes: 1024, mount_path: "/data" } }),
      ]),
      "Production desired state",
    );
  });
});
