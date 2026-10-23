import {
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  domainEnabled: [] as boolean[],
  access: {
    data: {
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: "prj_1",
      environment_id: "env_1",
      environment_restricted: true,
      permissions: ["domains.read", "domains.write"],
    },
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  list: {
    data: [] as Array<Record<string, unknown>>,
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  create: {
    mutateAsync: vi.fn(),
    isPending: false,
  },
  remove: {
    mutateAsync: vi.fn(),
    isPending: false,
  },
  customList: {
    data: [] as Array<Record<string, unknown>>,
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  customCreate: {
    mutateAsync: vi.fn(),
    isPending: false,
  },
  customVerify: {
    mutateAsync: vi.fn(),
    isPending: false,
  },
  customRemove: {
    mutateAsync: vi.fn(),
    isPending: false,
  },
}));

vi.mock("@/components/panels/automation-tokens/api", () => ({
  useProjectAccess: () => state.access,
}));

vi.mock("./domains/api", () => ({
  useServiceDomains: (
    _target: Record<string, string>,
    enabled: boolean,
  ) => {
    state.domainEnabled.push(enabled);
    return state.list;
  },
  useCreateDomain: () => state.create,
  useDeleteDomain: () => state.remove,
  normalizeDomainState: (record: {
    route_reachable: boolean;
    route_apply_state: string;
  }) =>
    record.route_reachable
      ? {
          pillStatus: "active",
          label: "Trusted ingress ready",
          description: "Trusted ingress evidence is complete.",
          inFlight: false,
        }
      : {
          pillStatus: "verifying",
          label: "Awaiting trusted ingress evidence",
          description: "Trusted ingress evidence is pending.",
          inFlight: true,
        },
}));

vi.mock("./domains/custom-api", () => ({
  useCustomDomains: () => state.customList,
  useCreateCustomDomain: () => state.customCreate,
  useVerifyCustomDomain: () => state.customVerify,
  useDeleteCustomDomain: () => state.customRemove,
}));

import { ApiError } from "@/api/client";
import { DomainList } from "./DomainList";

const domain = {
  id: "dom_1",
  workspace_id: "wrk_1",
  project_id: "prj_1",
  service_id: "svc_1",
  environment_id: "env_1",
  kind: "generated",
  hostname: "svc-one.example.test",
  path: "/",
  target_port: 8080,
  public: true,
  tls: false,
  version: 4,
  route_apply_state: "ready",
  route_reachable: true,
  created_at: "2026-07-29T12:00:00Z",
  updated_at: "2026-07-29T12:01:00Z",
};

const customDomain = {
  ...domain,
  id: "dom_custom",
  kind: "custom",
  hostname: "api.example.test",
  ownership_state: "pending",
  routing_dns: {
    record_type: "CNAME",
    record_name: "api.example.test",
    record_value: "apps.example.test",
    state: "operator_required",
    note: "Create the CNAME at your DNS provider.",
  },
  dns_challenge: {
    record_name: "_midnight-challenge.api.example.test",
    record_value: "midnight-verification=token",
    state: "pending",
    attempts: 0,
  },
};

beforeEach(() => {
  vi.clearAllMocks();
  state.domainEnabled = [];
  state.access.data = {
    principal_kind: "human",
    workspace_id: "wrk_1",
    project_id: "prj_1",
    environment_id: "env_1",
    environment_restricted: true,
    permissions: ["domains.read", "domains.write"],
  };
  state.access.isPending = false;
  state.access.error = null;
  state.list.data = [];
  state.list.isPending = false;
  state.list.error = null;
  state.create.isPending = false;
  state.remove.isPending = false;
  state.create.mutateAsync.mockReset();
  state.remove.mutateAsync.mockReset();
  state.list.refetch.mockResolvedValue({ data: [] });
  state.customList.data = [];
  state.customList.isPending = false;
  state.customList.error = null;
  state.customCreate.isPending = false;
  state.customVerify.isPending = false;
  state.customRemove.isPending = false;
  state.customCreate.mutateAsync.mockReset();
  state.customVerify.mutateAsync.mockReset();
  state.customRemove.mutateAsync.mockReset();
  state.customList.refetch.mockResolvedValue({ data: [] });
});

describe("DomainList", () => {
  it("fails closed before reading domains without exact domains.read permission", () => {
    state.access.data.permissions = ["service.read"];

    renderDomainList();

    expect(
      screen.getByText(/does not grant domains-read permission/i),
    ).toBeInTheDocument();
    expect(state.domainEnabled).toEqual([false]);
    expect(
      screen.queryByRole("button", { name: "Stage domain" }),
    ).toBeNull();
  });

  it("renders a permission-aware read-only surface without mutation controls", () => {
    state.access.data.permissions = ["domains.read"];
    state.list.data = [domain];

    renderDomainList();

    expect(screen.getByText(domain.hostname)).toBeInTheDocument();
    expect(screen.getByText(/You can view this domain and its routing/i)).toBeInTheDocument();
    expect(state.domainEnabled).toEqual([true]);
    expect(
      screen.queryByRole("button", { name: "Stage domain" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: `Remove ${domain.hostname}` }),
    ).toBeNull();
  });

  it("generates without collecting custom route configuration", async () => {
    const user = userEvent.setup();
    state.create.mutateAsync.mockResolvedValueOnce({
      changeset: { revision: 3 },
    });

    renderDomainList();
    await user.click(
      screen.getByRole("button", { name: "Stage domain" }),
    );

    await waitFor(() =>
      expect(state.create.mutateAsync).toHaveBeenCalledWith(),
    );
    expect(screen.getByText(/ready in Changes/i)).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Manage custom hostnames" }),
    ).toBeInTheDocument();
  });

  it("does not expose a browser link from trusted-ingress evidence alone", () => {
    state.list.data = [
      domain,
      {
        ...domain,
        id: "dom_2",
        hostname: "pending.example.test",
        route_apply_state: "file_applied",
        route_reachable: false,
      },
    ];

    renderDomainList();

    expect(screen.getByText("Trusted ingress ready")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("requires explicit confirmation and deletes the displayed version", async () => {
    const user = userEvent.setup();
    const consoleWarn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    state.list.data = [domain];
    state.remove.mutateAsync.mockResolvedValueOnce({
      changeset: { revision: 4 },
    });

    renderDomainList();
    await user.click(
      screen.getByRole("button", { name: `Remove ${domain.hostname}` }),
    );

    expect(state.remove.mutateAsync).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", {
      name: `Remove ${domain.hostname}?`,
    });
    expect(within(dialog).getByText(/using version 4/i)).toBeInTheDocument();
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "Cancel" }),
      ).toHaveFocus(),
    );
    expect(consoleWarn.mock.calls.flat().join(" ")).not.toMatch(
      /aria-describedby|DialogDescription/i,
    );
    expect(consoleError.mock.calls.flat().join(" ")).not.toMatch(
      /aria-describedby|DialogDescription/i,
    );

    await user.click(
      within(dialog).getByRole("button", {
        name: `Confirm remove ${domain.hostname}`,
      }),
    );

    await waitFor(() =>
      expect(state.remove.mutateAsync).toHaveBeenCalledWith({
        domainId: domain.id,
        expectedVersion: domain.version,
      }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    consoleWarn.mockRestore();
    consoleError.mockRestore();
  });

  it("refetches after a delete conflict and does not claim removal", async () => {
    const user = userEvent.setup();
    state.list.data = [domain];
    state.remove.mutateAsync.mockRejectedValueOnce(
      new ApiError(409, {
        error: { code: "conflict", message: "Version changed" },
      }),
    );

    renderDomainList();
    await user.click(
      screen.getByRole("button", { name: `Remove ${domain.hostname}` }),
    );
    await user.click(
      screen.getByRole("button", {
        name: `Confirm remove ${domain.hostname}`,
      }),
    );

    await waitFor(() => expect(state.list.refetch).toHaveBeenCalledTimes(1));
    expect(
      screen.getByText(/changed before it could be removed/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/was removed from generated route intent/i)).toBeNull();
  });

  it("surfaces custom-domain failures and keeps exact-version removal behind confirmation", async () => {
    const user = userEvent.setup();
    state.customList.data = [customDomain];
    state.customCreate.mutateAsync.mockRejectedValueOnce(
      new Error("Hostname admission failed"),
    );
    state.customRemove.mutateAsync.mockResolvedValueOnce(undefined);

    renderDomainList();
    await user.click(
      screen.getByRole("button", { name: "Manage custom hostnames" }),
    );

    await user.type(screen.getByRole("textbox", { name: "Hostname" }), "new.example.test");
    await user.click(screen.getByRole("button", { name: "Add hostname" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Hostname admission failed",
    );

    await user.click(
      screen.getByRole("button", { name: `Remove ${customDomain.hostname}` }),
    );
    const dialog = screen.getByRole("dialog", {
      name: `Remove ${customDomain.hostname}?`,
    });
    expect(state.customRemove.mutateAsync).not.toHaveBeenCalled();
    await user.click(
      within(dialog).getByRole("button", {
        name: `Confirm remove ${customDomain.hostname}`,
      }),
    );

    await waitFor(() =>
      expect(state.customRemove.mutateAsync).toHaveBeenCalledWith({
        domainId: customDomain.id,
        expectedVersion: customDomain.version,
      }),
    );
  });

  it("refreshes stale custom-domain verification before another attempt", async () => {
    const user = userEvent.setup();
    state.customList.data = [customDomain];
    state.customVerify.mutateAsync.mockRejectedValueOnce(
      new ApiError(409, {
        error: { code: "conflict", message: "Version changed" },
      }),
    );

    renderDomainList();
    await user.click(
      screen.getByRole("button", { name: "Manage custom hostnames" }),
    );
    await user.click(
      screen.getByRole("button", {
        name: `Verify DNS for ${customDomain.hostname}`,
      }),
    );

    await waitFor(() =>
      expect(state.customList.refetch).toHaveBeenCalledTimes(1),
    );
    expect(
      screen.getByText(/changed before DNS verification completed/i),
    ).toBeInTheDocument();
  });

  it("refreshes and closes a stale custom-domain delete after precondition failure", async () => {
    const user = userEvent.setup();
    state.customList.data = [customDomain];
    state.customRemove.mutateAsync.mockRejectedValueOnce(
      new ApiError(412, {
        error: { code: "precondition_failed", message: "Version changed" },
      }),
    );

    renderDomainList();
    await user.click(
      screen.getByRole("button", { name: "Manage custom hostnames" }),
    );
    await user.click(
      screen.getByRole("button", { name: `Remove ${customDomain.hostname}` }),
    );
    await user.click(
      screen.getByRole("button", {
        name: `Confirm remove ${customDomain.hostname}`,
      }),
    );

    await waitFor(() =>
      expect(state.customList.refetch).toHaveBeenCalledTimes(1),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(
      screen.getByText(/changed before it could be removed/i),
    ).toBeInTheDocument();
  });
});

function renderDomainList() {
  return render(
    <DomainList
      projectId="prj_1"
      serviceId="svc_1"
      environmentId="env_1"
    />,
  );
}
