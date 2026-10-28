import { expect, test, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { browserRequest, freshPassword, login, type Human } from "./support";

export type InvitationOnboardingTestContext = {
  administrator: () => Human & { token: string; projectId: string; serviceId: string; environmentId: string };
};

// Registered after the shared one-shot administrator claim. No recipient DB
// fixture is used: this module creates every new person through /invite.
export function registerInvitationOnboardingTests(context: InvitationOnboardingTestContext) {
  test("a genuinely new recipient enrolls publicly, gets only invited access, and existing accounts cannot be taken over", async ({ page, browser }) => {
    test.setTimeout(180_000);
    const admin = context.administrator();
    const adminToken = await login(page, admin);
    await page.goto(`/projects/${admin.projectId}/settings`);
    const email = `new-invite-${randomUUID()}@example.test`;
    const password = freshPassword();
    const credential = await createProjectInvitation(page, email);
    const access = await browserRequest(page, adminToken, `/api/v1alpha/projects/${admin.projectId}/access`);
    expect(access.status).toBe(200);
    const recipientContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
    const anonymousContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
    try {
      const recipient = await recipientContext.newPage();
      await recipient.goto("/invite");
      await expect(recipient.getByRole("heading", { name: "Join with an invitation", exact: true })).toBeVisible();
      const created = recipient.waitForResponse(response => new URL(response.url()).pathname === "/api/v1alpha/invitations/onboard" && response.request().method() === "POST");
      const signedIn = recipient.waitForResponse(response => new URL(response.url()).pathname === "/api/v1alpha/auth/sessions" && response.request().method() === "POST");
      await submitNewAccount(recipient, credential, password);
      const enrollment = await created;
      expect(enrollment.status()).toBe(201);
      expect(enrollment.headers()["cache-control"].includes("no-store")).toBe(true);
      const enrollmentBody = await enrollment.json();
      expect(enrollmentBody.email).toBe(email);
      expect(enrollmentBody.invitation.project_id).toBe(admin.projectId);
      expect(enrollmentBody.invitation.project_role).toBe("viewer");
      expect(JSON.stringify(enrollmentBody).includes(credential)).toBe(false);
      expect(JSON.stringify(enrollmentBody).includes(password)).toBe(false);
      expect((await signedIn).status()).toBe(201);
      await expect(recipient).toHaveURL(new RegExp(`/projects/${admin.projectId}$`));
      const token = await recipient.evaluate(() => sessionStorage.getItem("midnight.human-session-token"));
      expect(Boolean(token?.startsWith("midn_user_"))).toBe(true);
      const me = await browserRequest(recipient, token!, "/api/v1alpha/auth/me");
      expect(me.body.principal.instance_admin).toBe(false);
      const projects = await browserRequest(recipient, token!, "/api/v1alpha/projects");
      expect(projects.body.map((project: { id: string }) => project.id)).toEqual([admin.projectId]);
      const grant = await browserRequest(recipient, token!, `/api/v1alpha/projects/${admin.projectId}/access`);
      expect(grant.body.project_role).toBe("viewer");
      expect(grant.body.permissions.includes("project.manage")).toBe(false);
      const configPath = `/api/v1alpha/services/${admin.serviceId}/environments/${admin.environmentId}/config`;
      const config = await browserRequest(recipient, token!, configPath);
      expect(config.status).toBe(200);
      const forbidden = await browserRequest(recipient, token!, configPath, "PUT", { expected_version: config.body.version, target_port: 9091 }, `"${config.body.version}"`);
      expect(forbidden.status).toBe(403);

      const anonymous = await anonymousContext.newPage();
      await anonymous.goto("/invite");
      const body = { invitation_token: credential, display_name: "Attempted replay", password: freshPassword() };
      expect((await browserRequest(anonymous, "", "/api/v1alpha/invitations/onboard", "POST", body)).status).toBe(409);
      const storage = await recipient.evaluate(() => [JSON.stringify(sessionStorage), JSON.stringify(localStorage)]);
      expect(storage.some(value => value.includes(credential) || value.includes(password))).toBe(false);
      expect(recipient.url().includes(credential)).toBe(false);

      // The fresh user already exists now. A separate invitation must require
      // normal authentication and must not change the account's password.
      const followup = await browserRequest(page, adminToken, "/api/v1alpha/invitations", "POST", {
        email, workspace_id: access.body.workspace_id, workspace_role: "viewer", expires_at: new Date(Date.now() + 86_400_000).toISOString(),
      });
      expect(followup.status).toBe(201);
      const followupToken = followup.body.invitation_token;
      const attemptedPassword = freshPassword();
      const refused = anonymous.waitForResponse(response => new URL(response.url()).pathname === "/api/v1alpha/invitations/onboard" && response.request().method() === "POST");
      await submitNewAccount(anonymous, followupToken, attemptedPassword);
      expect((await refused).status()).toBe(409);
      await expect(anonymous.getByRole("alert")).toContainText("This account already exists");
      expect((await browserRequest(anonymous, "", "/api/v1alpha/auth/sessions", "POST", { email, password: attemptedPassword, client_kind: "dashboard" })).status).toBe(401);
      await login(anonymous, { email, password });
      await anonymous.goto("/");
      await anonymous.getByLabel(/^Invitation credential/).fill(followupToken);
      await anonymous.getByRole("button", { name: "Accept invitation", exact: true }).click();
      await expect(anonymous.getByText("Invitation accepted", { exact: true })).toBeVisible();
    } finally {
      await recipientContext.close();
      await anonymousContext.close();
    }
  });

  test("revoked invitations and forged enrollment grants cannot create an account", async ({ page, browser }) => {
    const admin = context.administrator();
    const adminToken = await login(page, admin);
    const access = await browserRequest(page, adminToken, `/api/v1alpha/projects/${admin.projectId}/access`);
    const email = `revoked-invite-${randomUUID()}@example.test`;
    const invite = await browserRequest(page, adminToken, "/api/v1alpha/invitations", "POST", { email, workspace_id: access.body.workspace_id, project_id: admin.projectId, project_role: "viewer", expires_at: new Date(Date.now() + 86_400_000).toISOString() });
    expect(invite.status).toBe(201);
    const credential = invite.body.invitation_token;
    const privateContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
    try {
      const recipient = await privateContext.newPage();
      await recipient.goto("/invite");
      const password = freshPassword();
      const body = { invitation_token: credential, display_name: "Revoked invitee", password };
      const forged = await browserRequest(recipient, "", "/api/v1alpha/invitations/onboard", "POST", { ...body, project_role: "owner", workspace_id: "wrk_forged" });
      expect(forged.status).toBe(400);
      const revoked = await browserRequest(page, adminToken, `/api/v1alpha/invitations/${invite.body.invitation.id}?workspace_id=${access.body.workspace_id}&project_id=${admin.projectId}`, "DELETE", undefined, `"${invite.body.invitation.resource_version}"`);
      expect(revoked.status).toBe(204);
      const response = recipient.waitForResponse(result => new URL(result.url()).pathname === "/api/v1alpha/invitations/onboard" && result.request().method() === "POST");
      await submitNewAccount(recipient, credential, password);
      expect((await response).status()).toBe(409);
      await expect(recipient.getByRole("alert")).toContainText("This invitation is unavailable");
      expect((await browserRequest(recipient, "", "/api/v1alpha/auth/sessions", "POST", { email, password, client_kind: "dashboard" })).status).toBe(401);
    } finally { await privateContext.close(); }
  });
}

async function submitNewAccount(page: Page, token: string, password: string) {
  await page.getByLabel("Invitation credential", { exact: true }).fill(token);
  await page.getByLabel("Your name", { exact: true }).fill("New Invited Person");
  await page.locator("#onboard-password").fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Create account and join", exact: true }).click();
}

async function createProjectInvitation(page: Page, email: string) {
  await page.getByRole("button", { name: "+ New invitation", exact: true }).click();
  const form = page.getByRole("dialog", { name: "New project invitation", exact: true });
  await form.getByLabel("Recipient email", { exact: true }).fill(email);
  await form.getByLabel(/^Project role/).selectOption("viewer");
  await form.getByRole("button", { name: "Create invitation", exact: true }).click();
  const result = page.getByRole("dialog", { name: "Copy created invitation credential", exact: true });
  const token = await result.getByLabel("Invitation credential", { exact: true }).inputValue();
  await expect(result.getByRole("link", { name: "/invite", exact: true })).toHaveAttribute("href", "/invite");
  await result.getByRole("button", { name: "I have saved the credential", exact: true }).click();
  return token;
}
