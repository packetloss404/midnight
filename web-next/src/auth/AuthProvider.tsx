import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  api,
  type IdentityLoginRequest,
  type IdentityLoginResult,
  type IdentityMe,
} from "@/api/client";
import {
  CREDENTIALS_CHANGED_EVENT,
  getHumanSessionToken,
  getOperatorRecoveryToken,
  isHumanSessionToken,
  setHumanSessionToken,
  setOperatorRecoveryToken,
  type CredentialChangeDetail,
} from "@/api/credentials";

export type AuthState =
  | { status: "checking" }
  | { status: "anonymous" }
  | { status: "developer" }
  | { status: "operator" }
  | { status: "human"; me: IdentityMe }
  | { status: "unavailable"; error: Error };

type AuthContextValue = {
  state: AuthState;
  login: (request: IdentityLoginRequest) => Promise<IdentityLoginResult>;
  logoutHuman: () => Promise<boolean>;
  logoutHumanLocally: () => Promise<boolean>;
  enterOperatorRecovery: (token: string) => Promise<void>;
  clearOperatorRecovery: () => Promise<boolean>;
  retryBootstrap: () => Promise<void>;
  hasOperatorRecovery: boolean;
  operatorFeaturesAvailable: boolean;
  sessionActionError?: Error;
};

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [state, setState] = useState<AuthState>({ status: "checking" });
  const [operatorPresent, setOperatorPresent] = useState(() =>
    false,
  );
  const [sessionActionError, setSessionActionError] = useState<Error>();
  const revision = useRef(0);
  const stateRef = useRef<AuthState>(state);

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  const resolveNonHumanMode = useCallback(async () => {
    const currentRevision = ++revision.current;
    setState({ status: "checking" });
    setOperatorPresent(false);
    const operatorToken = getOperatorRecoveryToken();
    if (operatorToken) {
      try {
        await api.verifyOperatorRecoveryToken(operatorToken);
        if (revision.current === currentRevision) {
          setOperatorPresent(true);
          setState({ status: "operator" });
        }
        return;
      } catch (error) {
        if (revision.current !== currentRevision) return;
        if (error instanceof ApiError && error.status === 401) {
          if (!setOperatorRecoveryToken()) {
            setState({
              status: "unavailable",
              error: credentialStorageError("clear the rejected recovery token"),
            });
            return;
          }
        } else {
          setState({
            status: "unavailable",
            error:
              error instanceof Error
                ? error
                : new Error("Operator verification unavailable"),
          });
          return;
        }
      }
    }

    try {
      // Memory/local-development stacks intentionally allow tokenless product
      // access. Probe a tenant-safe read so that production still fails closed
      // while local acceptance flows remain usable without inventing a token.
      await api.listProjects();
      if (revision.current === currentRevision) {
        setState({ status: "developer" });
      }
    } catch (error) {
      if (revision.current !== currentRevision) return;
      if (error instanceof ApiError && error.status === 401) {
        setState({ status: "anonymous" });
        return;
      }
      setState({
        status: "unavailable",
        error: error instanceof Error ? error : new Error("Control plane unavailable"),
      });
    }
  }, []);

  const bootstrap = useCallback(async () => {
    const token = getHumanSessionToken();
    const currentRevision = ++revision.current;
    if (!token) {
      await resolveNonHumanMode();
      return;
    }

    setState({ status: "checking" });
    setOperatorPresent(false);
    try {
      const me = await api.getIdentityMe();
      if (revision.current !== currentRevision) return;

      setState({ status: "human", me });
    } catch (error) {
      if (revision.current !== currentRevision) return;
      if (error instanceof ApiError && error.status === 401) {
        if (!setHumanSessionToken()) {
          setState({
            status: "unavailable",
            error: credentialStorageError("clear the invalid human session"),
          });
          return;
        }
        await resolveNonHumanMode();
        return;
      }
      setState({
        status: "unavailable",
        error: error instanceof Error ? error : new Error("Identity service unavailable"),
      });
    }
  }, [resolveNonHumanMode]);

  useEffect(() => {
    void bootstrap();
  }, [bootstrap]);

  useEffect(() => {
    const onCredentialChange = (event: Event) => {
      const detail = (event as CustomEvent<CredentialChangeDetail>).detail;
      if (detail?.kind !== "operator") return;
      setOperatorPresent(false);
      if (stateRef.current.status === "checking") {
        return;
      }
      if (stateRef.current.status === "human") {
        void bootstrap();
      } else {
        void resolveNonHumanMode();
      }
    };
    window.addEventListener(CREDENTIALS_CHANGED_EVENT, onCredentialChange);
    return () =>
      window.removeEventListener(CREDENTIALS_CHANGED_EVENT, onCredentialChange);
  }, [bootstrap, resolveNonHumanMode]);

  const clearCachedData = useCallback(async () => {
    await queryClient.cancelQueries();
    queryClient.clear();
  }, [queryClient]);

  const login = useCallback(
    async (request: IdentityLoginRequest) => {
      const result = await api.createIdentitySession(request);
      if (
        result.token_type !== "Bearer" ||
        !isHumanSessionToken(result.access_token) ||
        !result.user.id.startsWith("usr_") ||
        !result.session.id.startsWith("ses_") ||
        result.session.user_id !== result.user.id
      ) {
        throw new Error("The identity service returned an invalid session.");
      }
      ++revision.current;
      if (!setHumanSessionToken(result.access_token)) {
        throw credentialStorageError("save the human session");
      }
      // Product queries may have been populated with developer or recovery
      // authority. Never carry those authority-neutral cache entries into a
      // newly authenticated human session.
      await clearCachedData();
      setSessionActionError(undefined);
      setState({
        status: "human",
        me: {
          principal: {
            kind: "user",
            user_id: result.user.id,
            session_id: result.session.id,
            auth_method: result.user.mfa_enabled ? "password+totp" : "password",
            email: result.user.email,
            display_name: result.user.display_name,
            instance_admin: result.user.instance_admin,
          },
          session: result.session,
        },
      });
      return result;
    },
    [clearCachedData],
  );

  const finishHumanLogout = useCallback(async (): Promise<boolean> => {
    if (!setHumanSessionToken()) {
      setSessionActionError(
        credentialStorageError("remove the human session from this tab"),
      );
      return false;
    }
    ++revision.current;
    setSessionActionError(undefined);
    await clearCachedData();
    await resolveNonHumanMode();
    return true;
  }, [clearCachedData, resolveNonHumanMode]);

  const enterOperatorRecovery = useCallback(
    async (token: string) => {
      const candidate = token.trim();
      await api.verifyOperatorRecoveryToken(candidate);
      if (!setOperatorRecoveryToken(candidate)) {
        throw credentialStorageError("save the operator recovery token");
      }
      ++revision.current;
      setOperatorPresent(true);
      setSessionActionError(undefined);
      if (stateRef.current.status !== "human") {
        setState({ status: "operator" });
      }
      void clearCachedData();
    },
    [clearCachedData],
  );

  const logoutHuman = useCallback(async (): Promise<boolean> => {
    const currentSessionId =
      state.status === "human" ? state.me.session.id : undefined;
    if (!currentSessionId || !getHumanSessionToken()) {
      return finishHumanLogout();
    }
    setSessionActionError(undefined);
    try {
      await api.revokeIdentitySession(currentSessionId);
    } catch (error) {
      if (
        !(error instanceof ApiError) ||
        (error.status !== 401 && error.status !== 404)
      ) {
        setSessionActionError(
          error instanceof Error
            ? error
            : new Error("The session could not be revoked."),
        );
        return false;
      }
    }
    return finishHumanLogout();
  }, [finishHumanLogout, state]);

  const clearOperatorRecovery = useCallback(async (): Promise<boolean> => {
    if (!setOperatorRecoveryToken()) {
      setSessionActionError(
        credentialStorageError("remove the operator recovery token"),
      );
      return false;
    }
    setOperatorPresent(false);
    setSessionActionError(undefined);
    await clearCachedData();
    if (getHumanSessionToken()) {
      await bootstrap();
    } else {
      await resolveNonHumanMode();
    }
    return true;
  }, [bootstrap, clearCachedData, resolveNonHumanMode]);

  const value = useMemo<AuthContextValue>(
    () => ({
      state,
      login,
      logoutHuman,
      logoutHumanLocally: finishHumanLogout,
      enterOperatorRecovery,
      clearOperatorRecovery,
      retryBootstrap: bootstrap,
      hasOperatorRecovery: operatorPresent,
      operatorFeaturesAvailable:
        state.status === "developer" ||
        state.status === "operator" ||
        (state.status === "human" && state.me.principal.instance_admin),
      sessionActionError,
    }),
    [
      bootstrap,
      clearOperatorRecovery,
      enterOperatorRecovery,
      finishHumanLogout,
      login,
      logoutHuman,
      operatorPresent,
      sessionActionError,
      state,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}

function credentialStorageError(action: string): Error {
  return new Error(
    `Midnight could not ${action} because browser session storage is unavailable. Check browser privacy settings and retry.`,
  );
}
