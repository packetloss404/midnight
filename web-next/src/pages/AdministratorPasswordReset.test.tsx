import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { webcrypto } from "node:crypto";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/client";

// jsdom ships getRandomValues but not SubtleCrypto; enrollment needs both.
beforeAll(() => {
  if (!globalThis.crypto?.subtle) {
    Object.defineProperty(globalThis, "crypto", {
      configurable: true,
      value: webcrypto,
    });
  }
});

const mocks = vi.hoisted(() => ({
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

import { AdministratorPasswordReset } from "./AdministratorPasswordReset";
import { verifyTotpCode } from "./SetupCrypto";

const STRONG_PASSWORD = "correct horse battery staple";

function resetResult(overrides: Record<string, unknown> = {}) {
  return {
    user: {
      id: "usr_admin",
      email: "ops@example.com",
      display_name: "Ops",
      instance_admin: true,
      mfa_enabled: true,
    },
    totp_reenrolled: true,
    sessions_revoked: 2,
    ...overrides,
  };
}

async function fillAccount(
  user: ReturnType<typeof userEvent.setup>,
  { keepAuthenticator = false } = {},
) {
  await user.type(
    screen.getByLabelText("Administrator email"),
    "ops@example.com",
  );
  await user.type(screen.getByLabelText(/^New password/), STRONG_PASSWORD);
  await user.type(
    screen.getByLabelText("Confirm new password"),
    STRONG_PASSWORD,
  );
  if (keepAuthenticator) {
    await user.click(screen.getByLabelText(/Keep the existing authenticator/));
  }
}

beforeEach(() => {
  mocks.reset.mockReset();
  mocks.recoveryToken.mockReset();
  mocks.recoveryToken.mockReturnValue("midnight-operator-recovery-token");
});

describe("AdministratorPasswordReset", () => {
  it("enrolls a replacement authenticator and reports the blast radius", async () => {
    const user = userEvent.setup();
    mocks.reset.mockResolvedValue(resetResult());
    render(<AdministratorPasswordReset />);

    await fillAccount(user);
    await user.click(
      screen.getByRole("button", { name: "Continue to authenticator setup" }),
    );

    const secret = screen.getByText(/^[A-Z2-7]{32}$/).textContent ?? "";
    expect(secret).not.toHaveLength(0);
    expect(
      screen.getByText(new RegExp(`otpauth://totp/Midnight:ops%40example.com`)),
    ).toBeInTheDocument();

    // The console confirms the operator really enrolled the secret it just
    // generated, so type a code the secret actually produces.
    const code = await currentCodeFor(secret);
    await user.type(screen.getByLabelText("Authenticator code"), code);
    await user.click(
      screen.getByRole("button", { name: "Reset password and enroll" }),
    );

    await waitFor(() =>
      expect(mocks.reset).toHaveBeenCalledWith(
        {
          email: "ops@example.com",
          password: STRONG_PASSWORD,
          totp_secret: secret,
          totp_code: code,
        },
        "midnight-operator-recovery-token",
      ),
    );
    expect(
      await screen.findByText(/2 active sessions signed out/),
    ).toBeInTheDocument();
    // The secret stays readable exactly once, after the write succeeded.
    expect(screen.getByText(secret)).toBeInTheDocument();
    expect(screen.getByText(/is not shown again/)).toBeInTheDocument();
  });

  it("sends preserve_totp and never generates a secret when keeping the authenticator", async () => {
    const user = userEvent.setup();
    mocks.reset.mockResolvedValue(
      resetResult({ totp_reenrolled: false, sessions_revoked: 0 }),
    );
    render(<AdministratorPasswordReset />);

    await fillAccount(user, { keepAuthenticator: true });
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    await waitFor(() =>
      expect(mocks.reset).toHaveBeenCalledWith(
        {
          email: "ops@example.com",
          password: STRONG_PASSWORD,
          preserve_totp: true,
        },
        "midnight-operator-recovery-token",
      ),
    );
    expect(
      await screen.findByText(/no active sessions to sign out/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/existing authenticator is unchanged/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Enrollment URL/)).not.toBeInTheDocument();
  });

  it("uses the token typed into the panel when this tab has not stored one", async () => {
    const user = userEvent.setup();
    mocks.recoveryToken.mockReturnValue(undefined);
    mocks.reset.mockResolvedValue(
      resetResult({ totp_reenrolled: false, sessions_revoked: 1 }),
    );
    render(<AdministratorPasswordReset fallbackToken="  pasted-token  " />);

    await fillAccount(user, { keepAuthenticator: true });
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    await waitFor(() =>
      expect(mocks.reset).toHaveBeenCalledWith(
        expect.objectContaining({ preserve_totp: true }),
        "pasted-token",
      ),
    );
  });

  it("refuses to send anything without a recovery token", async () => {
    const user = userEvent.setup();
    mocks.recoveryToken.mockReturnValue(undefined);
    render(<AdministratorPasswordReset />);

    await fillAccount(user, { keepAuthenticator: true });
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    expect(
      await screen.findByText(/Paste your operator recovery token above first/),
    ).toBeInTheDocument();
    expect(mocks.reset).not.toHaveBeenCalled();
  });

  it("refuses a human session token, which the route would reject anyway", async () => {
    const user = userEvent.setup();
    mocks.recoveryToken.mockReturnValue(undefined);
    render(<AdministratorPasswordReset fallbackToken="midn_user_abc123" />);

    await fillAccount(user, { keepAuthenticator: true });
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    expect(
      await screen.findByText(/needs the installation's operator token/),
    ).toBeInTheDocument();
    expect(mocks.reset).not.toHaveBeenCalled();
  });

  it("validates the new password locally before spending a request", async () => {
    const user = userEvent.setup();
    render(<AdministratorPasswordReset />);

    await user.type(
      screen.getByLabelText("Administrator email"),
      "ops@example.com",
    );
    await user.type(screen.getByLabelText(/^New password/), STRONG_PASSWORD);
    await user.type(screen.getByLabelText("Confirm new password"), "different");
    await user.click(
      screen.getByRole("button", { name: "Continue to authenticator setup" }),
    );

    expect(
      await screen.findByText("The passwords do not match."),
    ).toBeInTheDocument();
    expect(mocks.reset).not.toHaveBeenCalled();
  });

  it("rejects a mistyped enrollment code before replacing the credential", async () => {
    const user = userEvent.setup();
    render(<AdministratorPasswordReset />);

    await fillAccount(user);
    await user.click(
      screen.getByRole("button", { name: "Continue to authenticator setup" }),
    );
    await user.type(screen.getByLabelText("Authenticator code"), "000000");
    await user.click(
      screen.getByRole("button", { name: "Reset password and enroll" }),
    );

    expect(
      await screen.findByText(/does not match the secret above/),
    ).toBeInTheDocument();
    expect(mocks.reset).not.toHaveBeenCalled();
  });

  it("explains the route's typed refusals instead of the generic status copy", async () => {
    const cases = [
      {
        keepAuthenticator: true,
        error: new ApiError(403, {
          error: {
            code: "AUTH_OPERATOR_RECOVERY_REQUIRED",
            message: "operator recovery credential required",
          },
        }),
        expected: /A signed-in account cannot reset an administrator credential/,
      },
      {
        keepAuthenticator: true,
        error: new ApiError(404, {
          error: {
            code: "RESOURCE_NOT_FOUND",
            message: "no enabled administrator account matches that address",
          },
        }),
        expected: /Disabled and deleted accounts cannot be recovered this way/,
      },
      {
        keepAuthenticator: true,
        error: new ApiError(400, {
          error: {
            code: "RESOURCE_VALIDATION_FAILED",
            message: "no TOTP enrollment to preserve",
          },
        }),
        expected: /no authenticator to keep/,
      },
      {
        keepAuthenticator: true,
        error: new ApiError(401, {
          error: { code: "AUTH_TOKEN_HEADER_INVALID", message: "bad token" },
        }),
        expected: /operator recovery token was not accepted/,
      },
    ];
    for (const testCase of cases) {
      mocks.reset.mockReset();
      mocks.reset.mockRejectedValue(testCase.error);
      const user = userEvent.setup();
      const view = render(<AdministratorPasswordReset />);

      await fillAccount(user, { keepAuthenticator: testCase.keepAuthenticator });
      await user.click(screen.getByRole("button", { name: "Reset password" }));

      expect(await screen.findByText(testCase.expected)).toBeInTheDocument();
      view.unmount();
    }
  }, 15_000);
});

/** Finds the code the generated secret currently produces. */
async function currentCodeFor(secret: string): Promise<string> {
  const { totpCode } = await import("./SetupCrypto");
  const code = await totpCode(secret);
  // Guard the helper itself: a wrong code would make the assertions vacuous.
  expect(await verifyTotpCode(secret, code)).toBe(true);
  return code;
}
