import { API_PREFIX, authHeader, type DeploymentResource, type OperationEvent } from "@/api/client";

export type DiagnosisReport = { id: string; text: string[] };
type Conversation = {
  id: string;
  actor_id?: string;
  project_id?: string;
  service_id?: string;
  metadata?: Record<string, unknown>;
};

async function read(path: string, signal: AbortSignal): Promise<Response> {
  const response = await fetch(`${API_PREFIX}${path}`, {
    headers: authHeader("product"), signal, cache: "no-store",
  });
  if (!response.ok) throw new Error("Diagnosis unavailable");
  return response;
}

// A finite durable SSE snapshot includes events attached after the failed
// operation's live stream closed. Polling this snapshot also rechecks access.
export async function loadDiagnosis(deployment: DeploymentResource, signal: AbortSignal): Promise<DiagnosisReport | null> {
  const snapshot = await (await read(`/operations/${encodeURIComponent(deployment.operation_id!)}/events`, signal)).text();
  let diagnosisId: string | undefined;
  for (const frame of snapshot.replace(/\r\n/g, "\n").split("\n\n")) {
    if (!frame.split("\n").includes("event: operation")) continue;
    const data = frame.split("\n").filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
    try {
      const event = JSON.parse(data) as OperationEvent;
      if (event.operation_id !== deployment.operation_id) continue;
      const match = /^deploy:diagnosis_attached diagnosis_id=([A-Za-z0-9_-]+)$/.exec(event.message ?? "");
      if (match) diagnosisId = match[1];
    } catch { /* Ignore malformed unrelated frames. */ }
  }
  if (!diagnosisId) return null;
  const base = `/agent/conversations/${encodeURIComponent(diagnosisId)}`;
  const conversation = await (await read(base, signal)).json() as Conversation;
  const metadata = conversation.metadata ?? {};
  if (conversation.id !== diagnosisId || conversation.actor_id !== "agent:diagnosis" ||
      conversation.project_id !== deployment.project_id || conversation.service_id !== deployment.service_id ||
      metadata["midnight.kind"] !== "diagnosis" ||
      metadata["midnight.operation_id"] !== deployment.operation_id ||
      metadata["midnight.deployment_id"] !== (deployment.deployment_id ?? deployment.id) ||
      metadata["midnight.project_id"] !== deployment.project_id ||
      metadata["midnight.service_id"] !== deployment.service_id ||
      metadata["midnight.environment_id"] !== deployment.environment_id) throw new Error("Diagnosis unavailable");
  const messages = await (await read(`${base}/messages`, signal)).json() as {
    data?: { role: string; content?: { kind?: string; type?: string; text?: string }[] }[];
    page?: { next_cursor?: string };
  };
  // The diagnosis producer writes one assistant report. Never render a partial
  // transcript or user/tool content as if it were the generated diagnosis.
  if (!Array.isArray(messages.data) || messages.page?.next_cursor) throw new Error("Diagnosis unavailable");
  const text = messages.data.filter((message) => message.role === "assistant")
    .flatMap((message) => message.content ?? [])
    .filter((block) => (block.kind ?? block.type) === "text" && typeof block.text === "string")
    .map((block) => block.text!);
  if (!text.length) throw new Error("Diagnosis unavailable");
  return { id: diagnosisId, text };
}
