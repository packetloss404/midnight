import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  __resetStagedChangesForTests,
  stageChange,
} from "@/lib/stagedChanges";

const apiFetch = vi.hoisted(() => vi.fn());
const graphqlRequest = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ apiFetch }));
vi.mock("@/api/graphql", () => ({ graphqlRequest }));

import {
  createEnvironment,
  createCommunityTemplateFork,
  createHumanProject,
  createHumanService,
  createService,
  deployHumanServiceImage,
  deployTemplate,
  discoverCommunityTemplate,
  duplicateEnvironment,
  listCommunityTemplateForks,
  stageCommunityTemplateFork,
  updateCommunityTemplateFork,
} from "./api";

beforeEach(() => {
  vi.clearAllMocks();
  __resetStagedChangesForTests();
  apiFetch.mockResolvedValue({
    id: "env_qa",
    project_id: "prj_one",
    slug: "qa",
    name: "QA",
    tier: "staging",
    copy_config: true,
    copied_service_configs: 1,
    copied_variables: 2,
    copied_variable_versions: 3,
    omitted_resources: [],
  });
});

describe("human environment create REST surface", () => {
  it("uses only human authority and the caller-stable replay key", async () => {
    apiFetch.mockResolvedValueOnce({
      id: "env_staging",
      project_id: "prj_one",
      slug: "staging",
      name: "Staging",
      tier: "staging",
      is_protected: false,
      resource_version: 1,
      created_at: "2026-07-29T12:00:00Z",
      updated_at: "2026-07-29T12:00:00Z",
    });

    const result = await createEnvironment(
      { projectId: " prj_one ", name: " Staging ", tier: "STAGING" },
      "environment-create-stable",
    );

    expect(apiFetch).toHaveBeenCalledWith("/environments", {
      auth: "human",
      method: "POST",
      body: {
        project_id: "prj_one",
        slug: "staging",
        name: "Staging",
        tier: "staging",
      },
      headers: { "Idempotency-Key": "environment-create-stable" },
    });
    expect(result.id).toBe("env_staging");
  });

  it("rejects mismatched, protected, or incomplete responses", async () => {
    const valid = {
      id: "env_staging",
      project_id: "prj_one",
      slug: "staging",
      name: "Staging",
      tier: "staging",
      is_protected: false,
      resource_version: 1,
      created_at: "2026-07-29T12:00:00Z",
      updated_at: "2026-07-29T12:00:00Z",
    };

    for (const response of [
      { ...valid, project_id: "prj_other" },
      { ...valid, is_protected: true },
      { ...valid, resource_version: 0 },
      { ...valid, updated_at: undefined },
    ]) {
      apiFetch.mockResolvedValueOnce(response);
      await expect(
        createEnvironment(
          { projectId: "prj_one", name: "Staging", tier: "STAGING" },
          "environment-create-stable",
        ),
      ).rejects.toThrow("invalid environment response");
    }
  });
});

describe("human project create REST surface", () => {
  it("uses only human authority, an explicit workspace, and a stable replay key", async () => {
    apiFetch.mockResolvedValueOnce({ id: "prj_one" });

    await createHumanProject(
      { name: "Tenant App", workspaceId: " wrk_one " },
      "project-create-stable",
    );

    expect(apiFetch).toHaveBeenCalledWith("/projects", {
      auth: "human",
      method: "POST",
      body: {
        workspace_id: "wrk_one",
        slug: "tenant-app",
        display_name: "Tenant App",
      },
      headers: { "Idempotency-Key": "project-create-stable" },
    });
  });
});

describe("community template REST surface", () => {
  const provenance = {
    source: "https://github.com/acme/queue",
    ref: "d".repeat(40),
    manifest_path: "template.yaml",
    manifest_sha256: "a".repeat(64),
    readme_path: "README.md",
    readme_sha256: "b".repeat(64),
    sbom_path: "sbom.json",
    sbom_sha256: "c".repeat(64),
  };
  const manifest = {
    schema_version: 1,
    template: {
      slug: "queue",
      name: "Queue",
      version: "1",
      summary: "Pinned queue",
      services: [],
    },
    metadata: {
      maintainer: "Acme",
      license: "Apache-2.0",
      security_review: "unreviewed" as const,
      risk_class: "moderate" as const,
      update_policy: "Monthly",
      deprecation_policy: "Six months",
      readme_path: "README.md",
      sbom: { path: "sbom.json", sha256: "c".repeat(64) },
    },
  };
  const discovery = {
    trust_class: "community" as const,
    manifest,
    provenance,
    readme: "# Queue",
    findings: [],
    valid: true,
  };
  const fork = {
    id: "tpl/fork",
    project_id: "prj_one",
    trust_class: "local" as const,
    upstream_manifest: manifest,
    draft: manifest,
    provenance,
    readme: "# Queue",
    findings: [],
    valid: true,
    resource_version: 7,
    created_at: "2026-08-30T12:00:00Z",
    updated_at: "2026-08-30T12:00:00Z",
  };

  it("uses explicit human authority for discovery and digest-fenced fork creation", async () => {
    apiFetch.mockResolvedValueOnce(discovery).mockResolvedValueOnce({ id: "tpl_1" });
    await discoverCommunityTemplate(
      { source: ` ${provenance.source} `, ref: ` ${provenance.ref} ` },
      "human",
    );
    await createCommunityTemplateFork(
      { projectId: "prj_one", discovery },
      "fork-stable",
      "human",
    );
    expect(apiFetch).toHaveBeenNthCalledWith(1, "/templates/community/discover", {
      auth: "human",
      method: "POST",
      body: { source: provenance.source, ref: provenance.ref },
    });
    expect(apiFetch).toHaveBeenNthCalledWith(2, "/templates/community/forks", {
      auth: "human",
      method: "POST",
      headers: { "Idempotency-Key": "fork-stable" },
      body: {
        project_id: "prj_one",
        source: provenance.source,
        ref: provenance.ref,
        expected_manifest_sha256: provenance.manifest_sha256,
      },
    });
  });

  it("uses strong conditional writes and project-scoped lists", async () => {
    apiFetch.mockResolvedValueOnce({ resource_version: 2 }).mockResolvedValueOnce({ forks: [] });
    await updateCommunityTemplateFork(
      { id: "tpl/a", projectId: "prj_one", resourceVersion: 1, draft: manifest },
      "human",
    );
    await listCommunityTemplateForks("prj/one", undefined, "human");
    expect(apiFetch).toHaveBeenNthCalledWith(1, "/templates/community/forks/tpl%2Fa", {
      auth: "human",
      method: "PATCH",
      headers: { "If-Match": `"1"` },
      body: { project_id: "prj_one", draft: manifest },
    });
    expect(apiFetch).toHaveBeenNthCalledWith(2, "/templates/community/forks?project_id=prj%2Fone", {
      signal: undefined,
      auth: "human",
    });
  });

  it("stages a valid fork through the current durable changeset revision", async () => {
    apiFetch
      .mockResolvedValueOnce({
        changeset: { revision: 4, state: "applied" },
      })
      .mockResolvedValueOnce({
        changeset: { revision: 5, state: "draft" },
      });

    await stageCommunityTemplateFork(
      {
        fork,
        projectId: "prj_one",
        environmentId: "env/production",
        serviceIds: { queue: "svc_queue" },
      },
      "template-stage-stable",
    );

    expect(apiFetch).toHaveBeenNthCalledWith(
      1,
      "/environments/env%2Fproduction/changeset",
      { auth: "human" },
    );
    expect(apiFetch).toHaveBeenNthCalledWith(
      2,
      "/templates/community/forks/tpl%2Ffork/changeset",
      {
        auth: "human",
        method: "POST",
        headers: {
          "Idempotency-Key": "template-stage-stable",
          "If-Match": '"4"',
        },
        body: {
          project_id: "prj_one",
          environment_id: "env/production",
          expected_revision: 4,
          expected_fork_version: 7,
          service_ids: { queue: "svc_queue" },
        },
      },
    );
  });

  it("refuses to replace another editor's active environment draft", async () => {
    apiFetch.mockResolvedValueOnce({
      changeset: { revision: 4, state: "draft" },
    });

    await expect(
      stageCommunityTemplateFork({
        fork,
        projectId: "prj_one",
        environmentId: "env_prod",
        serviceIds: { queue: "svc_queue" },
      }),
    ).rejects.toThrow("already has a saved draft");

    expect(apiFetch).toHaveBeenCalledTimes(1);
  });

  it("requires tab-local edits to be resolved before server-side template compilation", async () => {
    stageChange({
      serviceId: "svc_queue",
      environmentId: "env_prod",
      name: "LOG_LEVEL",
      op: "add",
      scope: "plain",
      value: "info",
    });

    await expect(
      stageCommunityTemplateFork({
        fork,
        projectId: "prj_one",
        environmentId: "env_prod",
        serviceIds: { queue: "svc_queue" },
      }),
    ).rejects.toThrow("Save or discard the tab-local Changes edits");

    expect(apiFetch).not.toHaveBeenCalled();
  });
});

describe("environment duplicate REST surface", () => {
  it("uses a fresh replay key for the real config-bearing transaction", async () => {
    await duplicateEnvironment({
      sourceEnvironmentId: "env/source",
      projectId: "prj_one",
      name: "QA",
      copyConfig: true,
    });

    expect(apiFetch).toHaveBeenCalledWith(
      "/environments/env%2Fsource/duplicate",
      {
        auth: "operator",
        method: "POST",
        body: {
          project_id: "prj_one",
          slug: "qa",
          name: "QA",
          copy_config: true,
        },
        headers: { "Idempotency-Key": expect.any(String) },
      },
    );
    const headers = apiFetch.mock.calls[0]?.[1]?.headers;
    expect(headers["Idempotency-Key"].length).toBeGreaterThan(0);
  });

  it("does not imply transactional config copy in metadata-only mode", async () => {
    await duplicateEnvironment({
      sourceEnvironmentId: "env_staging",
      projectId: "prj_one",
      name: "QA metadata",
      slug: "qa-metadata",
      copyConfig: false,
    });

    expect(apiFetch).toHaveBeenCalledWith(
      "/environments/env_staging/duplicate",
      expect.objectContaining({
        body: expect.objectContaining({ copy_config: false }),
        headers: undefined,
      }),
    );
  });
});

describe("service create REST surface", () => {
  it("keeps the existing operator-only authority", async () => {
    apiFetch.mockResolvedValueOnce({
      id: "svc_1",
      project_id: "prj_one",
      slug: "api",
      display_name: "API",
    });

    await createService({ projectId: "prj_one", name: "API" });

    expect(apiFetch).toHaveBeenCalledWith("/services", {
      auth: "operator",
      method: "POST",
      body: {
        project_id: "prj_one",
        slug: "api",
        display_name: "API",
      },
      headers: { "Idempotency-Key": expect.any(String) },
    });
  });

  it("uses only human authority and the caller's replay key for tenant create", async () => {
    apiFetch.mockResolvedValueOnce({
      id: "svc_1",
      project_id: "prj_one",
      slug: "api",
      display_name: "API",
    });

    await createHumanService(
      { projectId: "prj_one", name: "API" },
      "service-create-stable",
    );

    expect(apiFetch).toHaveBeenCalledWith("/services", {
      auth: "human",
      method: "POST",
      body: {
        project_id: "prj_one",
        slug: "api",
        display_name: "API",
      },
      headers: { "Idempotency-Key": "service-create-stable" },
    });
  });
});

describe("human immutable-image deployment REST surface", () => {
  const image = `registry.example/team/api@sha256:${"a".repeat(64)}`;

  it("posts the exact admitted body with human auth and a caller-stable key", async () => {
    apiFetch.mockResolvedValueOnce({ id: "op_1", state: "queued" });

    await deployHumanServiceImage(
      {
        serviceId: "svc/one",
        environmentId: "env_prod",
        image,
      },
      "deploy-stable",
    );

    expect(apiFetch).toHaveBeenCalledWith(
      "/services/svc%2Fone/deployments",
      {
        auth: "human",
        method: "POST",
        headers: { "Idempotency-Key": "deploy-stable" },
        body: {
          environment_id: "env_prod",
          image,
        },
      },
    );
    expect(graphqlRequest).not.toHaveBeenCalled();
  });

  it("adds only the strict generated-route intent when explicitly requested", async () => {
    apiFetch.mockResolvedValueOnce({ id: "op_1", state: "queued" });

    await deployHumanServiceImage(
      {
        serviceId: "svc_one",
        environmentId: "env_prod",
        image,
        route: { public: true },
      },
      "deploy-public-stable",
    );

    expect(apiFetch).toHaveBeenCalledWith(
      "/services/svc_one/deployments",
      {
        auth: "human",
        method: "POST",
        headers: { "Idempotency-Key": "deploy-public-stable" },
        body: {
          environment_id: "env_prod",
          image,
          route: { public: true },
        },
      },
    );
    expect(graphqlRequest).not.toHaveBeenCalled();
  });

  it("rejects expanded or disabled route objects before sending a request", async () => {
    await expect(
      deployHumanServiceImage(
        {
          serviceId: "svc_one",
          environmentId: "env_prod",
          image,
          route: { public: false, host: "example.test" } as never,
        },
        "deploy-public-stable",
      ),
    ).rejects.toThrow("exactly { public: true }");

    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("rejects mutable images and malformed operation responses before claiming success", async () => {
    await expect(
      deployHumanServiceImage(
        {
          serviceId: "svc_one",
          environmentId: "env_prod",
          image: "registry.example/team/api:latest",
        },
        "deploy-stable",
      ),
    ).rejects.toThrow("immutable image digest");
    expect(apiFetch).not.toHaveBeenCalled();

    apiFetch.mockResolvedValueOnce({ id: "op_1" });
    await expect(
      deployHumanServiceImage(
        {
          serviceId: "svc_one",
          environmentId: "env_prod",
          image,
        },
        "deploy-stable",
      ),
    ).rejects.toThrow("sent back a response it could not read");
  });
});

describe("official database template REST surface", () => {
  it("queues the durable saga with a fresh idempotency key", async () => {
    apiFetch.mockResolvedValueOnce({
      operation_id: "op_1",
      provision_run_id: "tpl_1",
      template_slug: "postgres",
      state: "queued",
    });

    await deployTemplate(
      "postgres",
      {
        projectId: "prj_one",
        environmentId: "env_prod",
        name: "Orders database",
      },
      "template-stable",
    );

    expect(apiFetch).toHaveBeenCalledWith(
      "/templates/postgres/instances",
      {
        method: "POST",
        body: {
          project_id: "prj_one",
          environment_id: "env_prod",
          name: "Orders database",
        },
        headers: { "Idempotency-Key": "template-stable" },
      },
    );
  });
});
