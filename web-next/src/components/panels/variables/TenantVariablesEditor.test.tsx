import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  __resetStagedChangesForTests,
  getStagedForService,
} from "@/lib/stagedChanges";

const state = vi.hoisted(() => ({
  service: {
    data: {
      id: "svc_1",
      project_id: "prj_1",
      slug: "web",
      display_name: "Web",
      created_at: "2026-07-29T12:00:00Z",
      updated_at: "2026-07-29T12:00:00Z",
    },
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  access: {
    data: {
      principal_kind: "human",
      workspace_id: "wrk_1",
      project_id: "prj_1",
      environment_id: "env_1",
      environment_restricted: false,
      permissions: ["variables.read", "variables.write"],
    },
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
  accessCalls: [] as Array<{
    projectId: string;
    environmentId?: string;
    enabled: boolean;
  }>,
  variableCalls: [] as Array<{
    target: {
      projectId: string;
      serviceId: string;
      environmentId: string;
    };
    enabled: boolean;
  }>,
  variables: [] as Array<{
    id: string;
    workspace_id: string;
    project_id: string;
    environment_id: string;
    service_id: string;
    name: string;
    scope: "plain" | "secret" | "sealed";
    version: number;
    last_rotated_at: string;
    length_bucket:
      | "empty"
      | "1-16"
      | "17-64"
      | "65-256"
      | "257-1024"
      | "1024+";
    kek_fingerprint: string;
    value_kind: "literal" | "reference";
    references: [];
  }>,
  variablesPending: false,
  variablesError: null as Error | null,
  refetchVariables: vi.fn(),
}));

vi.mock("@/api/hooks", () => ({
  useService: () => state.service,
}));

vi.mock("@/components/panels/automation-tokens/api", () => ({
  useProjectAccess: (
    projectId: string,
    environmentId: string | undefined,
    enabled: boolean,
  ) => {
    state.accessCalls.push({ projectId, environmentId, enabled });
    return state.access;
  },
}));

vi.mock("./tenant-api", () => ({
  TENANT_VARIABLE_SCOPES: ["plain", "secret", "sealed"],
  TENANT_VARIABLE_NAME_MAX_BYTES: 255,
  isValidTenantVariableCreateName: (name: string) =>
    name.length > 0 &&
    new TextEncoder().encode(name).byteLength <= 255 &&
    /^[A-Za-z_][A-Za-z0-9_]*$/.test(name),
  tenantVariableKeys: {
    list: (target: {
      projectId: string;
      serviceId: string;
      environmentId: string;
    }) => [
      "tenant-service-environment-variables",
      "human",
      target.projectId,
      target.serviceId,
      target.environmentId,
    ],
  },
  useTenantVariables: (
    target: {
      projectId: string;
      serviceId: string;
      environmentId: string;
    },
    enabled: boolean,
  ) => {
    state.variableCalls.push({ target, enabled });
    return {
      data: state.variables,
      isPending: state.variablesPending,
      error: state.variablesError,
      refetch: state.refetchVariables,
    };
  },
}));

import { TenantVariablesEditor } from "./TenantVariablesEditor";

function metadata(
  overrides: Partial<(typeof state.variables)[number]> = {},
): (typeof state.variables)[number] {
  return {
    id: "var_1",
    workspace_id: "wrk_1",
    project_id: "prj_1",
    environment_id: "env_1",
    service_id: "svc_1",
    name: "API_TOKEN",
    scope: "secret",
    version: 4,
    last_rotated_at: "2026-07-29T12:00:00Z",
    length_bucket: "17-64",
    kek_fingerprint: "kek-test",
    value_kind: "literal",
    references: [],
    ...overrides,
  };
}

function renderEditor() {
  return render(
    <TenantVariablesEditor
      projectId="prj_1"
      serviceId="svc_1"
      environmentId="env_1"
    />,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  __resetStagedChangesForTests();
  state.service.data.id = "svc_1";
  state.service.data.project_id = "prj_1";
  state.service.isPending = false;
  state.service.error = null;
  state.access.data = {
    principal_kind: "human",
    workspace_id: "wrk_1",
    project_id: "prj_1",
    environment_id: "env_1",
    environment_restricted: false,
    permissions: ["variables.read", "variables.write"],
  };
  state.access.isPending = false;
  state.access.error = null;
  state.variables = [];
  state.variablesPending = false;
  state.variablesError = null;
  state.refetchVariables.mockResolvedValue({ data: state.variables });
});

describe("TenantVariablesEditor", () => {
  it("waits for exact service/environment access before enabling the metadata query", () => {
    state.access.isPending = true;

    renderEditor();

    expect(
      screen.getByText("Checking exact variable access…"),
    ).toBeInTheDocument();
    expect(state.accessCalls.at(-1)).toEqual({
      projectId: "prj_1",
      environmentId: "env_1",
      enabled: true,
    });
    expect(state.variableCalls.at(-1)).toMatchObject({ enabled: false });
  });

  it("fails closed on a service/project mismatch without exposing a variable request", () => {
    state.service.data.project_id = "prj_other";

    renderEditor();

    expect(
      screen.getByText(/could not verify that this service belongs/i),
    ).toBeInTheDocument();
    expect(state.accessCalls.at(-1)?.enabled).toBe(false);
    expect(state.variableCalls.at(-1)?.enabled).toBe(false);
  });

  it("fails closed when exact environment access is not echoed", () => {
    state.access.data.environment_id = "env_other";

    renderEditor();

    expect(
      screen.getByText(/could not verify the exact project and environment/i),
    ).toBeInTheDocument();
    expect(state.variableCalls.at(-1)?.enabled).toBe(false);
  });

  it("renders exact service-owned metadata read-only with variables.read", () => {
    state.access.data.permissions = ["variables.read"];
    state.variables = [metadata()];

    renderEditor();

    expect(screen.getByText("API_TOKEN")).toBeInTheDocument();
    expect(screen.getByText(/metadata v4/i)).toBeInTheDocument();
    expect(
      screen.getByText(/cannot add, replace, or remove/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /replace api_token/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /remove api_token/i }),
    ).not.toBeInTheDocument();
  });

  it("keeps a safe legacy hyphenated row available to replace or remove", async () => {
    const user = userEvent.setup();
    state.variables = [metadata({ name: "LEGACY-NAME" })];

    renderEditor();

    expect(screen.getByText("LEGACY-NAME")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Remove LEGACY-NAME" }),
    ).toBeEnabled();
    await user.click(
      screen.getByRole("button", { name: "Replace LEGACY-NAME" }),
    );
    expect(
      screen.getByLabelText("Replacement value for LEGACY-NAME"),
    ).toHaveAttribute("type", "password");
  });

  it("disables invalid or overlength names and accepts the 255-byte boundary", async () => {
    const user = userEvent.setup();

    renderEditor();
    await user.click(screen.getByRole("button", { name: "+ Add variable" }));
    const name = screen.getByLabelText("Variable name");
    const save = screen.getByRole("button", { name: "Save variable" });
    await user.type(screen.getByLabelText("Variable value"), "secret");

    await user.type(name, "LEGACY-NAME");
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(name).toHaveAccessibleDescription(/no more than 255 bytes/i);
    expect(save).toBeDisabled();

    await user.clear(name);
    fireEvent.change(name, { target: { value: "A".repeat(256) } });
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(name).toHaveAccessibleDescription(/no more than 255 bytes/i);
    expect(save).toBeDisabled();
    expect(getStagedForService("svc_1", "env_1")).toEqual([]);

    await user.clear(name);
    fireEvent.change(name, { target: { value: "A".repeat(255) } });
    expect(name).toHaveAttribute("aria-invalid", "false");
    expect(save).toBeEnabled();
  });

  it("masks and stages a new secret, then clears plaintext from the form", async () => {
    const user = userEvent.setup();

    renderEditor();
    await user.click(screen.getByRole("button", { name: "+ Add variable" }));
    await user.type(screen.getByLabelText("Variable name"), "NEW_TOKEN");
    const value = screen.getByLabelText("Variable value");
    expect(value).toHaveAttribute("type", "password");
    expect(value).toHaveAccessibleDescription(/complete replacement/i);
    await user.type(value, "first-secret");

    const save = screen.getByRole("button", { name: "Save variable" });
    await user.click(save);
    expect(
      await screen.findByText(
        "NEW_TOKEN is ready in Changes. Save the encrypted draft for review, then apply and deploy it.",
      ),
    ).toBeInTheDocument();
    expect(getStagedForService("svc_1", "env_1")).toEqual([
      {
        serviceId: "svc_1",
        environmentId: "env_1",
        name: "NEW_TOKEN",
        op: "add",
        scope: "secret",
        value: "first-secret",
      },
    ]);
    expect(screen.queryByLabelText("Variable value")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("first-secret");
  });

  it("stages a masked replacement with its exact metadata baseline", async () => {
    const user = userEvent.setup();
    state.variables = [metadata()];

    renderEditor();
    await user.click(
      screen.getByRole("button", { name: "Replace API_TOKEN" }),
    );
    const replacement = screen.getByLabelText(
      "Replacement value for API_TOKEN",
    );
    expect(replacement).toHaveAttribute("type", "password");
    await user.type(replacement, "keep-this-draft");
    await user.click(
      screen.getByRole("button", {
        name: "Save replacement for API_TOKEN",
      }),
    );

    expect(
      await screen.findByText(
        "API_TOKEN replacement is ready in Changes. Save the encrypted draft for review, then apply and deploy it.",
      ),
    ).toBeInTheDocument();
    expect(getStagedForService("svc_1", "env_1")).toEqual([
      expect.objectContaining({
        serviceId: "svc_1",
        environmentId: "env_1",
        name: "API_TOKEN",
        op: "edit",
        scope: "secret",
        value: "keep-this-draft",
        base: expect.objectContaining({
          id: "var_1",
          projectId: "prj_1",
          version: 4,
        }),
      }),
    ]);
    expect(screen.queryByLabelText("Replacement value for API_TOKEN")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("keep-this-draft");
  });

  it("keeps sealed replacements masked and prevents a privacy downgrade", async () => {
    const user = userEvent.setup();
    state.variables = [metadata({ scope: "sealed" })];

    renderEditor();
    await user.click(
      screen.getByRole("button", { name: "Replace API_TOKEN" }),
    );

    const scope = screen.getByLabelText("Value handling");
    expect(within(scope).getAllByRole("option")).toHaveLength(1);
    expect(within(scope).getByRole("option", { name: "Sealed" })).toBeVisible();
    expect(scope).toHaveAccessibleDescription(/cannot be downgraded/i);
    expect(
      screen.getByLabelText("Replacement value for API_TOKEN"),
    ).toHaveAttribute("type", "password");
  });

  it("drops plaintext drafts when the exact environment target changes", async () => {
    const user = userEvent.setup();
    const view = renderEditor();
    await user.click(screen.getByRole("button", { name: "+ Add variable" }));
    await user.type(screen.getByLabelText("Variable name"), "TEMP_TOKEN");
    await user.type(screen.getByLabelText("Variable value"), "scope-secret");

    state.access.data.environment_id = "env_2";
    view.rerender(
      <TenantVariablesEditor
        projectId="prj_1"
        serviceId="svc_1"
        environmentId="env_2"
      />,
    );

    expect(screen.queryByLabelText("Variable value")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("scope-secret");
    expect(state.variableCalls.at(-1)?.target.environmentId).toBe("env_2");
  });

  it("uses an explicit confirmation for removal and makes runtime behavior honest", async () => {
    const user = userEvent.setup();
    state.variables = [metadata()];

    renderEditor();
    await user.click(
      screen.getByRole("button", { name: "Remove API_TOKEN" }),
    );
    expect(
      screen.getByRole("alert", { name: "Remove API_TOKEN" }),
    ).toHaveTextContent(/existing containers are not changed/i);
    await user.click(
      screen.getByRole("button", { name: "Confirm remove API_TOKEN" }),
    );

    expect(getStagedForService("svc_1", "env_1")).toEqual([
      expect.objectContaining({
        serviceId: "svc_1",
        environmentId: "env_1",
        name: "API_TOKEN",
        op: "remove",
        scope: "secret",
        value: "",
        base: expect.objectContaining({ id: "var_1", version: 4 }),
      }),
    ]);
    expect(
      await screen.findByText(/removal is ready in Changes/i),
    ).toHaveTextContent(/apply and deploy it/i);
  });
});
