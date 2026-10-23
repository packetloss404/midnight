import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const storageAPI = vi.hoisted(() => ({
  backup: vi.fn(),
  restore: vi.fn(),
  apply: vi.fn(),
  idle: {
    mutate: vi.fn(),
    isPending: false,
    error: null,
  },
}));

vi.mock("./storage/api", () => ({
  useVolumeRecovery: () => ({
    isPending: false,
    error: null,
    data: {
      entries: [
        {
          volume: {
            id: "vol_1",
            project_id: "prj_1",
            environment_id: "env_1",
            service_id: "svc_1",
            name: "data",
            size_bytes: 1_073_741_824,
            status: "attached",
            mount_path: "/data",
            created_at: "2026-07-28T00:00:00Z",
            updated_at: "2026-07-28T00:00:00Z",
          },
          backups: [
            {
              id: "vbkp_1",
              volume_id: "vol_1",
              trigger_kind: "manual",
              state: "succeeded",
              object_bytes: 100,
              source_bytes: 200,
              created_at: "2026-07-28T00:00:00Z",
            },
          ],
          restores: [
            {
              id: "vrst_staged",
              backup_id: "vbkp_1",
              source_volume_id: "vol_1",
              state: "staged",
              replacement_volume_id: "vol_2",
              source_retained: true,
              attachment_swap_staged: true,
              restored_bytes: 200,
              created_at: "2026-07-28T00:05:00Z",
            },
            {
              id: "vrst_applied",
              backup_id: "vbkp_1",
              source_volume_id: "vol_1",
              state: "applied",
              replacement_volume_id: "vol_3",
              source_retained: true,
              attachment_swap_staged: true,
              restored_bytes: 200,
              created_at: "2026-07-28T00:10:00Z",
            },
          ],
          policies: [],
          capabilities: {
            backups: true,
            restores: true,
            policies: true,
          },
        },
      ],
    },
  }),
  useCreateVolume: () => storageAPI.idle,
  useCreateBackupPolicy: () => storageAPI.idle,
  useDeleteBackupPolicy: () => storageAPI.idle,
  useCreateVolumeBackup: () => ({
    mutate: storageAPI.backup,
    isPending: false,
    error: null,
  }),
  useCreateVolumeRestore: () => ({
    mutate: storageAPI.restore,
    isPending: false,
    error: null,
  }),
  useApplyVolumeRestore: () => ({
    mutate: storageAPI.apply,
    isPending: false,
    error: null,
  }),
}));

import { VolumeRecoveryPanel } from "./VolumeRecoveryPanel";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("VolumeRecoveryPanel", () => {
  it("shows honest staged recovery state and invokes each recovery action", async () => {
    const user = userEvent.setup();
    render(
      <VolumeRecoveryPanel
        projectId="prj_1"
        environmentId="env_1"
        serviceId="svc_1"
      />,
    );

    expect(screen.getByText("/data · 1.0 GiB")).toBeInTheDocument();
    expect(screen.getByText(/source retained · redeploy handed off/i)).toBeInTheDocument();
    expect(screen.getByText(/the service switches over when that deployment finishes/i)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Back up now" }));
    await user.click(screen.getByRole("button", { name: "Restore to replacement" }));
    await user.click(screen.getByRole("button", { name: "Apply & redeploy" }));

    expect(storageAPI.backup).toHaveBeenCalledWith({
      volumeId: "vol_1",
      retentionDays: 30,
    });
    expect(storageAPI.restore).toHaveBeenCalledWith({ backupId: "vbkp_1" });
    expect(storageAPI.apply).toHaveBeenCalledWith({ restoreId: "vrst_staged" });
  });

  it("does not imply storage exists without an environment scope", () => {
    render(<VolumeRecoveryPanel serviceId="svc_1" />);

    expect(screen.getByText("Persistent storage")).toBeInTheDocument();
    expect(screen.getByText(/choose a project environment/i)).toBeInTheDocument();
    expect(screen.queryByText("data")).not.toBeInTheDocument();
  });
});
