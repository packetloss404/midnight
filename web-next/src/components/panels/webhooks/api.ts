import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch, type RequestAuthPolicy } from "@/api/client";

export type WebhookDeliveryState =
  | "pending"
  | "processing"
  | "retry_wait"
  | "succeeded"
  | "ignored"
  | "dead_letter";

export type WebhookDeliverySummary = {
  id: string;
  provider: "github" | "gitlab-generic" | "bitbucket" | "midnight-incoming";
  event_type: string;
  provider_delivery_id: string;
  state: WebhookDeliveryState;
  route_attempt: number;
  max_route_attempts: number;
  manual_retries: number;
  reason_code?: string;
  last_error?: string;
  received_at: string;
  updated_at: string;
};

export type WebhookDeliveryTarget = {
  id: string;
  project_id: string;
  service_id: string;
  repository: string;
  branch: string;
  commit_sha: string;
  state: WebhookDeliveryState;
  attempt: number;
  max_attempts: number;
  manual_retries: number;
  reason_code?: string;
  last_error?: string;
  operation_id?: string;
  deployment_id?: string;
  updated_at: string;
};

export type WebhookDeliveryDetail = WebhookDeliverySummary & {
  targets: WebhookDeliveryTarget[];
  exclusive_project: boolean;
};

type WebhookDeliveryPage = { items: WebhookDeliverySummary[]; next_cursor?: string };

export const projectWebhookKeys = {
  list: (projectId: string) => ["project-webhook-deliveries", projectId] as const,
};

export async function listProjectWebhookDeliveries(
  projectId: string,
  authority: Extract<RequestAuthPolicy, "human" | "operator">,
  signal?: AbortSignal,
): Promise<WebhookDeliveryDetail[]> {
  if (!projectId.trim()) throw new Error("A project is required.");
  const scope = encodeURIComponent(projectId);
  const page = await apiFetch<WebhookDeliveryPage>(
    `/webhook-deliveries?project_id=${scope}&limit=50`,
    { auth: authority, signal },
  );
  if (!page || !Array.isArray(page.items)) {
    throw new Error("Midnight returned an invalid webhook delivery list.");
  }

  // Both collection and detail are server-scoped. Detail fetches supply target
  // evidence for the redrive exclusivity check without downloading foreign
  // target metadata into the browser.
  const details = await Promise.all(
    page.items.map((item) =>
      apiFetch<WebhookDeliveryDetail>(
        `/webhook-deliveries/${encodeURIComponent(item.id)}?project_id=${scope}`,
        { auth: authority, signal },
      ),
    ),
  );
  for (const detail of details) {
    if (
      !detail ||
      !Array.isArray(detail.targets) ||
      typeof detail.exclusive_project !== "boolean" ||
      detail.targets.some((target) => target.project_id !== projectId)
    ) {
      throw new Error("Midnight returned a webhook delivery without target ownership metadata.");
    }
  }
  return details;
}

export function canRedriveProjectDelivery(
  detail: WebhookDeliveryDetail,
  projectId: string,
): boolean {
  return (
    detail.targets.length > 0 &&
    detail.exclusive_project === true &&
    detail.targets.every((target) => target.project_id === projectId)
  );
}

export function redriveWebhookDelivery(
  deliveryId: string,
  projectId: string,
  idempotencyKey: string,
  authority: Extract<RequestAuthPolicy, "human" | "operator">,
): Promise<WebhookDeliveryDetail> {
  return apiFetch<WebhookDeliveryDetail>(
    `/webhook-deliveries/${encodeURIComponent(deliveryId)}/redrive?project_id=${encodeURIComponent(projectId)}`,
    {
      auth: authority,
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      // The endpoint's exact-one-document contract requires an empty body.
    },
  );
}

export function useProjectWebhookDeliveries(
  projectId: string,
  authority: Extract<RequestAuthPolicy, "human" | "operator">,
  enabled: boolean,
) {
  return useQuery<WebhookDeliveryDetail[], Error>({
    queryKey: projectWebhookKeys.list(projectId),
    queryFn: ({ signal }) => listProjectWebhookDeliveries(projectId, authority, signal),
    enabled: enabled && Boolean(projectId),
  });
}

export function useRedriveProjectWebhookDelivery(
  projectId: string,
  authority: Extract<RequestAuthPolicy, "human" | "operator">,
) {
  const client = useQueryClient();
  return useMutation<
    WebhookDeliveryDetail,
    Error,
    { detail: WebhookDeliveryDetail; idempotencyKey: string }
  >({
    mutationFn: ({ detail, idempotencyKey }) => {
      if (!canRedriveProjectDelivery(detail, projectId)) {
        throw new Error(
          "This delivery includes targets outside the current project and cannot be redriven from project settings.",
        );
      }
      return redriveWebhookDelivery(detail.id, projectId, idempotencyKey, authority);
    },
    onSuccess: () =>
      client.invalidateQueries({ queryKey: projectWebhookKeys.list(projectId) }),
  });
}
