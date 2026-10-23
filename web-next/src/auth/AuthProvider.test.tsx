import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import {
  getHumanSessionToken,
  getOperatorRecoveryToken,
  setHumanSessionToken,
  setOperatorRecoveryToken,
} from "@/api/credentials";
import { AuthProvider, useAuth } from "./AuthProvider";

const ME = {
  principal: {
    kind: "user" as const,
    user_id: "usr_1",
    session_id: "ses_1",
    auth_method: "password" as const,
    email: "person@example.com",
    display_name: "Person",
    instance_admin: false,
  },
  session: {
    id: "ses_1",
    user_id: "usr_1",
    token_prefix: "midn_user_t",
    client_kind: "dashboard" as const,
    created_at: "2026-01-01T00:00:00Z",
    last_seen_at: "2026-01-01T00:00:00Z",
    expires_at: "2026-01-02T00:00:00Z",
    idle_expires_at: "2026-01-01T08:00:00Z",
    current: true,
  },
};

afterEach(() => {
  window.sessionStorage.clear();
  vi.restoreAllMocks();
});

function renderProvider() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const view = render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    </QueryClientProvider>,
  );
  return { ...view, queryClient };
}

function AuthProbe() {
  const auth = useAuth();
  const [caught, setCaught] = useState<Error>();
  return (
    <div>
      <span data-testid="auth-status">{auth.state.status}</span>
      <span data-testid="operator-features">
        {auth.operatorFeaturesAvailable ? "enabled" : "disabled"}
      </span>
      {auth.state.status === "human" ? (
        <span>{auth.state.me.principal.email}</span>
      ) : null}
      {auth.sessionActionError ? (
        <span data-testid="session-action-error">
          {auth.sessionActionError.message}
        </span>
      ) : null}
      {caught ? <span data-testid="caught-error">{caught.message}</span> : null}
      <button
        type="button"
        onClick={() =>
          void auth
            .login({
              email: "person@example.com",
              password: "secret",
            })
            .catch(setCaught)
        }
      >
        Login
      </button>
      <button type="button" onClick={() => void auth.logoutHuman()}>
        Logout
      </button>
      <button type="button" onClick={() => void auth.logoutHumanLocally()}>
        Logout locally
      </button>
      <button
        type="button"
        onClick={() =>
          void auth.enterOperatorRecovery("operator-recovery").catch(setCaught)
        }
      >
        Enter operator
      </button>
    </div>
  );
}

describe("AuthProvider", () => {
  it("bootstraps a saved human session through /auth/me", async () => {
    setHumanSessionToken("midn_user_token");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(JSON.stringify(ME), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );

    renderProvider();

    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    const headers = vi.mocked(fetch).mock.calls[0]?.[1]?.headers as Record<
      string,
      string
    >;
    expect(headers.Authorization).toBe("Bearer midn_user_token");
  });

  it("enables installation settings for an administrator human session", async () => {
    setHumanSessionToken("midn_user_admin");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ...ME,
          principal: { ...ME.principal, instance_admin: true },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    renderProvider();

    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    expect(screen.getByTestId("operator-features")).toHaveTextContent("enabled");
  });

  it("clears an invalid saved session without promoting it to operator access", async () => {
    setHumanSessionToken("midn_user_invalid");
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      new Response(
        JSON.stringify({
          error: { code: "AUTH_SESSION_INVALID", message: "Session invalid" },
        }),
        { status: 401, headers: { "content-type": "application/json" } },
      ),
    );

    renderProvider();

    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous"),
    );
    expect(getHumanSessionToken()).toBeUndefined();
  });

  it.each([501, 503])(
    "retains a saved session when identity bootstrap returns %s",
    async (status) => {
      setHumanSessionToken("midn_user_retry");
      vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            error: {
              code: status === 501 ? "NOT_IMPLEMENTED" : "INTERNAL_ERROR",
              message: "Unavailable",
            },
          }),
          { status, headers: { "content-type": "application/json" } },
        ),
      );

      renderProvider();

      await waitFor(() =>
        expect(screen.getByTestId("auth-status")).toHaveTextContent(
          "unavailable",
        ),
      );
      expect(getHumanSessionToken()).toBe("midn_user_retry");
    },
  );

  it("preserves the human session on revoke 5xx until local-only logout is explicit", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(async (input, init) => {
        const path = new URL(String(input), window.location.origin).pathname;
        if (path.endsWith("/projects")) {
          return new Response(
            JSON.stringify({
              error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
            }),
            { status: 401 },
          );
        }
        if (path.endsWith("/auth/sessions") && init?.method === "POST") {
          return new Response(
            JSON.stringify({
              access_token: "midn_user_fresh",
              token_type: "Bearer",
              expires_in: 86400,
              user: {
                id: "usr_1",
                email: "person@example.com",
                display_name: "Person",
                mfa_enabled: false,
              },
              session: ME.session,
            }),
            { status: 201 },
          );
        }
        if (path.endsWith("/auth/sessions/ses_1")) {
          return new Response(
            JSON.stringify({
              error: { code: "INTERNAL_ERROR", message: "Unavailable" },
            }),
            { status: 503 },
          );
        }
        return new Response("", { status: 500 });
      });

    renderProvider();
    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous"),
    );
    fireEvent.click(screen.getByRole("button", { name: "Login" }));
    expect(await screen.findByText("person@example.com")).toBeInTheDocument();

    const loginCall = fetchMock.mock.calls.find(
      ([input, init]) =>
        String(input).endsWith("/auth/sessions") && init?.method === "POST",
    );
    const loginHeaders = loginCall?.[1]?.headers as Record<string, string>;
    expect(loginHeaders.Authorization).toBeUndefined();
    expect(getHumanSessionToken()).toBe("midn_user_fresh");

    fireEvent.click(screen.getByRole("button", { name: "Logout" }));
    await screen.findByTestId("session-action-error");
    expect(getHumanSessionToken()).toBe("midn_user_fresh");
    expect(screen.getByTestId("auth-status")).toHaveTextContent("human");

    fireEvent.click(screen.getByRole("button", { name: "Logout locally" }));
    await waitFor(() => expect(getHumanSessionToken()).toBeUndefined());
    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous"),
    );
  });

  it("clears product data cached under non-human authority before completing human login", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = new URL(String(input), window.location.origin).pathname;
      if (path.endsWith("/auth/sessions") && init?.method === "POST") {
        return new Response(
          JSON.stringify({
            access_token: "midn_user_fresh",
            token_type: "Bearer",
            expires_in: 86400,
            user: {
              id: "usr_1",
              email: "person@example.com",
              display_name: "Person",
              mfa_enabled: false,
            },
            session: ME.session,
          }),
          { status: 201 },
        );
      }
      return new Response(
        JSON.stringify({
          error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
        }),
        { status: 401 },
      );
    });

    const { queryClient } = renderProvider();
    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous"),
    );
    queryClient.setQueryData(["environments", "prj_recovery"], [
      { id: "env_recovery_only" },
    ]);

    fireEvent.click(screen.getByRole("button", { name: "Login" }));
    expect(await screen.findByText("person@example.com")).toBeInTheDocument();

    expect(
      queryClient.getQueryData(["environments", "prj_recovery"]),
    ).toBeUndefined();
  });

  it.each([204, 401, 404])(
    "treats revoke status %s as a completed logout",
    async (status) => {
      setHumanSessionToken("midn_user_existing");
      vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
        const path = new URL(String(input), window.location.origin).pathname;
        if (path.endsWith("/auth/me")) {
          return new Response(JSON.stringify(ME), { status: 200 });
        }
        if (
          path.endsWith("/auth/sessions/ses_1") &&
          init?.method === "DELETE"
        ) {
          if (status === 204) return new Response(null, { status });
          return new Response(
            JSON.stringify({
              error: {
                code:
                  status === 401
                    ? "AUTH_SESSION_INVALID"
                    : "RESOURCE_NOT_FOUND",
                message: "Session no longer exists",
              },
            }),
            { status },
          );
        }
        return new Response(
          JSON.stringify({
            error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
          }),
          { status: 401 },
        );
      });

      renderProvider();
      expect(await screen.findByText("person@example.com")).toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "Logout" }));

      await waitFor(() =>
        expect(screen.getByTestId("auth-status")).toHaveTextContent(
          "anonymous",
        ),
      );
      expect(getHumanSessionToken()).toBeUndefined();
    },
  );

  it("does not claim logout when the revoked token cannot be removed from storage", async () => {
    setHumanSessionToken("midn_user_existing");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = new URL(String(input), window.location.origin).pathname;
      if (path.endsWith("/auth/me")) {
        return new Response(JSON.stringify(ME), { status: 200 });
      }
      if (
        path.endsWith("/auth/sessions/ses_1") &&
        init?.method === "DELETE"
      ) {
        return new Response(null, { status: 204 });
      }
      return new Response("", { status: 500 });
    });

    renderProvider();
    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    const restoreStorage = installThrowingSessionStorage("removeItem");
    fireEvent.click(screen.getByRole("button", { name: "Logout" }));

    await screen.findByTestId("session-action-error");
    expect(screen.getByTestId("auth-status")).toHaveTextContent("human");
    expect(getHumanSessionToken()).toBe("midn_user_existing");
    restoreStorage();
  });

  it("preserves the human session when revoke fails before receiving a response", async () => {
    setHumanSessionToken("midn_user_existing");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = new URL(String(input), window.location.origin).pathname;
      if (path.endsWith("/auth/me")) {
        return new Response(JSON.stringify(ME), { status: 200 });
      }
      if (
        path.endsWith("/auth/sessions/ses_1") &&
        init?.method === "DELETE"
      ) {
        throw new TypeError("network unavailable");
      }
      return new Response("", { status: 500 });
    });

    renderProvider();
    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Logout" }));

    await screen.findByTestId("session-action-error");
    expect(screen.getByTestId("auth-status")).toHaveTextContent("human");
    expect(getHumanSessionToken()).toBe("midn_user_existing");
  });

  it("verifies a stored operator token before enabling recovery features", async () => {
    setOperatorRecoveryToken("stored-operator");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(JSON.stringify({ status: "ready" }), { status: 200 }),
    );

    renderProvider();

    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("operator"),
    );
    expect(screen.getByTestId("operator-features")).toHaveTextContent("enabled");
    const headers = vi.mocked(fetch).mock.calls[0]?.[1]?.headers as Record<
      string,
      string
    >;
    expect(headers.Authorization).toBe("Bearer stored-operator");
  });

  it("preserves a stored operator token but keeps features disabled on probe 5xx", async () => {
    setOperatorRecoveryToken("stored-operator");
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: { code: "INTERNAL_ERROR", message: "Unavailable" },
        }),
        { status: 503 },
      ),
    );

    renderProvider();

    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("unavailable"),
    );
    expect(getOperatorRecoveryToken()).toBe("stored-operator");
    expect(screen.getByTestId("operator-features")).toHaveTextContent("disabled");
  });

  it("clears a rejected stored operator token and remains anonymous", async () => {
    setOperatorRecoveryToken("rejected-operator");
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      new Response(
        JSON.stringify({
          error: { code: "AUTH_TOKEN_HEADER_INVALID", message: "Invalid" },
        }),
        { status: 401 },
      ),
    );

    renderProvider();

    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous"),
    );
    expect(getOperatorRecoveryToken()).toBeUndefined();
    expect(screen.getByTestId("operator-features")).toHaveTextContent("disabled");
  });

  it("does not claim human auth when session persistence fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = new URL(String(input), window.location.origin).pathname;
      if (path.endsWith("/auth/sessions") && init?.method === "POST") {
        return new Response(
          JSON.stringify({
            access_token: "midn_user_fresh",
            token_type: "Bearer",
            expires_in: 86400,
            user: {
              id: "usr_1",
              email: "person@example.com",
              display_name: "Person",
              mfa_enabled: false,
            },
            session: ME.session,
          }),
          { status: 201 },
        );
      }
      return new Response(
        JSON.stringify({
          error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
        }),
        { status: 401 },
      );
    });

    renderProvider();
    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous"),
    );
    const restoreStorage = installThrowingSessionStorage("setItem");
    fireEvent.click(screen.getByRole("button", { name: "Login" }));

    await screen.findByTestId("caught-error");
    expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous");
    expect(getHumanSessionToken()).toBeUndefined();
    restoreStorage();
  });

  it("does not claim operator auth when recovery-token persistence fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const path = new URL(String(input), window.location.origin).pathname;
      if (path.endsWith("/dashboard/summary")) {
        return new Response(JSON.stringify({ status: "ready" }), {
          status: 200,
        });
      }
      return new Response(
        JSON.stringify({
          error: { code: "AUTH_REQUIRED_MISSING", message: "Sign in" },
        }),
        { status: 401 },
      );
    });

    renderProvider();
    await waitFor(() =>
      expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous"),
    );
    const restoreStorage = installThrowingSessionStorage("setItem");
    fireEvent.click(screen.getByRole("button", { name: "Enter operator" }));

    await screen.findByTestId("caught-error");
    expect(screen.getByTestId("auth-status")).toHaveTextContent("anonymous");
    expect(screen.getByTestId("operator-features")).toHaveTextContent("disabled");
    expect(getOperatorRecoveryToken()).toBeUndefined();
    restoreStorage();
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
