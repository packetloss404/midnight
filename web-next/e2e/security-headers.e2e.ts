import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

const expectSecurityHeaders =
  process.env.MIDNIGHT_E2E_EXPECT_SECURITY_HEADERS === "1";

test.skip(
  !expectSecurityHeaders,
  "security headers are emitted by midnight-server, not the Vite development server",
);

const consoleContentSecurityPolicy = [
  "base-uri 'none'",
  "connect-src 'self'",
  "default-src 'self'",
  "font-src 'self' data:",
  "form-action 'self'",
  "frame-ancestors 'none'",
  "frame-src 'none'",
  "img-src 'self' data: blob:",
  "manifest-src 'self'",
  "media-src 'self' blob:",
  "object-src 'none'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "worker-src 'self' blob:",
].join("; ");

const expectedHeaders = {
  "content-security-policy": consoleContentSecurityPolicy,
  "permissions-policy":
    "accelerometer=(), autoplay=(), camera=(), clipboard-read=(self), clipboard-write=(self), display-capture=(), encrypted-media=(), fullscreen=(self), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(self), screen-wake-lock=(), usb=()",
  "referrer-policy": "no-referrer",
  "strict-transport-security": "max-age=31536000; includeSubDomains",
  "x-content-type-options": "nosniff",
  "x-frame-options": "DENY",
};

test("production console and API enforce the security-header contract", async ({
  page,
}) => {
  const consoleErrors: string[] = [];
  const pageErrors: string[] = [];
  let documentHeaders: Record<string, string> | undefined;

  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("response", async (response) => {
    if (response.request().resourceType() === "document") {
      documentHeaders = await response.allHeaders();
    }
  });
  await page.addInitScript(() => {
    const violations: string[] = [];
    Object.defineProperty(window, "__midnightCSPViolations", {
      configurable: false,
      value: violations,
    });
    document.addEventListener("securitypolicyviolation", (event) => {
      violations.push(
        `${event.effectiveDirective}:${event.blockedURI || "inline"}`,
      );
    });
  });

  await openDeterministicConsole(page);
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  expect(documentHeaders).toBeDefined();
  assertHeaders(documentHeaders!);

  const apiResponse = await page.request.get("/api/v1alpha/compat");
  expect(apiResponse.status()).toBe(200);
  assertHeaders(apiResponse.headers());

  const cspViolations = await page.evaluate(
    () =>
      (
        window as Window & { __midnightCSPViolations?: string[] }
      ).__midnightCSPViolations ?? [],
  );
  expect(cspViolations).toEqual([]);
  expect(consoleErrors).toEqual([]);
  expect(pageErrors).toEqual([]);
});

function assertHeaders(headers: Record<string, string>) {
  for (const [name, value] of Object.entries(expectedHeaders)) {
    expect(headers[name], name).toBe(value);
  }
}
