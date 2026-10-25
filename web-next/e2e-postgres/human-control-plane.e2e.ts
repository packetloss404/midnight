import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { totpCode } from "../src/pages/SetupCrypto";

test("enrolls a real administrator, applies encrypted variables and runtime changes, enforces CAS, and revokes sessions", async ({ page, request }) => {
  const { operatorToken } = JSON.parse(await readFile(process.env.MIDNIGHT_PG_E2E_STATE_PATH!, "utf8")) as { operatorToken: string };
  const email = "browser-admin@example.test";
  const password = "browser acceptance correct horse battery staple";
  await page.addInitScript(() => {
    window.localStorage.setItem("midnight.agent.collapsed", "1");
    window.localStorage.setItem("midnight.agent.pinned", "0");
  });

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Sign in", exact: true })).toBeVisible();
  await page.getByText("Emergency recovery", { exact: true }).click();
  await page.getByLabel("Operator recovery token").fill(operatorToken);
  await page.getByRole("button", { name: "Verify and enter recovery mode" }).click();
  await expect(page).toHaveURL(/\/setup$/);
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Your name", { exact: true }).fill("Browser Administrator");
  await page.getByLabel("Workspace name", { exact: true }).fill("Browser Workspace");
  await page.locator("#setup-password").fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Continue to authenticator setup" }).click();
  const enrollmentSecret = (await page.locator("code").filter({ hasText: /^[A-Z2-7]{32}$/ }).textContent())!.trim();
  await page.getByLabel("Authenticator code", { exact: true }).fill(await totpCode(enrollmentSecret));
  await page.getByRole("button", { name: "Create administrator account", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Projects", exact: true })).toBeVisible();

  // Subsequent product work must use the human session, without recovery fallback.
  await page.evaluate(() => window.sessionStorage.removeItem("midnight.api-token"));
  await page.reload();
  const token = await page.evaluate(() => window.sessionStorage.getItem("midnight.human-session-token"));
  expect(token).toMatch(/^midn_user_/);
  const headers = { Authorization: `Bearer ${token}` };
  const me = await request.get("/api/v1alpha/auth/me", { headers });
  expect(me.status()).toBe(200);
  expect((await me.json()).principal.instance_admin).toBe(true);

  await page.goto("/new");
  await page.getByRole("button", { name: "Empty project", exact: true }).click();
  await page.getByPlaceholder("my-app").fill("Durable Browser Project");
  await page.getByLabel("Also create a starter service").check();
  await page.getByPlaceholder("api").fill("Browser Service");
  await page.getByRole("button", { name: "Create project", exact: true }).click();
  await expect(page).toHaveURL(/\/services\/[^/]+\/overview/);
  const destination = new URL(page.url());
  const projectId = destination.pathname.split("/")[2];
  const serviceId = destination.pathname.split("/")[4];
  const environmentId = destination.searchParams.get("env");
  expect(environmentId).toBeTruthy();
  const servicePath = `/api/v1alpha/services/${serviceId}/environments/${environmentId}`;
  const changesetPath = `/api/v1alpha/environments/${environmentId}/changeset`;
  await page.goto(`/projects/${projectId}/services/${serviceId}/configure?env=${environmentId}&configure=source`);
  await expect(page.getByText("No GitHub source is linked", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Variables", exact: true }).click();
  await page.getByRole("button", { name: "+ Add variable", exact: true }).click();
  const addVariable = page.getByRole("form", { name: "Add service variable" });
  await addVariable.getByLabel("Variable name", { exact: true }).fill("BROWSER_SECRET");
  const secretValue = "disposable-browser-secret-never-returned";
  await addVariable.getByLabel("Variable value", { exact: true }).fill(secretValue);
  await addVariable.getByRole("button", { name: "Save variable", exact: true }).click();
  await page.getByRole("tab", { name: "Runtime", exact: true }).click();
  await page.getByLabel("Target port", { exact: true }).fill("8088");
  await page.getByRole("button", { name: "Save desired config", exact: true }).click();
  const changes = page.getByRole("region", { name: "Changes", exact: true });
  const draftResponse = page.waitForResponse((response) => new URL(response.url()).pathname === changesetPath && response.request().method() === "PUT");
  await changes.getByRole("button", { name: "Save for review", exact: true }).click();
  const savedDraft = await draftResponse;
  expect(savedDraft.ok()).toBe(true);
  expect(savedDraft.request().headers().authorization).toBe(`Bearer ${token}`);
  expect(savedDraft.request().headers()["if-match"]).toBe('"0"');
  const draft = await savedDraft.json();
  expect(draft.changeset.items.map((item: { kind: string }) => item.kind).sort()).toEqual(["runtime", "variable"]);
  expect(JSON.stringify(draft)).not.toContain(secretValue);
  await page.reload();
  await expect(changes.getByText(/2 saved · revision/)).toBeVisible();
  await changes.getByLabel("Advanced change actions").click();
  const appliedResponse = page.waitForResponse((response) => new URL(response.url()).pathname === `${changesetPath}/apply` && response.request().method() === "POST");
  await changes.getByRole("button", { name: "Apply without deploying", exact: true }).click();
  const applied = await appliedResponse;
  expect(applied.ok()).toBe(true);
  expect((await applied.json()).changeset.state).toBe("applied");

  const configResponse = await request.get(`${servicePath}/config`, { headers });
  expect(configResponse.status()).toBe(200);
  const config = await configResponse.json();
  expect(config.version).toBe(1);
  expect(config.target_port).toBe(8088);
  const metadataResponse = await request.get(`${servicePath}/variables`, { headers });
  expect(metadataResponse.status()).toBe(200);
  const metadataText = await metadataResponse.text();
  expect(metadataText).not.toContain(secretValue);
  const metadata = JSON.parse(metadataText);
  const variable = (Array.isArray(metadata) ? metadata : metadata.items).find((item: { name: string }) => item.name === "BROWSER_SECRET");
  expect(variable.version).toBe(1);
  expect(variable.scope).toBe("secret");

  // These requests run in Chromium against the same API and human authority.
  // A stale writer must receive 412 and leave the accepted configuration intact.
  const staleConfig = await page.evaluate(async ({ path, token }) => {
    const response = await fetch(path, {
      method: "PUT", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID(), "If-Match": '"0"' },
      body: JSON.stringify({ expected_version: 0, target_port: 9999 }),
    });
    return { status: response.status, body: await response.json() };
  }, { path: `${servicePath}/config`, token });
  expect(staleConfig.status).toBe(412);

  await page.getByRole("tab", { name: "Variables", exact: true }).click();
  await page.getByRole("button", { name: "Replace BROWSER_SECRET", exact: true }).click();
  await page.getByLabel("Replacement value for BROWSER_SECRET", { exact: true }).fill("rotated-disposable-browser-secret");
  await page.getByRole("button", { name: "Save replacement for BROWSER_SECRET", exact: true }).click();
  await changes.getByRole("button", { name: "Save for review", exact: true }).click();
  await expect(changes.getByRole("button", { name: "Apply & deploy", exact: true })).toBeEnabled();
  await changes.getByLabel("Advanced change actions").click();
  const rotatedResponse = page.waitForResponse((response) => new URL(response.url()).pathname === `${changesetPath}/apply` && response.request().method() === "POST");
  await changes.getByRole("button", { name: "Apply without deploying", exact: true }).click();
  expect((await rotatedResponse).ok()).toBe(true);
  await expect(page.getByText(/metadata v2/)).toBeVisible();
  const staleVariable = await page.evaluate(async ({ path, token }) => {
    const response = await fetch(path, {
      method: "PATCH", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID(), "If-Match": '"1"' },
      body: JSON.stringify({ expected_version: 1, value: "must-not-overwrite-rotated-value" }),
    });
    return response.status;
  }, { path: `${servicePath}/variables/${variable.id}`, token });
  expect(staleVariable).toBe(412);
  const currentVariables = await request.get(`${servicePath}/variables`, { headers });
  const currentMetadata = await currentVariables.json();
  expect((Array.isArray(currentMetadata) ? currentMetadata : currentMetadata.items).find((item: { id: string }) => item.id === variable.id).version).toBe(2);
  const currentConfig = await request.get(`${servicePath}/config`, { headers });
  expect((await currentConfig.json()).target_port).toBe(8088);
  const deployments = await request.get(`/api/v1alpha/services/${serviceId}/deployments?environment_id=${environmentId}&limit=100`, { headers });
  expect((await deployments.json()).items).toHaveLength(0);

  await page.getByLabel("Open account menu").click();
  await page.getByRole("button", { name: "Sign out of this session", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Sign in", exact: true })).toBeVisible();
  expect((await request.get("/api/v1alpha/auth/me", { headers })).status()).toBe(401);
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel(/^Authenticator code/).fill(await totpCode(enrollmentSecret));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Projects", exact: true })).toBeVisible();
  const nextToken = await page.evaluate(() => window.sessionStorage.getItem("midnight.human-session-token"));
  expect(nextToken).not.toBe(token);
  expect((await request.get("/api/v1alpha/auth/me", { headers })).status()).toBe(401);
});
