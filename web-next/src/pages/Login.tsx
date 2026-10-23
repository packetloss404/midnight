import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { api, ApiError } from "@/api/client";
import { isHumanSessionToken } from "@/api/credentials";
import { useAuth } from "@/auth/AuthProvider";
import { Button, Field, TextInput } from "@/components/panels/form-kit";
import { presentError } from "@/lib/errorPresentation";
import { AdministratorPasswordReset } from "./AdministratorPasswordReset";

type LoginSearch = { redirect?: string };

export function Login() {
  const auth = useAuth();
  const navigate = useNavigate();
  const search = useRouterState({
    select: (routerState) => routerState.location.search as LoginSearch,
  });
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [totpCode, setTotpCode] = useState("");
  const [mfaRequired, setMFARequired] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [retryAfter, setRetryAfter] = useState(0);
  const [operatorToken, setOperatorToken] = useState("");
  const [operatorError, setOperatorError] = useState<string | null>(null);
  const [operatorBusy, setOperatorBusy] = useState(false);

  useEffect(() => {
    if (retryAfter <= 0) return;
    const timer = window.setInterval(
      () => setRetryAfter((value) => Math.max(0, value - 1)),
      1_000,
    );
    return () => window.clearInterval(timer);
  }, [retryAfter]);

  async function submitHuman(event: FormEvent) {
    event.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await auth.login({
        email,
        password,
        ...(totpCode.trim() ? { totp_code: totpCode.trim() } : {}),
        client_kind: "dashboard",
      });
      setPassword("");
      setTotpCode("");
      await navigateToRequestedPage();
    } catch (caught) {
      if (caught instanceof ApiError && caught.code === "MFA_REQUIRED") {
        setMFARequired(true);
        setTotpCode("");
      }
      if (caught instanceof ApiError && caught.status === 429) {
        setRetryAfter(caught.retryAfterSeconds ?? 60);
      }
      setError(caught);
    } finally {
      setBusy(false);
    }
  }

  async function submitOperator(event: FormEvent) {
    event.preventDefault();
    const token = operatorToken.trim();
    setOperatorError(null);
    if (!token) {
      setOperatorError("Enter the recovery token created on the Midnight host.");
      return;
    }
    if (isHumanSessionToken(token)) {
      setOperatorError("Use the account form above for a human session token.");
      return;
    }
    setOperatorBusy(true);
    try {
      await auth.enterOperatorRecovery(token);
      setOperatorToken("");
      // Recovery access can administer the server but cannot own a workspace
      // or link a Git source. On an installation that has never been claimed,
      // send the operator to first-run setup instead of into an empty console.
      if (await installationNeedsSetup()) {
        await navigateTo("/setup");
        return;
      }
      await navigateToRequestedPage();
    } catch (caught) {
      setOperatorError(presentError(caught).message);
    } finally {
      setOperatorBusy(false);
    }
  }

  async function installationNeedsSetup(): Promise<boolean> {
    try {
      const status = await api.getIdentityClaimStatus();
      return !status.claimed;
    } catch {
      // A server too old or too broken to answer the probe should not block
      // recovery access; fall through to the normal destination.
      return false;
    }
  }

  async function navigateTo(destinationPath: string) {
    try {
      await navigate({ to: destinationPath, replace: true } as never);
      if (window.location.pathname !== destinationPath) {
        window.location.replace(destinationPath);
      }
    } catch {
      window.location.replace(destinationPath);
    }
  }

  async function navigateToRequestedPage() {
    const redirect = safeInternalRedirect(search.redirect);
    const destination = new URL(redirect, window.location.origin);
    try {
      await navigate({
        to: destination.pathname,
        search: Object.fromEntries(destination.searchParams),
        hash: destination.hash.slice(1),
        replace: true,
      } as never);
      if (window.location.pathname === "/login") {
        window.location.replace(redirect);
      }
    } catch {
      // A hand-declared route tree can reject a dynamic destination that is
      // nevertheless a verified same-origin path. The saved session survives
      // a hard navigation and is re-verified through /auth/me on bootstrap.
      window.location.replace(redirect);
    }
  }

  const errorMessage = error ? presentError(error).message : null;
  const signedInHuman = auth.state.status === "human";
  const signedInOperator = auth.state.status === "operator";
  const signedInLabel =
    auth.state.status === "human"
      ? auth.state.me.principal.display_name || auth.state.me.principal.email
      : undefined;

  return (
    <main className="grid min-h-[100dvh] place-items-center px-4 py-10">
      <section className="grid w-full max-w-md gap-5 rounded-xl border border-line bg-surface p-6 shadow-lg">
        <header className="grid gap-2">
          <div className="grid h-10 w-10 place-items-center rounded-lg bg-accent-soft font-mono font-bold text-accent">
            M
          </div>
          <div>
            <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
              Midnight Console
            </p>
            <h1 className="m-0 mt-1 text-2xl font-semibold tracking-tight">
              Sign in
            </h1>
            <p className="m-0 mt-1 text-sm text-muted">
              Use your Midnight account. Sessions are scoped to your workspace
              access and expire automatically.
            </p>
          </div>
        </header>

        {signedInHuman || signedInOperator ? (
          <div className="grid gap-3 rounded-lg border border-line bg-surface-2 p-4">
            <p className="m-0 text-sm text-text">
              {signedInHuman
                ? `Signed in as ${signedInLabel}.`
                : "Operator recovery access is configured for this tab."}
            </p>
            <Button onClick={() => void navigateToRequestedPage()}>
              Continue to console
            </Button>
          </div>
        ) : (
          <form className="grid gap-4" onSubmit={submitHuman}>
            <Field label="Email" htmlFor="login-email">
              <TextInput
                id="login-email"
                type="email"
                autoComplete="username"
                value={email}
                onChange={(event) => {
                  setEmail(event.target.value);
                  setMFARequired(false);
                  setTotpCode("");
                  setError(null);
                }}
                required
                autoFocus
              />
            </Field>
            <Field label="Password" htmlFor="login-password">
              <TextInput
                id="login-password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => {
                  setPassword(event.target.value);
                  setMFARequired(false);
                  setError(null);
                }}
                required
              />
            </Field>
            <Field
              label="Authenticator code (if enabled)"
              hint={
                mfaRequired
                  ? "MFA is enabled for this account. Enter the current six-digit code."
                  : "Enter the current six-digit code if your account uses MFA; otherwise leave this blank."
              }
              htmlFor="login-totp"
            >
              <TextInput
                id="login-totp"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                value={totpCode}
                onChange={(event) => {
                  setTotpCode(event.target.value.replace(/\D/g, "").slice(0, 6));
                  setError(null);
                }}
                required={mfaRequired}
              />
            </Field>
            {errorMessage ? (
              <p role="alert" className="m-0 text-xs text-danger">
                {errorMessage}
              </p>
            ) : null}
            <Button
              type="submit"
              disabled={
                busy ||
                retryAfter > 0 ||
                !email.trim() ||
                !password ||
                ((mfaRequired || totpCode.length > 0) && totpCode.length !== 6)
              }
            >
              {busy
                ? "Signing in…"
                : retryAfter > 0
                  ? `Try again in ${retryAfter}s`
                  : mfaRequired
                    ? "Verify and sign in"
                    : "Sign in"}
            </Button>
          </form>
        )}

        <details className="border-t border-line pt-4">
          <summary className="cursor-pointer text-xs font-semibold text-muted">
            Emergency recovery
          </summary>
          <div className="mt-3 grid gap-3">
            <p className="m-0 text-xs text-muted">
              Use this break-glass credential only when administrator sign-in
              is unavailable. Normal server administration uses your Midnight
              account and is kept in the same console.
            </p>
            <form className="grid gap-2" onSubmit={submitOperator}>
              <label className="sr-only" htmlFor="operator-recovery-token">
                Operator recovery token
              </label>
              <TextInput
                id="operator-recovery-token"
                type="password"
                autoComplete="off"
                spellCheck={false}
                value={operatorToken}
                onChange={(event) => {
                  setOperatorToken(event.target.value);
                  setOperatorError(null);
                }}
                placeholder="Paste operator recovery token"
              />
              {operatorError ? (
                <p role="alert" className="m-0 text-xs text-danger">
                  {operatorError}
                </p>
              ) : null}
              <Button
                type="submit"
                variant="secondary"
                disabled={operatorBusy || !operatorToken.trim()}
              >
                {operatorBusy ? "Verifying…" : "Verify and enter recovery mode"}
              </Button>
            </form>

            <details className="border-t border-line pt-3">
              <summary className="cursor-pointer text-xs font-semibold text-muted">
                Reset an administrator password
              </summary>
              <div className="mt-3 grid gap-3">
                <p className="m-0 text-xs text-muted">
                  Use this when an administrator password is lost. First-run
                  setup is one-shot and cannot be repeated, so this is the
                  supported way back in. The reset signs out every active
                  session for that account, and enrolls a replacement
                  authenticator unless you keep the existing one.
                </p>
                <AdministratorPasswordReset fallbackToken={operatorToken} />
              </div>
            </details>
          </div>
        </details>
      </section>
    </main>
  );
}

export function safeInternalRedirect(value?: string): string {
  if (!value || !value.startsWith("/") || value.startsWith("//")) return "/";
  try {
    const url = new URL(value, window.location.origin);
    if (url.origin !== window.location.origin) return "/";
    return `${url.pathname}${url.search}${url.hash}`;
  } catch {
    return "/";
  }
}
