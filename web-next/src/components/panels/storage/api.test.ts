import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  __resetStagedChangesForTests,
  getStagedForService,
} from "@/lib/stagedChanges";

const client = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;

    constructor(status: number) {
      super(`Request failed (${status})`);
      this.status = status;
    }
  }
  return { apiFetch: vi.fn(), ApiError };
});
vi.mock("@/api/client", () => client);
const apiFetch = client.apiFetch;

import {
  applyVolumeRestore,
  createVolumeBackup,
  fetchVolumeRecovery,
  stageVolumeCreate,
} from "./api";

beforeEach(() => {
  vi.clearAllMocks();
  __resetStagedChangesForTests();
});

describe("volume recovery API", () => {
  it("loads backup, restore, and policy truth for exact scoped volumes", async () => {
    const controller = new AbortController();
    apiFetch.mockImplementation((path: string) => {
      if (path.startsWith("/volumes?")) {
        return Promise.resolve([
          {
            id: "vol/1",
            project_id: "prj 1",
            environment_id: "env/1",
            service_id: "svc_1",
            name: "data",
            size_bytes: 1024,
            status: "attached",
            resource_version: 3,
            created_at: "2026-07-28T00:00:00Z",
            updated_at: "2026-07-28T00:00:00Z",
          },
        ]);
      }
      if (path.endsWith("/backups")) return Promise.resolve({ data: [{ id: "vbkp_1" }] });
      if (path.endsWith("/restores")) return Promise.resolve({ data: [{ id: "vrst_1" }] });
      return Promise.resolve([{ id: "vbpol_1" }]);
    });

    const result = await fetchVolumeRecovery(
      "prj 1",
      "env/1",
      "svc_1",
      controller.signal,
    );

    expect(apiFetch).toHaveBeenNthCalledWith(
      1,
      "/volumes?project_id=prj+1&environment_id=env%2F1&service_id=svc_1",
      { signal: controller.signal },
    );
    expect(apiFetch).toHaveBeenNthCalledWith(
      2,
      "/volumes/vol%2F1/backups",
      { signal: controller.signal },
    );
    expect(apiFetch).toHaveBeenNthCalledWith(
      3,
      "/volumes/vol%2F1/restores",
      { signal: controller.signal },
    );
    expect(apiFetch).toHaveBeenNthCalledWith(
      4,
      "/volumes/vol%2F1/backup-policies",
      { signal: controller.signal },
    );
    expect(result.entries[0]?.backups).toEqual([{ id: "vbkp_1" }]);
    expect(result.entries[0]?.restores).toEqual([{ id: "vrst_1" }]);
    expect(result.entries[0]?.policies).toEqual([{ id: "vbpol_1" }]);
    expect(result.entries[0]?.capabilities).toEqual({
      backups: true,
      restores: true,
      policies: true,
    });
  });

  it("keeps volume truth visible when recovery execution is unavailable", async () => {
    apiFetch.mockImplementation((path: string) => {
      if (path.startsWith("/volumes?")) {
        return Promise.resolve([
          {
            id: "vol_1",
            project_id: "prj_1",
            environment_id: "env_1",
            service_id: "svc_1",
            name: "data",
            size_bytes: 1024,
            status: "provisioning",
            resource_version: 1,
            created_at: "2026-07-28T00:00:00Z",
            updated_at: "2026-07-28T00:00:00Z",
          },
        ]);
      }
      return Promise.reject(new client.ApiError(501));
    });

    const result = await fetchVolumeRecovery("prj_1", "env_1", "svc_1");

    expect(result.entries).toHaveLength(1);
    expect(result.entries[0]).toMatchObject({
      backups: [],
      restores: [],
      policies: [],
      capabilities: {
        backups: false,
        restores: false,
        policies: false,
      },
    });
  });

  it("uses idempotent mutations for backup and atomic restore cutover", async () => {
    apiFetch.mockResolvedValue({});

    await createVolumeBackup("vol/1", 30, "backup-once");
    await applyVolumeRestore("vrst/1", "apply-once");

    expect(apiFetch).toHaveBeenNthCalledWith(1, "/volumes/vol%2F1/backups", {
      method: "POST",
      headers: { "Idempotency-Key": "backup-once" },
      body: { retention_days: 30 },
    });
    expect(apiFetch).toHaveBeenNthCalledWith(
      2,
      "/volume-restores/vrst%2F1/apply",
      {
        method: "POST",
        headers: { "Idempotency-Key": "apply-once" },
        body: {},
      },
    );
  });

  it("stages volume intent alongside other Operations edits without a request", () => {
    stageVolumeCreate("env/1", "svc_1", {
      name: "data",
      size_bytes: 1 << 30,
      mount_path: "/var/lib/data",
    });

    expect(getStagedForService("svc_1", "env/1")).toEqual([
      {
        kind: "storage",
        serviceId: "svc_1",
        environmentId: "env/1",
        name: "data",
        op: "add",
        storage: {
          size_bytes: 1 << 30,
          mount_path: "/var/lib/data",
        },
      },
    ]);
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
