import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/client";

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  navigate: vi.fn().mockResolvedValue(undefined),
  reset: vi.fn(),
  recoveryToken: vi.fn(),
}));

vi.mock("@/api/client", async () => {
  const actual =
    await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    api: { resetAdministratorPassword: mocks.reset },
  };
});

vi.mock("@/api/credentials", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/credentials")>()),
  getOperatorRecoveryToken: mocks.recoveryToken,
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mocks.navigate,
  useRouterState: ({ select }: { select: (state: unknown) => unknown }) =>
    select({ location: { search: {} } }),
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    state: { status: "anonymous" },
    login: mocks.login,
    enterOperatorRecovery: vi.fn(),
  }),
}));

import { Login } from "./Login";

beforeEach(() => {
  mocks.login.mockReset();
  mocks.navigate.mockClear();
  mocks.reset.mockReset();
  mocks.recoveryToken.mockReset();
});

describe("Login", () => {
  it("shows the optional authenticator field before credentials are submitted", () => {
    render(<Login />);

    expect(
      screen.getByRole("textbox", { name: /^Authenticator code \(if enabled\)/ }),
    ).toBeInTheDocument();
    expect(screen.getByText(/otherwise leave this blank/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
  });

  it("submits a complete authenticator code with the first login request", async () => {
    const user = userEvent.setup();
    mocks.login.mockResolvedValue(undefined);
    render(<Login />);

    await user.type(screen.getByLabelText("Email"), "operator@example.com");
    await user.type(screen.getByLabelText("Password"), "correct horse battery staple");
    await user.type(
      screen.getByRole("textbox", { name: /^Authenticator code \(if enabled\)/ }),
      "123456",
    );
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() =>
      expect(mocks.login).toHaveBeenCalledWith({
        email: "operator@example.com",
        password: "correct horse battery staple",
        totp_code: "123456",
        client_kind: "dashboard",
      }),
    );
  });

  it("omits the authenticator property when the optional field is blank", async () => {
    const user = userEvent.setup();
    mocks.login.mockResolvedValue(undefined);
    render(<Login />);

    await user.type(screen.getByLabelText("Email"), "operator@example.com");
    await user.type(screen.getByLabelText("Password"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() =>
      expect(mocks.login).toHaveBeenCalledWith({
        email: "operator@example.com",
        password: "correct horse battery staple",
        client_kind: "dashboard",
      }),
    );
  });

  it("requires a complete code after the server returns MFA_REQUIRED", async () => {
    const user = userEvent.setup();
    mocks.login.mockRejectedValue(
      new ApiError(401, {
        error: {
          code: "MFA_REQUIRED",
          message: "multi-factor authentication is required",
        },
      }),
    );
    render(<Login />);

    await user.type(screen.getByLabelText("Email"), "operator@example.com");
    await user.type(screen.getByLabelText("Password"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(
      await screen.findByText(/MFA is enabled for this account/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Verify and sign in" }),
    ).toBeDisabled();

    await user.type(
      screen.getByRole("textbox", { name: /^Authenticator code \(if enabled\)/ }),
      "123456",
    );
    expect(
      screen.getByRole("button", { name: "Verify and sign in" }),
    ).toBeEnabled();
  });

  it("resets an account-specific MFA challenge when the email changes", async () => {
    const user = userEvent.setup();
    mocks.login.mockRejectedValue(
      new ApiError(401, {
        error: {
          code: "MFA_REQUIRED",
          message: "multi-factor authentication is required",
        },
      }),
    );
    render(<Login />);

    const email = screen.getByLabelText("Email");
    await user.type(email, "operator@example.com");
    await user.type(screen.getByLabelText("Password"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await screen.findByText(/MFA is enabled for this account/i);

    await user.clear(email);
    await user.type(email, "other@example.com");

    expect(screen.getByText(/otherwise leave this blank/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
  });

  it("offers administrator password recovery inside the emergency panel", () => {
    render(<Login />);

    const emergencyPanel = screen
      .getByText("Emergency recovery")
      .closest("details");
    const resetPanel = screen
      .getByText("Reset an administrator password")
      .closest("details");
    expect(emergencyPanel).not.toBeNull();
    expect(resetPanel).not.toBeNull();
    expect(emergencyPanel).toContainElement(resetPanel);
    expect(emergencyPanel).toContainElement(
      screen.getByLabelText("Administrator email"),
    );
    expect(
      screen.getByText(/signs out every active session for that account/i),
    ).toBeInTheDocument();
  });

  it("hands the recovery token typed in the panel to the reset request", async () => {
    const user = userEvent.setup();
    mocks.recoveryToken.mockReturnValue(undefined);
    mocks.reset.mockResolvedValue({
      user: {
        id: "usr_admin",
        email: "ops@example.com",
        display_name: "Ops",
        instance_admin: true,
        mfa_enabled: true,
      },
      totp_reenrolled: false,
      sessions_revoked: 1,
    });
    render(<Login />);

    // One token field serves both actions: an operator who cannot sign in
    // pastes it once and resets without first entering recovery mode.
    await user.type(
      screen.getByLabelText("Operator recovery token"),
      "operator-recovery-token",
    );
    await user.type(
      screen.getByLabelText("Administrator email"),
      "ops@example.com",
    );
    await user.type(
      screen.getByLabelText(/^New password/),
      "correct horse battery staple",
    );
    await user.type(
      screen.getByLabelText("Confirm new password"),
      "correct horse battery staple",
    );
    await user.click(screen.getByLabelText(/Keep the existing authenticator/));
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    await waitFor(() =>
      expect(mocks.reset).toHaveBeenCalledWith(
        {
          email: "ops@example.com",
          password: "correct horse battery staple",
          preserve_totp: true,
        },
        "operator-recovery-token",
      ),
    );
    expect(mocks.navigate).not.toHaveBeenCalled();
  });
});
