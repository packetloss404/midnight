import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type PreviewCapabilityBlocker = {
  code:
    | "pull_request_router_missing"
    | "durable_preview_mapping_missing"
    | "preview_isolation_missing"
    | "preview_cleanup_missing";
  detail: string;
};

export type PreviewEnvironment = {
  id: string;
  pull_request: number;
  environment_id: string;
  deployment_id?: string;
  state: "creating" | "deploying" | "ready" | "refreshing" | "deleting" | "failed";
  url?: string;
  head_sha: string;
  from_fork: boolean;
  expires_at: string;
  updated_at: string;
};

export type PreviewEnvironmentList = {
  service_id: string;
  project_id: string;
  items: PreviewEnvironment[];
  capability: {
    state: "setup_required" | "enabled";
    automatic_lifecycle: boolean;
    received_event: "pull_request";
    current_disposition: "accepted_but_not_routed" | "routed";
    requested_actions: Array<
      | "opened:create"
      | "reopened:update"
      | "synchronize:update"
      | "closed:delete"
    >;
    blockers: PreviewCapabilityBlocker[];
  };
};

export async function listPreviewEnvironments(
  serviceId: string,
  signal?: AbortSignal,
): Promise<PreviewEnvironmentList> {
  const raw = await apiFetch<unknown>(
    `/services/${encodeURIComponent(serviceId)}/preview-environments`,
    { auth: "product", signal },
  );
  if (!isRecord(raw) || raw.service_id !== serviceId ||
      typeof raw.project_id !== "string" || raw.project_id.length === 0 ||
      !Array.isArray(raw.items) || !raw.items.every(isPreviewEnvironment) ||
      !isPreviewCapability(raw.capability)) {
    throw new Error(
      "Midnight sent back preview environments it could not read. Please try again.",
    );
  }
  return raw as PreviewEnvironmentList;
}

export function usePreviewEnvironments(serviceId: string) {
  return useQuery({
    queryKey: ["services", serviceId, "preview-environments"],
    queryFn: ({ signal }) => listPreviewEnvironments(serviceId, signal),
    enabled: Boolean(serviceId),
  });
}

const BLOCKER_CODES = new Set<PreviewCapabilityBlocker["code"]>([
  "pull_request_router_missing",
  "durable_preview_mapping_missing",
  "preview_isolation_missing",
  "preview_cleanup_missing",
]);

const REQUESTED_ACTIONS = new Set<
  PreviewEnvironmentList["capability"]["requested_actions"][number]
>([
  "opened:create",
  "reopened:update",
  "synchronize:update",
  "closed:delete",
]);

function isPreviewCapability(value: unknown): boolean {
  if (!isRecord(value) ||
      value.received_event !== "pull_request" ||
      !Array.isArray(value.requested_actions) ||
      value.requested_actions.length !== REQUESTED_ACTIONS.size ||
      new Set(value.requested_actions).size !== REQUESTED_ACTIONS.size ||
      value.requested_actions.some(
        (action) =>
          typeof action !== "string" ||
          !REQUESTED_ACTIONS.has(
            action as PreviewEnvironmentList["capability"]["requested_actions"][number],
          ),
      ) ||
      !Array.isArray(value.blockers)) {
    return false;
  }
  if (value.state === "enabled") {
    return value.automatic_lifecycle === true && value.current_disposition === "routed" &&
      value.blockers.length === 0;
  }
  if (value.state !== "setup_required" || value.automatic_lifecycle !== false ||
      value.current_disposition !== "accepted_but_not_routed" || value.blockers.length === 0) {
    return false;
  }
  return value.blockers.every(
    (blocker) =>
      isRecord(blocker) &&
      typeof blocker.code === "string" &&
      BLOCKER_CODES.has(blocker.code as PreviewCapabilityBlocker["code"]) &&
      typeof blocker.detail === "string" &&
      blocker.detail.trim().length > 0,
  );
}

const PREVIEW_STATES = new Set(["creating", "deploying", "ready", "refreshing", "deleting", "failed"]);

function isPreviewEnvironment(value: unknown): boolean {
  return isRecord(value) && typeof value.id === "string" && value.id.length > 0 &&
    typeof value.pull_request === "number" && value.pull_request > 0 &&
    typeof value.environment_id === "string" && value.environment_id.length > 0 &&
    typeof value.state === "string" && PREVIEW_STATES.has(value.state) &&
    typeof value.head_sha === "string" && /^[0-9a-f]{40}$/.test(value.head_sha) &&
    typeof value.from_fork === "boolean" &&
    typeof value.expires_at === "string" && !Number.isNaN(Date.parse(value.expires_at)) &&
    typeof value.updated_at === "string" && !Number.isNaN(Date.parse(value.updated_at)) &&
    (value.url === undefined || (typeof value.url === "string" && value.url.startsWith("https://"))) &&
    (value.deployment_id === undefined || typeof value.deployment_id === "string");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
