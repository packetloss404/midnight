import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { totpCode } from "../src/pages/SetupCrypto";
import { authenticatePage, browserRequest, freshPassword, login, seedExistingRecipients, type Human } from "./support";
import { registerInvitationOnboardingTests } from "./invitation-onboarding";
import { registerDiagnosisTests } from "./diagnosis";
import { registerTenantChatTests } from "./tenant-chat";

// One disposable database has one first-run claim. Keep dependent human flows
// in one serial suite, with credentials only in worker memory.
test.describe.configure({ mode: "serial" });
let administrator: Human & { token: string; projectId: string; serviceId: string; environmentId: string };

test("enrolls a real administrator, applies encrypted variables and runtime changes, enforces CAS, and revokes sessions", async ({ page, request }) => {
  const { operatorToken } = JSON.parse(await readFile(process.env.MIDNIGHT_PG_E2E_STATE_PATH!, "utf8")) as { operatorToken: string };
  const email = "browser-admin@example.test";
  const password = "browser acceptance correct horse battery staple";
  await page.addInitScript(() => {
    window.localStorage.setItem("midnight.agent.collapsed", "1");
    window.localStorage.setItem("midnight.agent.pinned", "0");
  });

  const initialErrors: string[] = [];
  const initialResponses: string[] = [];
  const recordInitialError = (error: Error) => initialErrors.push(error.message.slice(0, 500));
  const recordInitialResponse = (response: import("@playwright/test").Response) => {
    const path = new URL(response.url()).pathname;
    if (path.startsWith("/api/")) initialResponses.push(`${response.status()} ${path}`);
  };
  page.on("pageerror", recordInitialError);
  page.on("response", recordInitialResponse);
  await page.goto("/");
  try {
    await expect(page.getByRole("heading", { name: "Sign in", exact: true })).toBeVisible();
  } catch (error) {
    // Pre-credential boot diagnostics only: no headers, storage or screenshots.
    const redact = (value: string) => value.split(operatorToken).join("[redacted]").split(password).join("[redacted]");
    console.error(JSON.stringify({ initialPath: new URL(page.url()).pathname, initialErrors: initialErrors.map(redact), initialResponses, initialBody: redact((await page.locator("body").innerText()).slice(0, 2000)) }));
    throw error;
  } finally {
    page.off("pageerror", recordInitialError);
    page.off("response", recordInitialResponse);
  }
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
  expect(token?.startsWith("midn_user_")).toBe(true);
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
  expect(savedDraft.request().headers().authorization === `Bearer ${token}`).toBe(true);
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
  expect(nextToken !== token).toBe(true);
  expect((await request.get("/api/v1alpha/auth/me", { headers })).status()).toBe(401);
  administrator = { email, password, totpSecret: enrollmentSecret, token: nextToken!, projectId, serviceId, environmentId: environmentId! };
});

test("existing recipients accept invitations, enforce viewer/editor roles, and lose access after removal", async ({ page, browser }) => {
  test.setTimeout(180_000);
  const [recipient, other] = await seedExistingRecipients();
  await authenticatePage(page, administrator.token);
  await page.goto(`/projects/${administrator.projectId}/settings`);
  const credential = await createInvitation(page, recipient.email, "viewer");
  const adminAccess = await browserRequest(page, administrator.token, `/api/v1alpha/projects/${administrator.projectId}/access`);
  const invitationMetadata = await browserRequest(page, administrator.token,
    `/api/v1alpha/invitations?workspace_id=${adminAccess.body.workspace_id}&project_id=${administrator.projectId}`);
  expect(invitationMetadata.status).toBe(200);
  expect(invitationMetadata.text.includes(credential)).toBe(false);
  expect(invitationMetadata.text.includes('"invitation_token"')).toBe(false);
  const recipientContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
  const otherContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
  try {
    const otherPage = await otherContext.newPage();
    const otherToken = await login(otherPage, other);
    const mismatch = await browserRequest(otherPage, otherToken, "/api/v1alpha/invitations/accept", "POST", { invitation_token: credential });
    expect(mismatch.status).toBe(409);
    const recipientPage = await recipientContext.newPage();
    const recipientToken = await login(recipientPage, recipient);
    await expect(recipientPage.getByRole("heading", { name: "Your projects", exact: true })).toBeVisible();
    await expect(recipientPage.getByText("No projects are visible to this account.", { exact: true })).toBeVisible();
    await recipientPage.getByLabel(/^Invitation credential/).fill(credential);
    await recipientPage.getByRole("button", { name: "Accept invitation", exact: true }).click();
    await expect(recipientPage.getByText("Invitation accepted", { exact: true })).toBeVisible();
    await expect(recipientPage.getByRole("link", { name: /Durable Browser Project/ })).toBeVisible();
    expect((await browserRequest(recipientPage, recipientToken, "/api/v1alpha/invitations/accept", "POST", { invitation_token: credential })).status).toBe(409);

    const accessPath = `/api/v1alpha/projects/${administrator.projectId}/access`;
    const access = await browserRequest(recipientPage, recipientToken, accessPath);
    expect(access.status).toBe(200);
    expect(access.body.project_role).toBe("viewer");
    expect(access.body.permissions.includes("project.manage")).toBe(false);
    await recipientPage.goto(`/projects/${administrator.projectId}/settings`);
    await expect(recipientPage.getByText("Project manager access required", { exact: true })).toBeVisible();
    await expect(recipientPage.getByRole("button", { name: "+ New token", exact: true })).toHaveCount(0);
    await expect(recipientPage.getByRole("button", { name: "+ New invitation", exact: true })).toHaveCount(0);
    const configPath = `/api/v1alpha/services/${administrator.serviceId}/environments/${administrator.environmentId}/config`;
    const config = await browserRequest(recipientPage, recipientToken, configPath);
    expect(config.status).toBe(200);
    const mutation = { expected_version: config.body.version, target_port: 8090 };
    expect((await browserRequest(recipientPage, recipientToken, configPath, "PUT", mutation, `"${config.body.version}"`)).status).toBe(403);

    // Change the exact project membership through the real manager UI, then
    // re-evaluate the same already-issued session against current authority.
    await page.reload();
    const memberRow = page.locator("div").filter({ has: page.getByLabel(`Role for ${recipient.email}`, { exact: true }) }).last();
    await page.getByLabel(`Role for ${recipient.email}`, { exact: true }).selectOption("editor");
    await memberRow.getByRole("button", { name: "Save role", exact: true }).click();
    await expect.poll(async () => (await browserRequest(recipientPage, recipientToken, accessPath)).body.project_role).toBe("editor");
    expect((await browserRequest(recipientPage, recipientToken, configPath, "PUT", mutation, `"${config.body.version}"`)).status).toBe(200);
    await recipientPage.goto(`/projects/${administrator.projectId}/services/${administrator.serviceId}/configure?env=${administrator.environmentId}&configure=runtime`);
    await expect(recipientPage.getByLabel("Target port", { exact: true })).toHaveValue("8090");
    await memberRow.getByRole("button", { name: "Remove", exact: true }).click();
    await memberRow.getByRole("button", { name: "Confirm remove", exact: true }).click();
    await expect(page.getByLabel(`Role for ${recipient.email}`, { exact: true })).toHaveCount(0);
    expect((await browserRequest(recipientPage, recipientToken, `/api/v1alpha/projects/${administrator.projectId}`)).status).toBe(404);
    expect((await browserRequest(recipientPage, recipientToken, "/api/v1alpha/auth/me")).status).toBe(200);
    await recipientPage.goto("/");
    await expect(recipientPage.getByRole("link", { name: /Durable Browser Project/ })).toHaveCount(0);

    const oldCredential = await createInvitation(page, other.email, "viewer");
    const invitationRow = card(page, "Invitations").getByRole("listitem").filter({ hasText: other.email });
    await invitationRow.getByRole("button", { name: "Reissue", exact: true }).click();
    await page.getByRole("dialog", { name: "Reissue invitation credential?", exact: true }).getByRole("button", { name: "Reissue credential", exact: true }).click();
    const replacementDialog = page.getByRole("dialog", { name: "Copy reissued invitation credential", exact: true });
    const replacement = await replacementDialog.getByLabel("Invitation credential", { exact: true }).inputValue();
    await replacementDialog.getByRole("button", { name: "I have saved the credential", exact: true }).click();
    expect((await browserRequest(otherPage, otherToken, "/api/v1alpha/invitations/accept", "POST", { invitation_token: oldCredential })).status).toBe(409);
    const replacementRow = card(page, "Invitations").getByRole("listitem").filter({ hasText: other.email }).filter({ has: page.getByRole("button", { name: "Revoke", exact: true }) });
    await replacementRow.getByRole("button", { name: "Revoke", exact: true }).click();
    const revoked = page.waitForResponse((response) => response.request().method() === "DELETE" && new URL(response.url()).pathname.includes("/invitations/"));
    await page.getByRole("dialog", { name: "Revoke invitation?", exact: true }).getByRole("button", { name: "Revoke invitation", exact: true }).click();
    expect((await revoked).status()).toBe(204);
    expect((await browserRequest(otherPage, otherToken, "/api/v1alpha/invitations/accept", "POST", { invitation_token: replacement })).status).toBe(409);
  } finally {
    await recipientContext.close();
    await otherContext.close();
  }
});

test("project automation credentials admit only scoped CI work and stop working after browser revocation", async ({ page }) => {
  await authenticatePage(page, administrator.token);
  const other = await createProject(page, "Other Browser Project");
  await page.goto(`/projects/${administrator.projectId}/settings`);
  const original = await createAutomationToken(page, "Browser release credential");
  const path = `/api/v1alpha/projects/${administrator.projectId}/automation-tokens`;
  const metadata = await browserRequest(page, administrator.token, path);
  expect(metadata.status).toBe(200);
  expect(metadata.text.includes(original)).toBe(false);
  expect(metadata.text.includes('"access_token"')).toBe(false);
  expect((await browserRequest(page, original, `/api/v1alpha/projects/${other.projectId}`)).status).toBe(404);
  expect((await browserRequest(page, original, `/api/v1alpha/services/${administrator.serviceId}`, "DELETE")).status).toBe(403);
  const accepted = await browserRequest(page, original, `/api/v1alpha/services/${administrator.serviceId}/deployments`, "POST", {
    environment_id: administrator.environmentId,
    image: "registry.example.test/browser@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  });
  expect(accepted.status).toBe(202);
  expect((await browserRequest(page, original, `/api/v1alpha/operations/${accepted.body.id}`)).status).toBe(200);
  // Replacement is create-new + revoke-old; there is no rotate endpoint.
  const replacement = await createAutomationToken(page, "Browser replacement credential");
  const tokenRow = card(page, "Automation tokens").getByRole("listitem").filter({ hasText: "Browser release credential" });
  await tokenRow.getByRole("button", { name: "Revoke", exact: true }).click();
  const revoked = page.waitForResponse((response) => response.request().method() === "DELETE" && new URL(response.url()).pathname.includes("/automation-tokens/"));
  await page.getByRole("dialog", { name: "Revoke automation token?", exact: true }).getByRole("button", { name: "Revoke token", exact: true }).click();
  expect((await revoked).status()).toBe(204);
  expect((await browserRequest(page, original, `/api/v1alpha/operations/${accepted.body.id}`)).status).toBe(401);
  expect((await browserRequest(page, replacement, `/api/v1alpha/projects/${administrator.projectId}`)).status).toBe(200);
  await page.reload();
  await expect(tokenRow.getByText("revoked", { exact: true })).toBeVisible();
  await createAutomationToken(page, "Browser concurrent revocation");
  const current = await browserRequest(page, administrator.token, path);
  const concurrent = current.body.items.find((item: { name: string }) => item.name === "Browser concurrent revocation");
  await card(page, "Automation tokens").getByRole("listitem").filter({ hasText: concurrent.name }).getByRole("button", { name: "Revoke", exact: true }).click();
  // Another client changes the resource after the confirmation was opened.
  expect((await browserRequest(page, administrator.token, `${path}/${concurrent.id}`, "DELETE", undefined, `"${concurrent.resource_version}"`)).status).toBe(204);
  const staleRevoke = page.waitForResponse((response) => response.request().method() === "DELETE" && new URL(response.url()).pathname.endsWith(`/${concurrent.id}`));
  const staleDialog = page.getByRole("dialog", { name: "Revoke automation token?", exact: true });
  await staleDialog.getByRole("button", { name: "Revoke token", exact: true }).click();
  expect((await staleRevoke).status()).toBe(412);
  await expect(staleDialog).toBeVisible();
  await staleDialog.getByRole("button", { name: "Keep token", exact: true }).click();
  const deployments = await browserRequest(page, administrator.token, `/api/v1alpha/services/${administrator.serviceId}/deployments?environment_id=${administrator.environmentId}&limit=100`);
  expect(deployments.body.items).toHaveLength(1);
  // This harness has no worker: acceptance is durable queue admission, never
  // evidence of image pull, runtime secret delivery, or serving traffic.
  expect(deployments.body.items[0].status).toBe("QUEUED");
});

test("administrator recovery keeps MFA, revokes every previous session, and admits the replacement password", async ({ page, browser }) => {
  const { operatorToken } = JSON.parse(await readFile(process.env.MIDNIGHT_PG_E2E_STATE_PATH!, "utf8")) as { operatorToken: string };
  const context = await browser.newContext({ baseURL: "http://127.0.0.1:4183" });
  try {
    const secondPage = await context.newPage();
    const secondToken = await login(secondPage, administrator);
    await page.goto("/login");
    await page.getByText("Emergency recovery", { exact: true }).click();
    await page.getByLabel("Operator recovery token", { exact: true }).fill(operatorToken);
    await page.getByText("Reset an administrator password", { exact: true }).click();
    const password = freshPassword();
    await page.getByLabel("Administrator email", { exact: true }).fill(administrator.email);
    await page.getByLabel(/^New password/).fill(password);
    await page.getByLabel("Confirm new password", { exact: true }).fill(password);
    await page.getByLabel("Keep the existing authenticator", { exact: false }).check();
    const resetResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1alpha/auth/password-reset");
    await page.getByRole("button", { name: "Reset password", exact: true }).click();
    const reset = await resetResponse;
    expect(reset.status()).toBe(200);
    const result = await reset.json();
    expect(result.sessions_revoked).toBeGreaterThanOrEqual(2);
    expect(result.totp_reenrolled).toBe(false);
    expect((await browserRequest(page, administrator.token, "/api/v1alpha/auth/me")).status).toBe(401);
    expect((await browserRequest(secondPage, secondToken, "/api/v1alpha/auth/me")).status).toBe(401);
    const rejected = await browserRequest(page, "", "/api/v1alpha/auth/sessions", "POST", { email: administrator.email, password: administrator.password, totp_code: await totpCode(administrator.totpSecret!), client_kind: "web" });
    expect(rejected.status).toBe(401);
    await page.reload();
    await page.getByLabel("Email", { exact: true }).fill(administrator.email);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page.getByRole("button", { name: "Verify and sign in", exact: true })).toBeVisible();
    await page.getByLabel(/^Authenticator code/).fill(await totpCode(administrator.totpSecret!));
    await page.getByRole("button", { name: "Verify and sign in", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Projects", exact: true })).toBeVisible();
    administrator.password = password;
    administrator.token = (await page.evaluate(() => window.sessionStorage.getItem("midnight.human-session-token"))) ?? "";
  } finally { await context.close(); }
});

registerInvitationOnboardingTests({ administrator: () => administrator });
registerDiagnosisTests({ administrator: () => administrator });
registerTenantChatTests({ administrator: () => administrator });

function card(page: Page, title: string) {
  return page.locator("section").filter({ has: page.getByRole("heading", { name: title, exact: true }) }).last();
}

async function createInvitation(page: Page, email: string, role: string) {
  await card(page, "Invitations").getByRole("button", { name: "+ New invitation", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "New project invitation", exact: true });
  await dialog.getByLabel("Recipient email", { exact: true }).fill(email);
  await dialog.getByRole("combobox", { name: /^Project role/ }).selectOption(role);
  const created = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1alpha/invitations" && response.request().method() === "POST");
  await dialog.getByRole("button", { name: "Create invitation", exact: true }).click();
  expect((await created).headers()["cache-control"].split(",").map((directive) => directive.trim())).toContain("no-store");
  const revealed = page.getByRole("dialog", { name: "Copy created invitation credential", exact: true });
  const credential = await revealed.getByLabel("Invitation credential", { exact: true }).inputValue();
  expect(credential.startsWith("midn_inv_")).toBe(true);
  await revealed.getByRole("button", { name: "I have saved the credential", exact: true }).click();
  return credential;
}

async function createAutomationToken(page: Page, name: string) {
  await card(page, "Automation tokens").getByRole("button", { name: "+ New token", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "New automation token", exact: true });
  await dialog.getByLabel("Token name", { exact: true }).fill(name);
  const created = page.waitForResponse((response) => new URL(response.url()).pathname.endsWith("/automation-tokens") && response.request().method() === "POST");
  await dialog.getByRole("button", { name: "Create token", exact: true }).click();
  expect((await created).headers()["cache-control"].split(",").map((directive) => directive.trim())).toContain("no-store");
  const revealed = page.getByRole("dialog", { name: "Copy automation token", exact: true });
  const token = await revealed.getByLabel("Access token", { exact: true }).inputValue();
  expect(token.startsWith("midn_sa_")).toBe(true);
  await revealed.getByRole("button", { name: "I have saved this token", exact: true }).click();
  return token;
}

async function createProject(page: Page, name: string) {
  await page.goto("/new");
  await page.getByRole("button", { name: "Empty project", exact: true }).click();
  await page.getByPlaceholder("my-app").fill(name);
  await page.getByLabel("Also create a starter service").check();
  await page.getByPlaceholder("api").fill("Other Browser Service");
  await page.getByRole("button", { name: "Create project", exact: true }).click();
  await expect(page).toHaveURL(/\/services\/[^/]+\/overview/);
  return { projectId: new URL(page.url()).pathname.split("/")[2] };
}
