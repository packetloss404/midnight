import { afterEach, describe, expect, it, vi } from "vitest";
import type { DeploymentResource } from "@/api/client";
import { loadDiagnosis } from "./api";

export const deployment = { id: "dpl_1", deployment_id: "dpl_1", operation_id: "op_1", project_id: "prj_1", service_id: "svc_1", environment_id: "env_1", operation_state: "FAILED" } as DeploymentResource;
const conversation = {
  id: "conv_1", actor_id: "agent:diagnosis", project_id: "prj_1", service_id: "svc_1",
  metadata: { "midnight.kind": "diagnosis", "midnight.project_id": "prj_1", "midnight.service_id": "svc_1", "midnight.environment_id": "env_1", "midnight.deployment_id": "dpl_1", "midnight.operation_id": "op_1" },
};
const attachment = (operation = "op_1") => `event: operation\ndata: ${JSON.stringify({ operation_id: operation, message: "deploy:diagnosis_attached diagnosis_id=conv_1" })}\n\n`;
const messages = { data: [{ role: "assistant", content: [{ kind: "text", text: "Check the listening port. <script>untrusted</script>" }] }] };
function fixtures(...responses: Response[]) {
  const fetch = vi.fn();
  responses.forEach((response) => fetch.mockResolvedValueOnce(response));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
afterEach(() => { vi.unstubAllGlobals(); sessionStorage.clear(); });
describe("durable diagnosis lookup", () => {
  it("reads the real SSE snapshot then validates report lineage", async () => {
    sessionStorage.setItem("midnight.human-session-token", "midn_user_test");
    const fetch = fixtures(new Response(attachment()), Response.json(conversation), Response.json(messages));
    expect(await loadDiagnosis(deployment, new AbortController().signal)).toEqual({ id: "conv_1", text: [messages.data[0].content[0].text] });
    expect(fetch.mock.calls[0][0]).toBe("/api/v1alpha/operations/op_1/events");
    expect(fetch.mock.calls[2][1]).toMatchObject({ headers: { Authorization: "Bearer midn_user_test" }, cache: "no-store" });
  });
  it("waits when no attachment exists and ignores another operation's event", async () => {
    fixtures(new Response(attachment("op_other")));
    expect(await loadDiagnosis(deployment, new AbortController().signal)).toBeNull();
  });
  it.each(["midnight.operation_id", "midnight.deployment_id", "midnight.project_id", "midnight.service_id", "midnight.environment_id"])("refuses mismatched %s without fetching messages", async (key) => {
    const fetch = fixtures(new Response(attachment()), Response.json({ ...conversation, metadata: { ...conversation.metadata, [key]: "other" } }));
    await expect(loadDiagnosis(deployment, new AbortController().signal)).rejects.toThrow("Diagnosis unavailable");
    expect(fetch).toHaveBeenCalledTimes(2);
  });
  it("refuses a forged user report and never renders partial pages", async () => {
    fixtures(new Response(attachment()), Response.json({ ...conversation, actor_id: "user:other" }));
    await expect(loadDiagnosis(deployment, new AbortController().signal)).rejects.toThrow();
    fixtures(new Response(attachment()), Response.json(conversation), Response.json({ ...messages, page: { next_cursor: "more" } }));
    await expect(loadDiagnosis(deployment, new AbortController().signal)).rejects.toThrow();
  });
  it("fails closed when report access is revoked", async () => {
    fixtures(new Response(attachment()), new Response("forbidden", { status: 403 }));
    await expect(loadDiagnosis(deployment, new AbortController().signal)).rejects.toThrow("Diagnosis unavailable");
  });
});
