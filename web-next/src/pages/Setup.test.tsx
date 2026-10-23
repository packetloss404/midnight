import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { webcrypto } from "node:crypto";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/client";

// jsdom ships getRandomValues but not SubtleCrypto; the setup flow needs both.
beforeAll(() => {
  if (!globalThis.crypto?.subtle) {
    Object.defineProperty(globalThis, "crypto", {
      configurable: true,
      value: webcrypto,
    });
  }
});

const mocks = vi.hoisted(() => ({
  getClaimStatus: vi.fn(),
  claim: vi.fn(),
  login: vi.fn(),
  recoveryToken: vi.fn(),
}));

vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>(
    "@/api/client",
  );
  return {
    ...actual,
    api: {
      getIdentityClaimStatus: mocks.getClaimStatus,
      claimDefaultWorkspace: mocks.claim,
    },
  };
});

vi.mock("@/api/credentials", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/credentials")>()),
  getOperatorRecoveryToken: mocks.recoveryToken,
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({ state: { status: "operator" }, login: mocks.login }),
}));

import { Setup } from "./Setup";
import { totpCode } from "./SetupCrypto";

const PASSWORD = "correct horse battery staple";

beforeEach(() => {
  mocks.getClaimStatus.mockReset().mockResolvedValue({ claimed: false });
  mocks.claim.mockReset().mockResolvedValue({
    user: { id: "usr_abc" },
    workspace: { id: "wrk_default" },
  });
  mocks.login.mockReset().mockResolvedValue(undefined);
  mocks.recoveryToken.mockReset().mockReturnValue("midn_admin_recovery");
});

async function fillAccountForm(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByLabelText("Email");
  await user.type(screen.getByLabelText("Email"), "owner@example.com");
  await user.type(screen.getByLabelText("Your name"), "Owner");
  await user.type(screen.getByLabelText("Workspace name"), "Acme Platform");
  await user.type(screen.getByLabelText(/^Password/), PASSWORD);
  await user.type(screen.getByLabelText("Confirm password"), PASSWORD);
}

describe("Setup", () => {
  it("tells the operator to paste a recovery token first when none is stored", async () => {
    mocks.recoveryToken.mockReturnValue(null);
    render(<Setup />);

    expect(
      await screen.findByText(/Paste your operator recovery token/i),
    ).toBeInTheDocument();
    expect(mocks.getClaimStatus).not.toHaveBeenCalled();
  });

  it("refuses to run again once the installation is claimed", async () => {
    mocks.getClaimStatus.mockResolvedValue({ claimed: true });
    render(<Setup />);

    expect(
      await screen.findByText(/already has an administrator/i),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Email")).not.toBeInTheDocument();
  });

  it("derives a workspace slug from the workspace name", async () => {
    const user = userEvent.setup();
    render(<Setup />);
    await screen.findByLabelText("Workspace name");

    await user.type(screen.getByLabelText("Workspace name"), "Acme Platform!");

    expect(screen.getByLabelText(/^Workspace slug/)).toHaveValue(
      "acme-platform",
    );
  });

  it("rejects a short password before generating an enrollment secret", async () => {
    const user = userEvent.setup();
    render(<Setup />);
    await screen.findByLabelText("Email");

    await user.type(screen.getByLabelText("Email"), "owner@example.com");
    await user.type(screen.getByLabelText("Your name"), "Owner");
    await user.type(screen.getByLabelText("Workspace name"), "Acme Platform");
    await user.type(screen.getByLabelText(/^Password/), "short");
    await user.type(screen.getByLabelText("Confirm password"), "short");
    await user.click(
      screen.getByRole("button", { name: /Continue to authenticator/i }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /at least 12 characters/i,
    );
    expect(mocks.claim).not.toHaveBeenCalled();
  });

  it("rejects mismatched password confirmation", async () => {
    const user = userEvent.setup();
    render(<Setup />);
    await screen.findByLabelText("Email");

    await user.type(screen.getByLabelText("Email"), "owner@example.com");
    await user.type(screen.getByLabelText("Your name"), "Owner");
    await user.type(screen.getByLabelText("Workspace name"), "Acme Platform");
    await user.type(screen.getByLabelText(/^Password/), PASSWORD);
    await user.type(screen.getByLabelText("Confirm password"), "something else");
    await user.click(
      screen.getByRole("button", { name: /Continue to authenticator/i }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /passwords do not match/i,
    );
    expect(mocks.claim).not.toHaveBeenCalled();
  });

  it("shows the enrollment secret and rejects a wrong confirmation code", async () => {
    const user = userEvent.setup();
    render(<Setup />);

    await fillAccountForm(user);
    await user.click(
      screen.getByRole("button", { name: /Continue to authenticator/i }),
    );

    expect(await screen.findByText(/otpauth:\/\/totp\//)).toBeInTheDocument();
    expect(screen.getByText(/not shown\s+again/i)).toBeInTheDocument();

    await user.type(screen.getByLabelText("Authenticator code"), "000000");
    await user.click(
      screen.getByRole("button", { name: /Create administrator account/i }),
    );

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        /does not match the secret/i,
      ),
    );
    expect(mocks.claim).not.toHaveBeenCalled();
  });

  it("claims the workspace with the recovery token and signs the new user in", async () => {
    const user = userEvent.setup();
    const onComplete = vi.fn();
    render(<Setup onComplete={onComplete} />);

    await fillAccountForm(user);
    await user.click(
      screen.getByRole("button", { name: /Continue to authenticator/i }),
    );

    const secret = (await screen.findByText(/^[A-Z2-7]{32}$/)).textContent ?? "";
    const code = await totpCode(secret);
    await user.type(screen.getByLabelText("Authenticator code"), code);
    await user.click(
      screen.getByRole("button", { name: /Create administrator account/i }),
    );

    await waitFor(() => expect(mocks.claim).toHaveBeenCalledTimes(1));
    expect(mocks.claim).toHaveBeenCalledWith(
      {
        email: "owner@example.com",
        display_name: "Owner",
        password: PASSWORD,
        totp_secret: secret,
        totp_code: code,
        workspace_slug: "acme-platform",
        workspace_display_name: "Acme Platform",
      },
      "midn_admin_recovery",
    );
    await waitFor(() =>
      expect(mocks.login).toHaveBeenCalledWith({
        email: "owner@example.com",
        password: PASSWORD,
        totp_code: code,
        client_kind: "dashboard",
      }),
    );
    await waitFor(() => expect(onComplete).toHaveBeenCalled());
  });

  it("switches to the already-claimed state when the API returns a conflict", async () => {
    const user = userEvent.setup();
    mocks.claim.mockRejectedValue(
      new ApiError(409, {
        error: { code: "RESOURCE_CONFLICT", message: "already claimed" },
      }),
    );
    render(<Setup />);

    await fillAccountForm(user);
    await user.click(
      screen.getByRole("button", { name: /Continue to authenticator/i }),
    );
    const secret = (await screen.findByText(/^[A-Z2-7]{32}$/)).textContent ?? "";
    await user.type(
      screen.getByLabelText("Authenticator code"),
      await totpCode(secret),
    );
    await user.click(
      screen.getByRole("button", { name: /Create administrator account/i }),
    );

    expect(
      await screen.findByText(/already has an administrator/i),
    ).toBeInTheDocument();
    expect(mocks.login).not.toHaveBeenCalled();
  });
});
