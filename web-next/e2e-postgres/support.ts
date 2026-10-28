import { expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { totpCode } from "../src/pages/SetupCrypto";

export type Human = { email: string; password: string; totpSecret?: string };
export const freshPassword = () => randomBytes(24).toString("base64url");

export async function login(page: Page, human: Human) {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill(human.email);
  await page.getByLabel("Password", { exact: true }).fill(human.password);
  if (human.totpSecret) await page.getByLabel(/^Authenticator code/).fill(await totpCode(human.totpSecret));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByLabel("Open account menu")).toBeVisible();
  const token = await page.evaluate(() => window.sessionStorage.getItem("midnight.human-session-token"));
  if (!token?.startsWith("midn_user_")) throw new Error("Human login did not establish a session");
  return token;
}

export async function seedExistingRecipients() {
  const state = JSON.parse(await readFile(process.env.MIDNIGHT_PG_E2E_STATE_PATH!, "utf8")) as { fixtureDsn: string };
  const users = ["recipient", "other"].map((name) => ({
    id: `usr_browser_${name}`, email: `browser-${name}@example.test`, password: freshPassword(),
  }));
  // This is explicit database setup, not account enrollment coverage. Credentials
  // travel over stdin, and neither the DSN nor passwords enter process arguments.
  await new Promise<void>((done, reject) => {
    const child = spawn("go", ["run", "./web-next/scripts/postgres-e2e-users.go"], {
      cwd: resolve(fileURLToPath(new URL("../..", import.meta.url))), stdio: ["pipe", "ignore", "pipe"],
    });
    child.on("error", reject);
    child.stderr.resume();
    child.on("close", (code) => code === 0 ? done() : reject(new Error("Disposable identity fixture failed")));
    child.stdin.end(JSON.stringify({ dsn: state.fixtureDsn, users }));
  });
  return users;
}

export async function authenticatePage(page: Page, token: string) {
  await page.addInitScript((value) => {
    window.sessionStorage.setItem("midnight.human-session-token", value);
    window.localStorage.setItem("midnight.agent.collapsed", "1");
  }, token);
}

export async function browserRequest(page: Page, token: string, path: string, method = "GET", body?: unknown, ifMatch?: string) {
  return page.evaluate(async ({ token, path, method, body, ifMatch }) => {
    const response = await fetch(path, {
      method,
      headers: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}), "Content-Type": "application/json",
        ...(method !== "GET" ? { "Idempotency-Key": crypto.randomUUID() } : {}),
        ...(ifMatch ? { "If-Match": ifMatch } : {}),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    const text = await response.text();
    return { status: response.status, text, body: text ? JSON.parse(text) : null };
  }, { token, path, method, body, ifMatch });
}
