import { afterEach, describe, expect, it, vi } from "vitest";
import {
  API_PREFIX,
  ApiError,
  TOKEN_CHANGED_EVENT,
  TOKEN_STORAGE_KEY,
  api,
  apiFetch,
  authHeader,
  getToken,
  setToken,
} from "./client";
import {
  HUMAN_SESSION_TOKEN_STORAGE_KEY,
  getHumanSessionToken,
  setHumanSessionToken,
  setOperatorRecoveryToken,
} from "./credentials";

afterEach(() => {
  window.sessionStorage.clear();
  vi.restoreAllMocks();
});

describe("runtime API token", () => {
  it("stores a trimmed token for the current browser session", () => {
    setToken("  runtime-secret  ");

    expect(window.sessionStorage.getItem(TOKEN_STORAGE_KEY)).toBe("runtime-secret");
    expect(getToken()).toBe("runtime-secret");
    expect(authHeader()).toEqual({ Authorization: "Bearer runtime-secret" });
  });

  it("clears the runtime token and announces changes", () => {
    const listener = vi.fn();
    window.addEventListener(TOKEN_CHANGED_EVENT, listener);
    setToken("runtime-secret");
    setToken();

    expect(window.sessionStorage.getItem(TOKEN_STORAGE_KEY)).toBeNull();
    expect(listener).toHaveBeenCalledTimes(2);
    window.removeEventListener(TOKEN_CHANGED_EVENT, listener);
  });

  it("reports browser storage write failures without announcing a change", () => {
    const listener = vi.fn();
    window.addEventListener(TOKEN_CHANGED_EVENT, listener);
    const restoreStorage = installThrowingSessionStorage("setItem");
    try {
      expect(setHumanSessionToken("midn_user_not-saved")).toBe(false);
      expect(setOperatorRecoveryToken("operator-not-saved")).toBe(false);
      expect(listener).not.toHaveBeenCalled();
      expect(getHumanSessionToken()).toBeUndefined();
      expect(getToken()).toBeUndefined();
    } finally {
      restoreStorage();
      window.removeEventListener(TOKEN_CHANGED_EVENT, listener);
    }
  });

  it("reports browser storage removal failures and leaves the token intact", () => {
    setHumanSessionToken("midn_user_still-present");
    const listener = vi.fn();
    window.addEventListener(TOKEN_CHANGED_EVENT, listener);
    const restoreStorage = installThrowingSessionStorage("removeItem");
    try {
      expect(setHumanSessionToken()).toBe(false);
      expect(listener).not.toHaveBeenCalled();
      expect(getHumanSessionToken()).toBe("midn_user_still-present");
    } finally {
      restoreStorage();
      window.removeEventListener(TOKEN_CHANGED_EVENT, listener);
    }
  });
});

function installThrowingSessionStorage(
  operation: "setItem" | "removeItem",
): () => void {
  const storage = window.sessionStorage;
  const descriptor = Object.getOwnPropertyDescriptor(window, "sessionStorage");
  const replacement: Storage = {
    get length() {
      return storage.length;
    },
    clear: () => storage.clear(),
    getItem: (key) => storage.getItem(key),
    key: (index) => storage.key(index),
    removeItem: (key) => {
      if (operation === "removeItem") throw new Error("storage disabled");
      storage.removeItem(key);
    },
    setItem: (key, value) => {
      if (operation === "setItem") throw new Error("storage disabled");
      storage.setItem(key, value);
    },
  };
  Object.defineProperty(window, "sessionStorage", {
    configurable: true,
    value: replacement,
  });
  return () => {
    if (descriptor) Object.defineProperty(window, "sessionStorage", descriptor);
    else {
      Object.defineProperty(window, "sessionStorage", {
        configurable: true,
        value: storage,
      });
    }
  };
}

describe("credential authority", () => {
  it("stores human and operator credentials separately", () => {
    setHumanSessionToken("  midn_user_human-session  ");
    setOperatorRecoveryToken("operator-recovery");

    expect(window.sessionStorage.getItem(HUMAN_SESSION_TOKEN_STORAGE_KEY)).toBe(
      "midn_user_human-session",
    );
    expect(getHumanSessionToken()).toBe("midn_user_human-session");
    expect(authHeader("none")).toEqual({});
    expect(authHeader("human")).toEqual({
      Authorization: "Bearer midn_user_human-session",
    });
    expect(authHeader("product")).toEqual({
      Authorization: "Bearer midn_user_human-session",
    });
    expect(authHeader("operator")).toEqual({
      Authorization: "Bearer midn_user_human-session",
    });
  });

  it("prefers the signed-in human session for installation administration", () => {
    setHumanSessionToken("midn_user_admin-session");
    setOperatorRecoveryToken("operator-recovery");

    expect(authHeader("operator")).toEqual({
      Authorization: "Bearer midn_user_admin-session",
    });
  });

  it("never substitutes recovery access for an explicit human request", () => {
    setOperatorRecoveryToken("operator-recovery");

    expect(authHeader("human")).toEqual({});
    expect(authHeader("product")).toEqual({
      Authorization: "Bearer operator-recovery",
    });
  });

  it("never attaches a saved credential to login", async () => {
    setHumanSessionToken("midn_user_stale");
    setOperatorRecoveryToken("operator-recovery");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          access_token: "midn_user_fresh",
          token_type: "Bearer",
          expires_in: 3600,
          user: {
            id: "usr_1",
            email: "person@example.com",
            display_name: "Person",
            mfa_enabled: false,
          },
          session: {
            id: "ses_1",
            user_id: "usr_1",
            token_prefix: "midn_user_f",
            client_kind: "dashboard",
            created_at: "2026-01-01T00:00:00Z",
            last_seen_at: "2026-01-01T00:00:00Z",
            expires_at: "2026-01-02T00:00:00Z",
            idle_expires_at: "2026-01-01T08:00:00Z",
            current: true,
          },
        }),
        { status: 201, headers: { "content-type": "application/json" } },
      ),
    );

    await api.createIdentitySession({
      email: "person@example.com",
      password: "secret",
    });

    const headers = vi.mocked(fetch).mock.calls[0]?.[1]?.headers as Record<
      string,
      string
    >;
    expect(headers.Authorization).toBeUndefined();
  });

  it("uses the human session across product and installation routes", async () => {
    setHumanSessionToken("midn_user_human-session");
    setOperatorRecoveryToken("operator-recovery");
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response("[]", {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response("[]", {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );

    await api.listProjects();
    await api.listGithubInstallations();

    const productHeaders = vi.mocked(fetch).mock.calls[0]?.[1]?.headers as Record<
      string,
      string
    >;
    const operatorHeaders = vi.mocked(fetch).mock.calls[1]?.[1]?.headers as Record<
      string,
      string
    >;
    expect(productHeaders.Authorization).toBe(
      "Bearer midn_user_human-session",
    );
    expect(operatorHeaders.Authorization).toBe(
      "Bearer midn_user_human-session",
    );
  });

  it("verifies a candidate recovery token without storing or substituting human auth", async () => {
    setHumanSessionToken("midn_user_human-session");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(JSON.stringify({ status: "ready" }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );

    await api.verifyOperatorRecoveryToken(" candidate-recovery ");

    const headers = vi.mocked(fetch).mock.calls[0]?.[1]?.headers as Record<
      string,
      string
    >;
    expect(headers.Authorization).toBe("Bearer candidate-recovery");
    expect(getToken()).toBeUndefined();
    expect(getHumanSessionToken()).toBe("midn_user_human-session");
  });
});

describe("API error boundary", () => {
  it("replaces an upstream HTML error body and preserves the request id", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response("<!doctype html><title>proxy details</title>", {
        status: 502,
        headers: { "x-request-id": "req-safe-1" },
      }),
    );

    const rejection = apiFetch("/projects").catch((error: unknown) => error);
    await expect(rejection).resolves.toMatchObject({
      name: "ApiError",
      status: 502,
      message: "Request failed (502)",
      requestId: "req-safe-1",
    });
    expect(fetch).toHaveBeenCalledWith(
      `${API_PREFIX}/projects`,
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("bounds a non-JSON error body before it reaches any component", () => {
    const error = new ApiError(400, "x".repeat(1_000));
    expect(error.message.length).toBeLessThanOrEqual(280);
    expect(error.message.endsWith("…")).toBe(true);
  });

  it("exposes a bounded Retry-After delay for login throttling", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: { code: "AUTH_RATE_LIMITED", message: "Try later" },
        }),
        { status: 429, headers: { "retry-after": "17" } },
      ),
    );

    const error = await apiFetch("/auth/sessions", { auth: "none" }).catch(
      (caught: unknown) => caught,
    );
    expect(error).toMatchObject({
      status: 429,
      code: "AUTH_RATE_LIMITED",
      retryAfterSeconds: 17,
    });
  });
});

describe("environment resource summary", () => {
  it("requests one encoded environment-scoped resource endpoint", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          schema_version: "midnight.environment-resources/v1alpha1",
          project: { id: "prj_1", slug: "midnight", display_name: "Midnight" },
          environment: {
            id: "env/prod",
            project_id: "prj_1",
            slug: "production",
            name: "Production",
            tier: "production",
            is_protected: true,
          },
          resources: [],
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await expect(api.getEnvironmentResources("env/prod")).resolves.toMatchObject({
      schema_version: "midnight.environment-resources/v1alpha1",
      environment: { id: "env/prod" },
      resources: [],
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      `${API_PREFIX}/environments/env%2Fprod/resources`,
      expect.objectContaining({ method: "GET" }),
    );
  });
});

describe("latest active deployment", () => {
  it("normalizes an empty 204 response to null for the query cache", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(null, { status: 204 }),
    );

    await expect(api.latestActiveDeployment("svc/no-active")).resolves.toBeNull();
    expect(fetch).toHaveBeenCalledWith(
      `${API_PREFIX}/services/svc%2Fno-active/deployments/latest-active`,
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("uses product authority and an exact encoded environment scope", async () => {
    setHumanSessionToken("midn_user_deployment-reader");
    setOperatorRecoveryToken("operator-recovery");
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response("[]", {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            id: "dep_1",
            service_id: "svc/one",
            environment_id: "env/prod",
          }),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        ),
      );

    await api.listDeployments("svc/one", "env/prod");
    await api.latestActiveDeployment("svc/one", "env/prod");

    for (const [url, init] of vi.mocked(fetch).mock.calls) {
      expect(url).toContain("environment_id=env%2Fprod");
      expect((init?.headers as Record<string, string>).Authorization).toBe(
        "Bearer midn_user_deployment-reader",
      );
    }
  });

  it("walks signed deployment pages without changing scope", async () => {
    setHumanSessionToken("midn_user_deployment-reader");
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            items: [
              {
                id: "dep_2",
                service_id: "svc/one",
                operation_id: "op_2",
                operation_state: "RUNNING",
                status: "BUILDING",
                active: false,
              },
            ],
            next_cursor: "dc1.signed/page+2=",
          }),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            items: [
              {
                id: "dep_1",
                service_id: "svc/one",
                operation_id: "op_1",
                operation_state: "SUCCEEDED",
                status: "ACTIVE",
                active: true,
              },
            ],
          }),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        ),
      );

    const deployments = await api.listDeployments("svc/one", "env/prod");

    expect(deployments.map((deployment) => deployment.id)).toEqual([
      "dep_2",
      "dep_1",
    ]);
    const calls = vi.mocked(fetch).mock.calls;
    expect(calls).toHaveLength(2);
    expect(calls[0]?.[0]).toContain(
      "/services/svc%2Fone/deployments?environment_id=env%2Fprod&limit=100",
    );
    expect(calls[1]?.[0]).toContain(
      "cursor=dc1.signed%2Fpage%2B2%3D",
    );
    for (const [url] of calls) {
      expect(url).toContain("environment_id=env%2Fprod");
      expect(url).toContain("limit=100");
    }
  });
});

describe("human service creation", () => {
  it("prefers the human product credential and preserves idempotency", async () => {
    setHumanSessionToken("midn_user_service-writer");
    setOperatorRecoveryToken("operator-recovery");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          id: "svc_1",
          project_id: "prj_1",
          slug: "api",
          display_name: "API",
        }),
        {
          status: 201,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    await api.createService(
      {
        project_id: "prj_1",
        slug: "api",
        display_name: "API",
      },
      "idem-service-1",
    );

    const init = vi.mocked(fetch).mock.calls[0]?.[1];
    const headers = init?.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer midn_user_service-writer");
    expect(headers["Idempotency-Key"]).toBe("idem-service-1");
  });
});

describe("GitHub App setup transport", () => {
  it("starts an empty-body workspace setup with only the human session", async () => {
    setHumanSessionToken("midn_user_workspace-admin");
    setOperatorRecoveryToken("operator-recovery");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          install_url:
            "https://github.com/apps/midnight/installations/new?state=signed",
          expires_at: "2026-08-30T12:10:00Z",
        }),
        { status: 201, headers: { "content-type": "application/json" } },
      ),
    );

    await api.createGithubSetup("wrk/one");

    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    const headers = init?.headers as Record<string, string>;
    expect(url).toBe(`${API_PREFIX}/workspaces/wrk%2Fone/github/setup`);
    expect(init?.method).toBe("POST");
    expect(init?.body).toBeUndefined();
    expect(headers.Authorization).toBe("Bearer midn_user_workspace-admin");
    expect(JSON.stringify(init)).not.toContain("operator-recovery");
  });
});

describe("environment changeset transport", () => {
  it("binds a durable draft to human authority, CAS, and idempotency", async () => {
    setHumanSessionToken("midn_user_changeset-writer");
    setOperatorRecoveryToken("operator-recovery");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          changeset: {
            id: "chg_1",
            workspace_id: "wrk_1",
            project_id: "prj_1",
            environment_id: "env/prod",
            created_by: "usr_1",
            state: "draft",
            revision: 1,
            items: [],
            created_at: "2026-08-13T00:00:00Z",
            updated_at: "2026-08-13T00:00:00Z",
          },
          validation: { valid: true, affected_services: [], errors: [], warnings: [] },
          capabilities: [],
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await api.putEnvironmentChangeset(
      "env/prod",
      0,
      [{
        kind: "variable",
        operation: "add",
        service_id: "svc/api",
        name: "DATABASE_URL",
        scope: "secret",
        value: "must-not-appear-in-response",
      }],
      "Rotate database credentials",
      "idem-changeset-1",
    );

    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    const headers = init?.headers as Record<string, string>;
    expect(url).toBe(`${API_PREFIX}/environments/env%2Fprod/changeset`);
    expect(init?.method).toBe("PUT");
    expect(headers.Authorization).toBe("Bearer midn_user_changeset-writer");
    expect(headers["If-Match"]).toBe('"0"');
    expect(headers["Idempotency-Key"]).toBe("idem-changeset-1");
    expect(JSON.parse(String(init?.body))).toEqual({
      expected_revision: 0,
      note: "Rotate database credentials",
      changes: [{
        kind: "variable",
        operation: "add",
        service_id: "svc/api",
        name: "DATABASE_URL",
        scope: "secret",
        value: "must-not-appear-in-response",
      }],
    });
  });

  it("binds apply to the same revision in its header and body", async () => {
    setHumanSessionToken("midn_user_changeset-writer");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          changeset: null,
          validation: { valid: true, affected_services: [], errors: [], warnings: [] },
          capabilities: [],
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await api.applyEnvironmentChangeset("env_1", 7, "idem-apply-7");

    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    const headers = init?.headers as Record<string, string>;
    expect(url).toBe(`${API_PREFIX}/environments/env_1/changeset/apply`);
    expect(init?.method).toBe("POST");
    expect(headers.Authorization).toBe("Bearer midn_user_changeset-writer");
    expect(headers["If-Match"]).toBe('"7"');
    expect(headers["Idempotency-Key"]).toBe("idem-apply-7");
    expect(JSON.parse(String(init?.body))).toEqual({ expected_revision: 7 });
  });

  it("starts one idempotent apply-and-deploy operation for the reviewed revision", async () => {
    setHumanSessionToken("midn_user_changeset-writer");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          applied_revision: 8,
          operation_group_id: "opg_8",
          affected_services: ["svc_1"],
          changeset: null,
          validation: { valid: true, affected_services: ["svc_1"], errors: [], warnings: [] },
          capabilities: [],
          operation_group: {
            id: "opg_8",
            environment_id: "env/prod",
            changeset_id: "chg_8",
            configuration_revision: 8,
            state: "queued",
            items: [{ service_id: "svc_1", resource_kinds: ["runtime"], requires_deploy: true, deployment_action: "redeploy", intake_state: "queued" }],
            created_at: "2026-09-01T00:00:00Z",
          },
        }),
        { status: 202, headers: { "content-type": "application/json" } },
      ),
    );

    const result = await api.applyAndDeployEnvironmentChangeset(
      "env/prod",
      8,
      "idem-apply-deploy-8",
    );

    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    const headers = init?.headers as Record<string, string>;
    expect(url).toBe(`${API_PREFIX}/environments/env%2Fprod/changeset/apply-and-deploy`);
    expect(init?.method).toBe("POST");
    expect(headers.Authorization).toBe("Bearer midn_user_changeset-writer");
    expect(headers["If-Match"]).toBe('"8"');
    expect(headers["Idempotency-Key"]).toBe("idem-apply-deploy-8");
    expect(JSON.parse(String(init?.body))).toEqual({ expected_revision: 8 });
    expect(result.operation_group?.items[0]?.intake_state).toBe("queued");
  });

  it("reads operation-group progress through the same tenant scope", async () => {
    setHumanSessionToken("midn_user_changeset-reader");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          id: "opg/8",
          environment_id: "env/prod",
          changeset_id: "chg_8",
          configuration_revision: 8,
          state: "running",
          items: [],
          created_at: "2026-09-01T00:00:00Z",
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await api.getEnvironmentOperationGroup("env/prod", "opg/8");

    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    const headers = init?.headers as Record<string, string>;
    expect(url).toBe(
      `${API_PREFIX}/environments/env%2Fprod/operation-groups/opg%2F8`,
    );
    expect(init?.method).toBe("GET");
    expect(headers.Authorization).toBe("Bearer midn_user_changeset-reader");
  });
});
