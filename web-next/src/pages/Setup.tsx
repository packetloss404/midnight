import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import { getOperatorRecoveryToken } from "@/api/credentials";
import { useAuth } from "@/auth/AuthProvider";
import { Button, Field, TextInput } from "@/components/panels/form-kit";
import { presentError } from "@/lib/errorPresentation";
import {
  MIN_ADMIN_PASSWORD_LENGTH,
  generateTotpSecret,
  isValidEmail,
  isValidWorkspaceSlug,
  otpauthUri,
  verifyTotpCode,
  workspaceSlugFrom,
} from "./SetupCrypto";

type SetupStage = "checking" | "account" | "enroll" | "claimed" | "no-token";

export function Setup({ onComplete }: { onComplete?: () => void } = {}) {
  const auth = useAuth();
  const [stage, setStage] = useState<SetupStage>("checking");
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [workspaceName, setWorkspaceName] = useState("");
  const [workspaceSlug, setWorkspaceSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [secret, setSecret] = useState("");
  const [totpInput, setTotpInput] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      if (!getOperatorRecoveryToken()) {
        if (!cancelled) setStage("no-token");
        return;
      }
      try {
        const status = await api.getIdentityClaimStatus();
        if (cancelled) return;
        setStage(status.claimed ? "claimed" : "account");
      } catch (caught) {
        if (cancelled) return;
        setError(presentError(caught).message);
        setStage("account");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const effectiveSlug = slugEdited
    ? workspaceSlug
    : workspaceSlugFrom(workspaceName);

  function accountProblem(): string | null {
    if (!isValidEmail(email)) return "Enter a valid email address.";
    if (!displayName.trim()) return "Enter the administrator's name.";
    if (!workspaceName.trim()) return "Enter a workspace name.";
    if (!isValidWorkspaceSlug(effectiveSlug)) {
      return "The workspace slug must be 3-64 lowercase letters, digits, or hyphens, and cannot start or end with a hyphen.";
    }
    if (password.length < MIN_ADMIN_PASSWORD_LENGTH) {
      return `The password must be at least ${MIN_ADMIN_PASSWORD_LENGTH} characters.`;
    }
    if (password !== confirmPassword) return "The passwords do not match.";
    return null;
  }

  function submitAccount(event: FormEvent) {
    event.preventDefault();
    const problem = accountProblem();
    if (problem) {
      setError(problem);
      return;
    }
    setError(null);
    setSecret(generateTotpSecret());
    setTotpInput("");
    setStage("enroll");
  }

  async function submitEnrollment(event: FormEvent) {
    event.preventDefault();
    setError(null);
    const recoveryToken = getOperatorRecoveryToken();
    if (!recoveryToken) {
      setStage("no-token");
      return;
    }
    setBusy(true);
    try {
      if (!(await verifyTotpCode(secret, totpInput))) {
        setError(
          "That code does not match the secret above. Check your authenticator's clock and try the current code.",
        );
        return;
      }
      await api.claimDefaultWorkspace(
        {
          email: email.trim().toLowerCase(),
          display_name: displayName.trim(),
          password,
          totp_secret: secret,
          totp_code: totpInput.trim(),
          workspace_slug: effectiveSlug,
          workspace_display_name: workspaceName.trim(),
        },
        recoveryToken,
      );
      await auth.login({
        email: email.trim().toLowerCase(),
        password,
        totp_code: totpInput.trim(),
        client_kind: "dashboard",
      });
      setPassword("");
      setConfirmPassword("");
      if (onComplete) {
        onComplete();
      } else {
        window.location.replace("/");
      }
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setStage("claimed");
        return;
      }
      setError(presentError(caught).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="grid min-h-[100dvh] place-items-center px-4 py-10">
      <section className="grid w-full max-w-lg gap-5 rounded-xl border border-line bg-surface p-6 shadow-lg">
        <header className="grid gap-1">
          <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
            First-run setup
          </p>
          <h1 className="m-0 text-2xl font-semibold tracking-tight">
            Create your administrator account
          </h1>
          <p className="m-0 text-sm text-muted">
            Recovery access can administer the server, but it cannot own a
            workspace or connect a Git source. Create a real account to finish
            setting up this installation.
          </p>
        </header>

        {stage === "checking" ? (
          <p role="status" className="m-0 text-sm text-muted">
            Checking whether first-run setup is still required…
          </p>
        ) : null}

        {stage === "no-token" ? (
          <p role="alert" className="m-0 text-sm text-danger">
            Paste your operator recovery token on the sign-in page first. Setup
            uses it once to create the first account.
          </p>
        ) : null}

        {stage === "claimed" ? (
          <div className="grid gap-3">
            <p role="status" className="m-0 text-sm text-text">
              This installation already has an administrator. First-user setup
              is one-shot and cannot be run again.
            </p>
            <a
              href="/login"
              className="inline-flex w-fit rounded-[var(--r-sm)] border border-accent bg-accent-soft px-3 py-2 text-xs font-semibold text-accent"
            >
              Go to sign in
            </a>
          </div>
        ) : null}

        {stage === "account" ? (
          <form className="grid gap-4" onSubmit={submitAccount}>
            <Field label="Email" htmlFor="setup-email">
              <TextInput
                id="setup-email"
                type="email"
                autoComplete="username"
                value={email}
                onChange={(event) => {
                  setEmail(event.target.value);
                  setError(null);
                }}
                required
                autoFocus
              />
            </Field>
            <Field label="Your name" htmlFor="setup-display-name">
              <TextInput
                id="setup-display-name"
                autoComplete="name"
                value={displayName}
                onChange={(event) => {
                  setDisplayName(event.target.value);
                  setError(null);
                }}
                required
              />
            </Field>
            <Field label="Workspace name" htmlFor="setup-workspace-name">
              <TextInput
                id="setup-workspace-name"
                value={workspaceName}
                onChange={(event) => {
                  setWorkspaceName(event.target.value);
                  setError(null);
                }}
                required
              />
            </Field>
            <Field
              label="Workspace slug"
              hint="Lowercase letters, digits, and hyphens. Used in URLs and the CLI."
              htmlFor="setup-workspace-slug"
            >
              <TextInput
                id="setup-workspace-slug"
                value={effectiveSlug}
                onChange={(event) => {
                  setSlugEdited(true);
                  setWorkspaceSlug(event.target.value);
                  setError(null);
                }}
                required
              />
            </Field>
            <Field
              label="Password"
              hint={`At least ${MIN_ADMIN_PASSWORD_LENGTH} characters. A passphrase is easier to remember than a short complex password.`}
              htmlFor="setup-password"
            >
              <TextInput
                id="setup-password"
                type="password"
                autoComplete="new-password"
                minLength={MIN_ADMIN_PASSWORD_LENGTH}
                value={password}
                onChange={(event) => {
                  setPassword(event.target.value);
                  setError(null);
                }}
                required
              />
            </Field>
            <Field label="Confirm password" htmlFor="setup-confirm-password">
              <TextInput
                id="setup-confirm-password"
                type="password"
                autoComplete="new-password"
                value={confirmPassword}
                onChange={(event) => {
                  setConfirmPassword(event.target.value);
                  setError(null);
                }}
                required
              />
            </Field>
            {error ? (
              <p role="alert" className="m-0 text-xs text-danger">
                {error}
              </p>
            ) : null}
            <Button type="submit">Continue to authenticator setup</Button>
          </form>
        ) : null}

        {stage === "enroll" ? (
          <form className="grid gap-4" onSubmit={submitEnrollment}>
            <div className="grid gap-2 rounded-lg border border-line bg-surface-2 p-4">
              <p className="m-0 text-sm text-text">
                Add this account to your authenticator app, then enter the
                current six-digit code to finish.
              </p>
              <p className="m-0 text-[11px] font-semibold uppercase tracking-wider text-muted">
                Enrollment URL
              </p>
              <code className="m-0 break-all font-mono text-[11px] text-text">
                {otpauthUri("Midnight", email.trim().toLowerCase(), secret)}
              </code>
              <p className="m-0 text-[11px] font-semibold uppercase tracking-wider text-muted">
                Secret (Base32)
              </p>
              <code className="m-0 break-all font-mono text-xs text-text">
                {secret}
              </code>
              <p className="m-0 text-xs text-danger">
                Save this now. It is not stored in plaintext and is not shown
                again.
              </p>
            </div>
            <Field label="Authenticator code" htmlFor="setup-totp">
              <TextInput
                id="setup-totp"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                value={totpInput}
                onChange={(event) => {
                  setTotpInput(
                    event.target.value.replace(/\D/g, "").slice(0, 6),
                  );
                  setError(null);
                }}
                required
                autoFocus
              />
            </Field>
            {error ? (
              <p role="alert" className="m-0 text-xs text-danger">
                {error}
              </p>
            ) : null}
            <Button type="submit" disabled={busy || totpInput.length !== 6}>
              {busy ? "Creating account…" : "Create administrator account"}
            </Button>
            <Button
              variant="secondary"
              disabled={busy}
              onClick={() => {
                setError(null);
                setStage("account");
              }}
            >
              Back
            </Button>
          </form>
        ) : null}
      </section>
    </main>
  );
}
