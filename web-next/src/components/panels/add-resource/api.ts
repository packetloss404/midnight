import { apiFetch, type Service } from "@/api/client";
import type { ProjectAccess } from "@/components/panels/automation-tokens/api";
import type { VolumeRecord } from "@/components/panels/storage/api";
import type { TemplateDeployResult } from "@/components/panels/create/api";

export type ResourceKind =
  | "github"
  | "image"
  | "empty"
  | "template"
  | "database"
  | "volume";

export type ResourceCapability = { available: boolean; reason: string };
export type ResourceCapabilities = Record<ResourceKind, ResourceCapability>;
export type ResourceMutationAuthority = "human" | "operator";

export function resolveResourceCapabilities(input: {
  authority: "human" | "operator";
  projectId: string;
  environmentId?: string;
  projectAccess?: ProjectAccess;
  environmentAccess?: ProjectAccess;
  accessPending?: boolean;
  installationAdminAuthorized?: boolean;
}): ResourceCapabilities {
  const operator = input.authority === "operator";
  const exactProject =
    input.projectAccess?.principal_kind === "human" &&
    input.projectAccess.project_id === input.projectId &&
    input.projectAccess.environment_id === undefined;
  const exactEnvironment =
    input.environmentAccess?.principal_kind === "human" &&
    input.environmentAccess.project_id === input.projectId &&
    input.environmentAccess.environment_id === input.environmentId;
  const projectPermissions = new Set(input.projectAccess?.permissions ?? []);
  const environmentPermissions = new Set(input.environmentAccess?.permissions ?? []);
  const checking = input.accessPending === true;
  const installationProjectAuthorized =
    operator ||
    (input.installationAdminAuthorized === true &&
      exactProject &&
      projectPermissions.has("project.read") &&
      projectPermissions.has("service.write"));
  const installationEnvironmentAuthorized =
    operator ||
    (input.installationAdminAuthorized === true &&
      exactEnvironment &&
      environmentPermissions.has("project.read") &&
      environmentPermissions.has("service.write") &&
      environmentPermissions.has("volume.write"));
  const environmentRequired = input.environmentId
    ? ""
    : "Select an environment first.";
  const humanReason = checking
    ? "Checking current project permissions."
    : "Your exact project permissions do not grant this action.";

  return {
    empty: operator || (exactProject && projectPermissions.has("service.write"))
      ? available("Creates a source-free service through POST /services.")
      : unavailable(humanReason),
    image:
      Boolean(input.environmentId) &&
      (operator ||
        (exactEnvironment &&
          environmentPermissions.has("service.write") &&
          environmentPermissions.has("deployment.trigger.cli")))
        ? available("Creates a service and deploys a digest-pinned image.")
        : unavailable(environmentRequired || humanReason),
    github:
      operator ||
      (exactProject &&
        projectPermissions.has("service.write") &&
        projectPermissions.has("deployment.trigger.git"))
        ? available("Creates a service and binds a repository Midnight can verify.")
        : unavailable(humanReason),
    template: installationProjectAuthorized
      ? available("Provisions an official executable template.")
      : unavailable("Official template provisioning requires installation-admin capability and exact project access."),
    database: installationProjectAuthorized
      ? available("Provisions an official database template.")
      : unavailable("Database provisioning requires installation-admin capability and exact project access."),
    volume:
      installationEnvironmentAuthorized && Boolean(input.environmentId)
        ? available("Creates storage through POST /volumes for a selected service.")
        : unavailable(
            operator || input.installationAdminAuthorized
              ? environmentRequired || "Volume provisioning requires exact environment service-write and volume-write access."
              : "Volume provisioning requires installation-admin capability.",
          ),
  };
}

export function createOperatorVolume(
  input: {
    projectId: string;
    environmentId: string;
    service: Pick<Service, "id">;
    name: string;
    mountPath: string;
    sizeBytes: number;
  },
  idempotencyKey: string,
  authority: ResourceMutationAuthority = "operator",
): Promise<VolumeRecord> {
  if (!input.environmentId || !input.service.id) {
    throw new Error("A service and environment are required for storage.");
  }
  if (!Number.isSafeInteger(input.sizeBytes) || input.sizeBytes < 1) {
    throw new Error("A positive storage size is required.");
  }
  return apiFetch<VolumeRecord>("/volumes", {
    auth: authority,
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: {
      project_id: input.projectId,
      environment_id: input.environmentId,
      service_id: input.service.id,
      name: input.name.trim(),
      mount_path: input.mountPath.trim(),
      size_bytes: input.sizeBytes,
    },
  });
}

export function deployOperatorTemplate(
  slug: string,
  input: { projectId: string; environmentId?: string; name?: string },
  idempotencyKey: string,
  authority: ResourceMutationAuthority = "operator",
): Promise<TemplateDeployResult> {
  return apiFetch<TemplateDeployResult>(
    `/templates/${encodeURIComponent(slug)}/instances`,
    {
      auth: authority,
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: {
        project_id: input.projectId,
        environment_id: input.environmentId,
        name: input.name,
      },
    },
  );
}

export function listResourceServices(
  projectId: string,
  environmentId: string | undefined,
  authority: ResourceMutationAuthority,
  signal?: AbortSignal,
): Promise<Service[]> {
  if (!projectId.trim()) {
    throw new Error("A project is required to list resource services.");
  }
  const query = new URLSearchParams({ project_id: projectId });
  if (environmentId) query.set("environment_id", environmentId);
  return apiFetch<Service[]>(`/services?${query.toString()}`, {
    auth: authority,
    signal,
  });
}

function available(reason: string): ResourceCapability {
  return { available: true, reason };
}

function unavailable(reason: string): ResourceCapability {
  return { available: false, reason };
}
