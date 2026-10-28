import { expect, test, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { randomUUID } from "node:crypto";
import { browserRequest, freshPassword, login } from "./support";
import type { InvitationOnboardingTestContext } from "./invitation-onboarding";

type Scope = { project_id: string; service_id: string; environment_id: string };
type DiagnosisFixture = { operation_id: string; deployment_id: string; conversation_id?: string };
const report = "Controlled browser diagnosis: set PORT to the application listener.";

// This is a controlled failed-deployment fixture, followed by the actual
// diagnosis Runner/provider HTTP adapter/conversation adapter/event persistence.
// It does not establish a real runtime failure or external model quality.
export function registerDiagnosisTests(context: InvitationOnboardingTestContext) {
  test("late runner-produced diagnosis is read-only, tenant-scoped, and hidden after membership revocation", async ({ page, browser }) => {
    test.setTimeout(180_000);
    const admin = context.administrator();
    const adminToken = await login(page, admin);
    const scope = { project_id: admin.projectId, service_id: admin.serviceId, environment_id: admin.environmentId };
    const adminAccess = await browserRequest(page, adminToken, `/api/v1alpha/projects/${admin.projectId}/access`);
    expect(adminAccess.status).toBe(200);
    const projects = await browserRequest(page, adminToken, "/api/v1alpha/projects");
    const other = projects.body.find((project: { id: string }) => project.id !== admin.projectId);
    expect(Boolean(other)).toBe(true);
    const fixture = await diagnosisFixture("prepare", scope);
    const viewerContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
    const outsiderContext = await browser.newContext({ baseURL: new URL(page.url()).origin });
    try {
      const viewer = await viewerContext.newPage();
      const outsider = await outsiderContext.newPage();
      const recipient = await onboardViewer(page, adminToken, viewer, adminAccess.body.workspace_id, admin.projectId);
      const outsiderAccess = await browserRequest(page, adminToken, `/api/v1alpha/projects/${other.id}/access`);
      const unrelated = await onboardViewer(page, adminToken, outsider, outsiderAccess.body.workspace_id, other.id);
      await viewer.goto(`/projects/${admin.projectId}/services/${admin.serviceId}/deployments?env=${admin.environmentId}&deployment=${fixture.deployment_id}`);
      const diagnosis = viewer.getByRole("region", { name: "Failure diagnosis", exact: true });
      await expect(diagnosis).toContainText("No diagnosis attached yet");
      const produced = await diagnosisFixture("run", scope, fixture);
      expect(Boolean(produced.conversation_id)).toBe(true);
      await expect(diagnosis).toContainText(report, { timeout: 25_000 });
      await expect(diagnosis.getByTestId("agent-composer-input")).toHaveCount(0);
      const conversationPath = `/api/v1alpha/agent/conversations/${produced.conversation_id}`;
      const conversation = await browserRequest(viewer, recipient.token, conversationPath);
      expect(conversation.status).toBe(200);
      expect(conversation.body.actor_id).toBe("agent:diagnosis");
      expect(conversation.body.metadata["midnight.operation_id"]).toBe(fixture.operation_id);
      expect(conversation.body.metadata["midnight.deployment_id"]).toBe(fixture.deployment_id);
      expect((await browserRequest(viewer, recipient.token, `${conversationPath}/messages`, "POST", { content: "Attempt to continue a system report", allow_mutations: false })).status).toBe(403);
      expect((await browserRequest(outsider, unrelated.token, conversationPath)).status).toBe(403);

      const memberPath = `/api/v1alpha/projects/${admin.projectId}/members/${recipient.userId}`;
      const member = await browserRequest(page, adminToken, memberPath);
      expect(member.status).toBe(200);
      const removed = await browserRequest(page, adminToken, memberPath, "DELETE", undefined, `"${member.body.resource_version}"`);
      expect(removed.status).toBe(204);
      // The visible report periodically revalidates its actual resource access.
      await expect(diagnosis).not.toContainText(report, { timeout: 20_000 });
      await expect(diagnosis).toContainText("Diagnosis unavailable");
      expect((await browserRequest(viewer, recipient.token, conversationPath)).status).toBe(403);
    } finally {
      await viewerContext.close();
      await outsiderContext.close();
    }
  });
}

async function onboardViewer(admin: Page, adminToken: string, page: Page, workspaceId: string, projectId: string) {
  const email = `diagnosis-viewer-${randomUUID()}@example.test`;
  const password = freshPassword();
  const invitation = await browserRequest(admin, adminToken, "/api/v1alpha/invitations", "POST", { email, workspace_id: workspaceId, project_id: projectId, project_role: "viewer", expires_at: new Date(Date.now() + 86_400_000).toISOString() });
  expect(invitation.status).toBe(201);
  await page.goto("/invite");
  const onboarded = await browserRequest(page, "", "/api/v1alpha/invitations/onboard", "POST", { invitation_token: invitation.body.invitation_token, display_name: "Diagnosis browser viewer", password });
  expect(onboarded.status).toBe(201);
  const token = await login(page, { email, password });
  return { token, userId: onboarded.body.user_id };
}

async function diagnosisFixture(mode: "prepare" | "run", scope: Scope, fixture?: DiagnosisFixture): Promise<DiagnosisFixture> {
  const state = JSON.parse(await readFile(process.env.MIDNIGHT_PG_E2E_STATE_PATH!, "utf8")) as { fixtureDsn: string };
  return new Promise((done, reject) => {
    const child = spawn("go", ["run", "./web-next/scripts/postgres-e2e-diagnosis.go"], {
      cwd: resolve(fileURLToPath(new URL("../..", import.meta.url))), stdio: ["pipe", "pipe", "pipe"],
    });
    let output = "";
    child.stdout.on("data", chunk => { output += chunk.toString(); if (output.length > 8192) child.kill(); });
    child.stderr.resume();
    child.on("error", () => reject(new Error("Controlled diagnosis producer could not start")));
    child.on("close", code => {
      if (code !== 0) { reject(new Error("Controlled diagnosis producer failed")); return; }
      try { done(JSON.parse(output) as DiagnosisFixture); } catch { reject(new Error("Controlled diagnosis producer returned an invalid result")); }
    });
    // DSN/credentials stay in private stdin; only safe fixture IDs use stdout.
    child.stdin.end(JSON.stringify({ mode, dsn: state.fixtureDsn, ...scope, ...(fixture ? { operation_id: fixture.operation_id, deployment_id: fixture.deployment_id } : {}) }));
  });
}
