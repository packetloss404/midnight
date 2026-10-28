import { expect, test, type Locator, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { browserRequest, freshPassword, login, type Human } from "./support";

type TenantChatContext = {
  administrator: () => Human & { token: string; projectId: string; serviceId: string; environmentId: string };
};

// The browser, authentication, memberships, chat, tools and PostgreSQL are
// real. Only the upstream model HTTP protocol is a local deterministic double;
// there is no page.route interception or vendor acceptance claim.
export function registerTenantChatTests(context: TenantChatContext) {
  test("project viewers and editors chat privately, use scoped tools, opt in to writes, and lose access on revocation", async ({ page, browser }) => {
    test.setTimeout(240_000);
    const admin = context.administrator();
    const adminToken = await login(page, admin);
    const access = await browserRequest(page, adminToken, `/api/v1alpha/projects/${admin.projectId}/access`);
    expect(access.status).toBe(200);
    const viewer = await inviteNewHuman(page, adminToken, access.body.workspace_id, admin.projectId, "viewer");
    const editor = await inviteNewHuman(page, adminToken, access.body.workspace_id, admin.projectId, "editor");
    const foreignProject = await browserRequest(page, adminToken, "/api/v1alpha/projects", "POST", {
      workspace_id: access.body.workspace_id, slug: `chat-private-${randomUUID().slice(0, 8)}`, display_name: "Other chat project",
    });
    expect(foreignProject.status).toBe(201);
    const viewerContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
    const editorContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
    for (const privateContext of [viewerContext, editorContext]) {
      await privateContext.addInitScript(() => {
        localStorage.setItem("midnight.agent.collapsed", "1");
        localStorage.setItem("midnight.agent.pinned", "0");
      });
    }
    try {
      const viewerPage = await viewerContext.newPage();
      const editorPage = await editorContext.newPage();
      const viewerToken = await login(viewerPage, viewer);
      const editorToken = await login(editorPage, editor);
      expect((await browserRequest(viewerPage, viewerToken, "/api/v1alpha/auth/me")).body.principal.instance_admin).toBe(false);
      expect((await browserRequest(editorPage, editorToken, "/api/v1alpha/auth/me")).body.principal.instance_admin).toBe(false);
      const viewerChat = await openNewChat(viewerPage, admin);
      await expect(viewerChat.dock.getByTestId("agent-allow-mutations")).toHaveAttribute("aria-checked", "false");
      const readMarker = toolMarker("get_service", { service_id: admin.serviceId });
      const readResponse = await sendMessage(viewerPage, viewerChat.dock, viewerChat.id, readMarker);
      expect(readResponse.request().postDataJSON().allow_mutations).toBe(false);
      await expect(viewerChat.dock.locator('[data-role="assistant"]').last()).toContainText("Browser fixture tool completed:");
      await expect(viewerChat.dock.locator('[data-role="assistant"]').last()).toContainText(admin.serviceId);
      await expect(viewerChat.dock.locator('[data-testid^="tool-call-"]').last()).toHaveAttribute("data-error", "false");
      // The read and tool result are durable after a real browser reload.
      expect(await viewerPage.evaluate(() => localStorage.getItem("midnight.agent.currentId"))).toBe(viewerChat.id);
      await viewerPage.reload();
      // Session/project authorization is asynchronous after reload. An
      // immediate isVisible() would skip the still-unmounted collapsed dock.
      await expect(viewerPage.getByTestId("agent-dock-fab")).toBeVisible();
      await viewerPage.getByTestId("agent-dock-fab").click();
      await expect(viewerPage.getByTestId("agent-dock").locator('[data-role="assistant"]').last()).toContainText("Browser fixture tool completed:");

      const editorChat = await openNewChat(editorPage, admin);
      await sendMessage(editorPage, editorChat.dock, editorChat.id, "private editor conversation");
      await expect(editorChat.dock.locator('[data-role="assistant"]').last()).toContainText("Browser fixture reply: private editor conversation");
      const variablePath = `/api/v1alpha/services/${admin.serviceId}/environments/${admin.environmentId}/variables`;
      const variableName = "QA_CHAT_BROWSER";
      const writeMarker = toolMarker("set_variable", { service_id: admin.serviceId, environment_id: admin.environmentId, name: variableName, value: "disposable-chat-fixture" });
      // A model asking for a write while the user toggle is off is refused.
      await sendMessage(editorPage, editorChat.dock, editorChat.id, writeMarker);
      await expect(editorChat.dock.locator('[data-testid^="tool-call-"]').last()).toHaveAttribute("data-error", "true");
      const before = await browserRequest(editorPage, editorToken, variablePath);
      expect(before.status).toBe(200);
      expect(before.body.some((variable: { name: string }) => variable.name === variableName)).toBe(false);
      await editorChat.dock.getByTestId("agent-allow-mutations").click();
      await expect(editorChat.dock.getByTestId("agent-allow-mutations")).toHaveAttribute("aria-checked", "true");
      const writeResponse = await sendMessage(editorPage, editorChat.dock, editorChat.id, writeMarker);
      expect(writeResponse.request().postDataJSON().allow_mutations).toBe(true);
      await expect(editorChat.dock.locator('[data-testid^="tool-call-"]').last()).toHaveAttribute("data-error", "false");
      const after = await browserRequest(editorPage, editorToken, variablePath);
      expect(after.status).toBe(200);
      expect(after.body.filter((variable: { name: string }) => variable.name === variableName)).toHaveLength(1);
      expect(JSON.stringify(after.body)).not.toContain("disposable-chat-fixture");

      const viewerCollection = await browserRequest(viewerPage, viewerToken, `/api/v1alpha/agent/conversations?project_id=${admin.projectId}&limit=200`);
      expect(viewerCollection.status).toBe(200);
      expect(viewerCollection.body.data.some((conversation: { id: string }) => conversation.id === viewerChat.id)).toBe(true);
      expect(viewerCollection.body.data.some((conversation: { id: string }) => conversation.id === editorChat.id)).toBe(false);
      expect((await browserRequest(viewerPage, viewerToken, `/api/v1alpha/agent/conversations/${editorChat.id}`)).status).toBe(404);
      expect((await browserRequest(editorPage, editorToken, `/api/v1alpha/agent/conversations/${viewerChat.id}/messages`)).status).toBe(404);
      expect((await browserRequest(viewerPage, viewerToken, `/api/v1alpha/agent/conversations?project_id=${foreignProject.body.id}`)).status).toBe(403);
      expect((await browserRequest(viewerPage, viewerToken, "/api/v1alpha/agent/conversations", "POST", { project_id: foreignProject.body.id })).status).toBe(403);

      const members = await browserRequest(page, adminToken, `/api/v1alpha/projects/${admin.projectId}/members`);
      expect(members.status).toBe(200);
      const membership = members.body.find((member: { user: { email: string } }) => member.user.email === viewer.email);
      expect(membership).toBeTruthy();
      const revoked = await browserRequest(page, adminToken, `/api/v1alpha/projects/${admin.projectId}/members/${membership.user.id}`, "DELETE", undefined, `"${membership.resource_version}"`);
      expect(revoked.status).toBe(204);
      expect((await browserRequest(viewerPage, viewerToken, `/api/v1alpha/agent/conversations/${viewerChat.id}`)).status).toBe(403);
      expect((await browserRequest(viewerPage, viewerToken, `/api/v1alpha/agent/conversations?project_id=${admin.projectId}`)).status).toBe(403);
      const draft = "membership revoked; this draft must remain";
      await viewerPage.getByTestId("agent-composer-input").fill(draft);
      const refusedStream = viewerPage.waitForResponse(response => new URL(response.url()).pathname === `/api/v1alpha/agent/conversations/${viewerChat.id}/stream`);
      await viewerPage.getByTestId("agent-composer-send").click();
      expect((await refusedStream).status()).toBe(403);
      await expect(viewerPage.getByTestId("agent-composer-input")).toHaveValue(draft);
      await expect(viewerPage.getByTestId("agent-send-error")).toBeVisible();
    } finally {
      await viewerContext.close();
      await editorContext.close();
    }
  });
}

async function inviteNewHuman(page: Page, token: string, workspaceId: string, projectId: string, role: "viewer" | "editor"): Promise<Human> {
  const human = { email: `chat-${role}-${randomUUID()}@example.test`, password: freshPassword() };
  const invitation = await browserRequest(page, token, "/api/v1alpha/invitations", "POST", {
    email: human.email, workspace_id: workspaceId, project_id: projectId, project_role: role,
    expires_at: new Date(Date.now() + 86_400_000).toISOString(),
  });
  expect(invitation.status).toBe(201);
  const enrolled = await browserRequest(page, "", "/api/v1alpha/invitations/onboard", "POST", {
    invitation_token: invitation.body.invitation_token, display_name: `Chat ${role}`, password: human.password,
  });
  expect(enrolled.status).toBe(201);
  return human;
}

async function openNewChat(page: Page, scope: { projectId: string; serviceId: string; environmentId: string }) {
  await page.goto(`/projects/${scope.projectId}/services/${scope.serviceId}/overview?env=${scope.environmentId}`);
  await page.getByTestId("agent-dock-fab").click();
  // Selecting the same service restores its inspector without changing the
  // target URL; reopening chat must still bind this exact service/environment.
  await page.locator(`[data-service-open="${scope.serviceId}"]`).click();
  await expect(page.locator(".service-inspector")).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/services/${scope.serviceId}/overview`));
  await page.getByTestId("agent-dock-fab").click();
  const dock = page.getByTestId("agent-dock");
  const created = page.waitForResponse(response => new URL(response.url()).pathname === "/api/v1alpha/agent/conversations" && response.request().method() === "POST");
  await dock.getByRole("button", { name: "New conversation", exact: true }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  const conversation = await response.json();
  expect(conversation.project_id).toBe(scope.projectId);
  expect(conversation.service_id).toBe(scope.serviceId);
  expect(conversation.metadata["midnight.environment_id"]).toBe(scope.environmentId);
  return { dock, id: conversation.id as string };
}

async function sendMessage(page: Page, dock: Locator, id: string, content: string) {
  await dock.getByTestId("agent-composer-input").fill(content);
  const completed = page.waitForResponse(response => new URL(response.url()).pathname === `/api/v1alpha/agent/conversations/${id}/messages` && response.request().method() === "POST");
  await dock.getByTestId("agent-composer-send").click();
  const response = await completed.catch(async () => {
    const reason = await dock.getByTestId("agent-send-error").innerText().catch(() => "No readable agent error was shown.");
    throw new Error(`Agent message POST did not complete. ${reason.slice(0, 500)}`);
  });
  expect(response.status()).toBe(200);
  await expect(dock.getByTestId("agent-composer-input")).toHaveValue("");
  return response;
}

function toolMarker(name: string, input: Record<string, unknown>) {
  return `MIDNIGHT_BROWSER_TOOL ${JSON.stringify({ name, input })}`;
}
