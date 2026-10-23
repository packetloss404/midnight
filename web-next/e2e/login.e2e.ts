import { expect, test } from "@playwright/test";
import { totpCode } from "../src/pages/SetupCrypto";
import { fulfillDeterministicApi } from "./support/app";

test("guards the console, signs in as a human, and scopes product auth", async ({
  page,
}) => {
  let authenticated = false;
  let loginAuthorization: string | undefined;
  let projectAuthorization: string | undefined;
  const authenticatedPaths: string[] = [];

  await page.route("**/api/v1alpha/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;

    if (
      pathname === "/api/v1alpha/projects" &&
      request.method() === "GET" &&
      !authenticated
    ) {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
        }),
      });
      return;
    }

    if (
      pathname === "/api/v1alpha/auth/sessions" &&
      request.method() === "POST"
    ) {
      loginAuthorization = request.headers().authorization;
      authenticated = true;
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          access_token: "midn_user_member_e2e",
          token_type: "Bearer",
          expires_in: 86400,
          user: {
            id: "usr_browser",
            email: "person@example.com",
            display_name: "Person",
            instance_admin: false,
            mfa_enabled: false,
          },
          session: {
            id: "ses_browser",
            user_id: "usr_browser",
            token_prefix: "midn_user_b",
            client_kind: "dashboard",
            created_at: "2026-01-01T00:00:00Z",
            last_seen_at: "2026-01-01T00:00:00Z",
            expires_at: "2027-01-02T00:00:00Z",
            idle_expires_at: "2027-01-01T08:00:00Z",
            current: true,
          },
        }),
      });
      return;
    }

    if (pathname === "/api/v1alpha/projects" && request.method() === "GET") {
      projectAuthorization = request.headers().authorization;
    }
    if (authenticated) authenticatedPaths.push(pathname);
    await fulfillDeterministicApi(route);
  });

  await page.goto("/");
  await expect(page).toHaveURL(/\/login\?redirect=/);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.getByLabel("Email", { exact: true }).fill("person@example.com");
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page).toHaveURL("/");
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  expect(loginAuthorization).toBeUndefined();
  expect(projectAuthorization).toBe("Bearer midn_user_member_e2e");
  await expect(page.getByTestId("agent-dock-fab")).toHaveCount(0);

  await page.getByRole("link", { name: /Gate Zero/ }).click();
  await expect(page.getByRole("heading", { name: "Gate Zero" })).toBeVisible();
  expect(authenticatedPaths).toContain(
    "/api/v1alpha/environments/env_production/resources",
  );
  expect(
    authenticatedPaths.some((path) => path.includes("/deployments")),
  ).toBe(false);
  expect(
    authenticatedPaths.some((path) => path.includes("/observability")),
  ).toBe(false);
});

test("stores operator recovery access only after a privileged probe succeeds", async ({
  page,
}) => {
  await page.route("**/api/v1alpha/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const authorization = request.headers().authorization;

    if (
      pathname === "/api/v1alpha/projects" &&
      request.method() === "GET" &&
      !authorization
    ) {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
        }),
      });
      return;
    }

    if (pathname === "/api/v1alpha/dashboard/summary") {
      if (authorization === "Bearer verified-operator") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ status: "ready" }),
        });
      } else {
        await route.fulfill({
          status: 401,
          contentType: "application/json",
          body: JSON.stringify({
            error: {
              code: "AUTH_TOKEN_HEADER_INVALID",
              message: "Invalid recovery token",
            },
          }),
        });
      }
      return;
    }

    await fulfillDeterministicApi(route);
  });

  await page.goto("/login");
  await page.getByText("Emergency recovery", { exact: true }).click();
  const token = page.getByLabel("Operator recovery token");
  await token.fill("not-valid");
  await page
    .getByRole("button", { name: "Verify and enter recovery mode" })
    .click();
  await expect(page.getByRole("alert")).toHaveText("Authentication is required.");
  await expect
    .poll(() =>
      page.evaluate(() => window.sessionStorage.getItem("midnight.api-token")),
    )
    .toBeNull();

  await token.fill("verified-operator");
  await page
    .getByRole("button", { name: "Verify and enter recovery mode" })
    .click();
  await expect(page).toHaveURL("/");
  await expect
    .poll(() =>
      page.evaluate(() => window.sessionStorage.getItem("midnight.api-token")),
    )
    .toBe("verified-operator");
});

test("resets a locked-out administrator from the emergency recovery panel", async ({
  page,
}) => {
  let resetAuthorization: string | undefined;
  let resetIdempotencyKey: string | undefined;
  let resetBody: Record<string, unknown> | undefined;

  await page.route("**/api/v1alpha/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;

    if (
      pathname === "/api/v1alpha/auth/password-reset" &&
      request.method() === "POST"
    ) {
      resetAuthorization = request.headers().authorization;
      resetIdempotencyKey = request.headers()["idempotency-key"];
      resetBody = request.postDataJSON();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          user: {
            id: "usr_admin",
            email: "ops@example.com",
            display_name: "Ops",
            instance_admin: true,
            mfa_enabled: true,
          },
          totp_reenrolled: true,
          sessions_revoked: 3,
        }),
      });
      return;
    }

    if (pathname === "/api/v1alpha/projects" && request.method() === "GET") {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
        }),
      });
      return;
    }

    await fulfillDeterministicApi(route);
  });

  await page.goto("/login");
  await page.getByText("Emergency recovery", { exact: true }).click();
  // The operator cannot sign in, so the token typed here is the only
  // credential available; the reset must use it without a round trip through
  // recovery mode.
  await page
    .getByLabel("Operator recovery token")
    .fill("operator-recovery-token");
  await page
    .getByText("Reset an administrator password", { exact: true })
    .click();

  await page.getByLabel("Administrator email").fill("ops@example.com");
  await page.getByLabel(/^New password/).fill("correct horse battery staple");
  await page
    .getByLabel("Confirm new password")
    .fill("correct horse battery staple");
  await page
    .getByRole("button", { name: "Continue to authenticator setup" })
    .click();

  const secret = (
    await page.getByTestId("reset-totp-secret").textContent()
  )?.trim();
  expect(secret).toMatch(/^[A-Z2-7]{32}$/);
  await expect(
    page.getByText(`otpauth://totp/Midnight:ops%40example.com`, {
      exact: false,
    }),
  ).toBeVisible();

  // Derive the authenticator code in the Playwright worker. Production serves
  // only bundled assets, so a browser-side import of a source path is invalid.
  const code = await totpCode(secret);
  await page.getByLabel("Authenticator code", { exact: true }).fill(code);
  await page
    .getByRole("button", { name: "Reset password and enroll" })
    .click();

  await expect(
    page.getByText("Password reset for ops@example.com. 3 active sessions signed out."),
  ).toBeVisible();
  expect(resetAuthorization).toBe("Bearer operator-recovery-token");
  expect(resetIdempotencyKey).toBeTruthy();
  expect(resetBody).toEqual({
    email: "ops@example.com",
    password: "correct horse battery staple",
    totp_secret: secret,
    totp_code: code,
  });
  // The verified secret stays readable exactly once, after the write landed.
  await expect(page.getByTestId("reset-totp-secret")).toHaveText(secret!);
  await expect(page.getByText(/is not shown again/)).toBeVisible();
});

test("accepts MFA up front and honors challenge and Retry-After state", async ({
  page,
}) => {
  let loginAttempts = 0;
  await page.route("**/api/v1alpha/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;

    if (pathname === "/api/v1alpha/projects") {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
        }),
      });
      return;
    }

    if (
      pathname === "/api/v1alpha/auth/sessions" &&
      request.method() === "POST"
    ) {
      loginAttempts += 1;
      if (loginAttempts === 1) {
        await route.fulfill({
          status: 401,
          contentType: "application/json",
          body: JSON.stringify({
            error: {
              code: "MFA_REQUIRED",
              message: "Multi-factor authentication is required",
            },
          }),
        });
        return;
      }
      await route.fulfill({
        status: 429,
        headers: { "Retry-After": "2" },
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "AUTH_RATE_LIMITED",
            message: "Login attempts are temporarily rate limited",
          },
        }),
      });
      return;
    }

    await fulfillDeterministicApi(route);
  });

  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("person@example.com");
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery staple");
  const totp = page.getByLabel(/^Authenticator code \(if enabled\)/);
  await expect(totp).toBeVisible();
  await expect(page.getByText(/otherwise leave this blank/i)).toBeVisible();
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByText(/MFA is enabled for this account/i)).toBeVisible();
  await totp.fill("123456");
  await page.getByRole("button", { name: "Verify and sign in" }).click();

  const throttled = page.getByRole("button", { name: "Try again in 2s" });
  await expect(throttled).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Verify and sign in" }),
  ).toBeEnabled({ timeout: 4_000 });
});
