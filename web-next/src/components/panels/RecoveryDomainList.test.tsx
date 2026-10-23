import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  create: {
    mutate: vi.fn(),
    isPending: false,
    error: null as Error | null,
  },
  remove: {
    mutate: vi.fn(),
    error: null as Error | null,
  },
  query: {
    data: {
      generatedDomains: [] as Array<Record<string, unknown>>,
      customDomains: [] as Array<Record<string, unknown>>,
    },
    isPending: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
}));

vi.mock("./domains/recovery-api", () => ({
  useRecoveryServiceDomains: () => state.query,
  useCreateRecoveryDomain: () => state.create,
  useDeleteRecoveryDomain: () => state.remove,
  normalizeRecoveryDomainState: () => ({
    pillStatus: "active",
    label: "Trusted ingress ready",
    description:
      "Trusted ingress is ready; public DNS and browser reachability are separate.",
    serving: true,
    inFlight: false,
  }),
}));

import { RecoveryDomainList } from "./RecoveryDomainList";

const generated = {
  id: "dom_generated",
  project_id: "prj_1",
  environment_id: "env_prod",
  service_id: "svc_web",
  kind: "generated",
  hostname: "generated.apps.example.test",
  tls_desired: false,
  tls_state: "disabled",
  apply_state: "ready",
  reachable: true,
};

const custom = {
  ...generated,
  id: "dom_custom",
  kind: "custom",
  hostname: "app.example.test",
  dns_challenge: {
    record_name: "_midnight.app.example.test",
    record_value: "challenge-value",
    state: "pending",
  },
};

beforeEach(() => {
  vi.clearAllMocks();
  state.query.data = {
    generatedDomains: [generated],
    customDomains: [custom],
  };
  state.query.isPending = false;
  state.query.error = null;
  state.create.isPending = false;
  state.create.error = null;
  state.remove.error = null;
});

describe("RecoveryDomainList", () => {
  it("renders and mutates the explicit operator recovery lifecycle without public links", async () => {
    const user = userEvent.setup();
    render(
      <RecoveryDomainList
        projectId="prj_1"
        serviceId="svc_web"
        environmentId="env_prod"
      />,
    );

    expect(
      screen.getByText(/installation-wide recovery surface/i),
    ).toBeInTheDocument();
    expect(screen.getByText(generated.hostname)).toBeInTheDocument();
    expect(screen.getByText(custom.hostname)).toBeInTheDocument();
    expect(screen.getAllByText("Trusted ingress ready")).toHaveLength(2);
    expect(screen.queryByRole("link")).toBeNull();

    await user.click(
      screen.getByRole("button", { name: "Generate recovery domain" }),
    );
    expect(state.create.mutate).toHaveBeenCalledWith({
      projectId: "prj_1",
      environmentId: "env_prod",
      serviceId: "svc_web",
      kind: "generated",
      tls: false,
    });

    await user.type(
      screen.getByRole("textbox", {
        name: "Recovery custom domain hostname",
      }),
      "new.example.test",
    );
    await user.click(
      screen.getByRole("button", { name: "Add recovery domain" }),
    );
    expect(state.create.mutate).toHaveBeenLastCalledWith(
      {
        projectId: "prj_1",
        environmentId: "env_prod",
        serviceId: "svc_web",
        kind: "custom",
        hostname: "new.example.test",
        tls: false,
      },
      expect.objectContaining({ onSuccess: expect.any(Function) }),
    );

    await user.click(
      screen.getAllByRole("button", { name: "Remove" })[0]!,
    );
    expect(state.remove.mutate).toHaveBeenCalledWith(generated.id);
  });

  it("keeps custom TLS intent out of generated-domain creation", async () => {
    const user = userEvent.setup();
    render(
      <RecoveryDomainList
        projectId="prj_1"
        serviceId="svc_web"
        environmentId="env_prod"
      />,
    );

    await user.click(
      screen.getByRole("checkbox", {
        name: "Request HTTPS for the custom recovery domain",
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Generate recovery domain" }),
    );
    expect(state.create.mutate).toHaveBeenLastCalledWith({
      projectId: "prj_1",
      environmentId: "env_prod",
      serviceId: "svc_web",
      kind: "generated",
      tls: false,
    });

    await user.click(
      screen.getByRole("checkbox", {
        name: "Request HTTPS for the generated recovery domain",
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Generate recovery domain" }),
    );
    expect(state.create.mutate).toHaveBeenLastCalledWith({
      projectId: "prj_1",
      environmentId: "env_prod",
      serviceId: "svc_web",
      kind: "generated",
      tls: true,
    });

    await user.type(
      screen.getByRole("textbox", {
        name: "Recovery custom domain hostname",
      }),
      "tls.example.test",
    );
    await user.click(
      screen.getByRole("button", { name: "Add recovery domain" }),
    );
    expect(state.create.mutate).toHaveBeenLastCalledWith(
      {
        projectId: "prj_1",
        environmentId: "env_prod",
        serviceId: "svc_web",
        kind: "custom",
        hostname: "tls.example.test",
        tls: true,
      },
      expect.objectContaining({ onSuccess: expect.any(Function) }),
    );
  });
});
