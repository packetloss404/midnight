import { useState, type FormEvent } from "react";
import {
  api,
  ApiError,
  type IdentityPasswordResetResult,
} from "@/api/client";
import { getOperatorRecoveryToken, isHumanSessionToken } from "@/api/credentials";
import { Button, Field, TextInput } from "@/components/panels/form-kit";
import { presentError } from "@/lib/errorPresentation";
import {
  MIN_ADMIN_PASSWORD_LENGTH,
  generateTotpSecret,
  isValidEmail,
  otpauthUri,
  verifyTotpCode,
} from "./SetupCrypto";

type ResetStage = "account" | "enroll" | "done";

/**
 * Break-glass password recovery for a locked-out installation administrator.
 *
 * `midnight admin create-user` spends the one-shot `/auth/claim`, so a lost
 * first-run password used to be recoverable only by dropping the database.
 * This is the console half of `midnight admin reset-password`: same route,
 * same operator recovery credential, same rules.
 *
 * The replacement authenticator is generated here rather than server-side for
 * the reason the first-run flow generates its own — the secret has to be
 * readable by the operator exactly once, and the code has to be confirmed
 * before the credential is replaced. The server re-verifies the code before it
 * writes anything.
 */
export function AdministratorPasswordReset({
  fallbackToken = "",
}: {
  /**
   * The token currently typed into the panel's own field, used when this tab
   * has not stored a verified one yet. It lets an operator who cannot sign in
   * paste the token once and reset a password without first entering recovery
   * mode and navigating back.
   */
  fallbackToken?: string;
}) {
  const [stage, setStage] = useState<ResetStage>("account");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [keepAuthenticator, setKeepAuthenticator] = useState(false);
  const [secret, setSecret] = useState("");
  const [totpInput, setTotpInput] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<IdentityPasswordResetResult | null>(null);

  const storedToken = getOperatorRecoveryToken();
  const recoveryToken = (storedToken ?? fallbackToken).trim();
  const account = email.trim().toLowerCase();

  function accountProblem(): string | null {
    if (!recoveryToken) {
      return "Paste your operator recovery token above first.";
    }
    if (isHumanSessionToken(recoveryToken)) {
      return "That is a human session token. Recovery needs the installation's operator token.";
    }
    if (!isValidEmail(email)) return "Enter the administrator's email address.";
    if (password.length < MIN_ADMIN_PASSWORD_LENGTH) {
      return `The new password must be at least ${MIN_ADMIN_PASSWORD_LENGTH} characters.`;
    }
    if (password !== confirmPassword) return "The passwords do not match.";
    return null;
  }

  async function submitReset(totpCode?: string) {
    setBusy(true);
    try {
      const reset = await api.resetAdministratorPassword(
        {
          email: account,
          password,
          ...(totpCode
            ? { totp_secret: secret, totp_code: totpCode }
            : { preserve_totp: true }),
        },
        recoveryToken,
      );
      setPassword("");
      setConfirmPassword("");
      setTotpInput("");
      setResult(reset);
      setStage("done");
    } catch (caught) {
      setError(resetFailureMessage(caught, keepAuthenticator));
    } finally {
      setBusy(false);
    }
  }

  async function submitAccount(event: FormEvent) {
    event.preventDefault();
    const problem = accountProblem();
    if (problem) {
      setError(problem);
      return;
    }
    setError(null);
    if (keepAuthenticator) {
      await submitReset();
      return;
    }
    setSecret(generateTotpSecret());
    setTotpInput("");
    setStage("enroll");
  }

  async function submitEnrollment(event: FormEvent) {
    event.preventDefault();
    setError(null);
    const code = totpInput.trim();
    // Checked locally first so an obvious typo does not spend a request that
    // would otherwise leave the operator guessing which half was wrong.
    if (!(await verifyTotpCode(secret, code))) {
      setError(
        "That code does not match the secret above. Check your authenticator's clock and try the current code.",
      );
      return;
    }
    await submitReset(code);
  }

  if (stage === "done" && result) {
    return (
      <div className="grid gap-2" data-testid="administrator-reset-done">
        <p role="status" className="m-0 text-xs text-text">
          Password reset for {result.user.email}.{" "}
          {result.sessions_revoked === 0
            ? "There were no active sessions to sign out."
            : `${result.sessions_revoked} active session${
                result.sessions_revoked === 1 ? "" : "s"
              } signed out.`}
        </p>
        {result.totp_reenrolled ? (
          <div className="grid gap-1 rounded-lg border border-line bg-surface-2 p-3">
            <p className="m-0 text-[11px] font-semibold uppercase tracking-wider text-muted">
              Enrollment URL
            </p>
            <code className="m-0 break-all font-mono text-[11px] text-text">
              {otpauthUri("Midnight", result.user.email, secret)}
            </code>
            <p className="m-0 text-[11px] font-semibold uppercase tracking-wider text-muted">
              Secret (Base32)
            </p>
            <code
              data-testid="reset-totp-secret"
              className="m-0 break-all font-mono text-xs text-text"
            >
              {secret}
            </code>
            <p className="m-0 text-xs text-danger">
              Save this now. It is not stored in plaintext and is not shown
              again. The account's previous authenticator entry no longer
              works — replace it rather than adding a second one.
            </p>
          </div>
        ) : (
          <p className="m-0 text-xs text-muted">
            The existing authenticator is unchanged. Sign in with the new
            password and the current code.
          </p>
        )}
        <Button
          variant="secondary"
          onClick={() => {
            setResult(null);
            setSecret("");
            setEmail("");
            setError(null);
            setStage("account");
          }}
        >
          Reset another account
        </Button>
      </div>
    );
  }

  if (stage === "enroll") {
    return (
      <form className="grid gap-2" onSubmit={(event) => void submitEnrollment(event)}>
        <div className="grid gap-1 rounded-lg border border-line bg-surface-2 p-3">
          <p className="m-0 text-xs text-text">
            Add this replacement to the authenticator app, then enter the
            current six-digit code. The account's previous entry stops working.
          </p>
          <p className="m-0 text-[11px] font-semibold uppercase tracking-wider text-muted">
            Enrollment URL
          </p>
          <code className="m-0 break-all font-mono text-[11px] text-text">
            {otpauthUri("Midnight", account, secret)}
          </code>
          <p className="m-0 text-[11px] font-semibold uppercase tracking-wider text-muted">
            Secret (Base32)
          </p>
          <code
            data-testid="reset-totp-secret"
            className="m-0 break-all font-mono text-xs text-text"
          >
            {secret}
          </code>
        </div>
        <Field label="Authenticator code" htmlFor="reset-totp">
          <TextInput
            id="reset-totp"
            inputMode="numeric"
            autoComplete="one-time-code"
            pattern="[0-9]{6}"
            maxLength={6}
            value={totpInput}
            onChange={(event) => {
              setTotpInput(event.target.value.replace(/\D/g, "").slice(0, 6));
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
        <Button type="submit" disabled={busy || totpInput.length !== 6}>
          {busy ? "Resetting…" : "Reset password and enroll"}
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
    );
  }

  return (
    <form className="grid gap-2" onSubmit={(event) => void submitAccount(event)}>
      <Field label="Administrator email" htmlFor="reset-email">
        <TextInput
          id="reset-email"
          type="email"
          autoComplete="off"
          value={email}
          onChange={(event) => {
            setEmail(event.target.value);
            setError(null);
          }}
          required
        />
      </Field>
      <Field
        label="New password"
        hint={`At least ${MIN_ADMIN_PASSWORD_LENGTH} characters.`}
        htmlFor="reset-password"
      >
        <TextInput
          id="reset-password"
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
      <Field label="Confirm new password" htmlFor="reset-confirm-password">
        <TextInput
          id="reset-confirm-password"
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
      <label className="flex items-start gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2.5">
        <input
          type="checkbox"
          checked={keepAuthenticator}
          onChange={(event) => {
            setKeepAuthenticator(event.target.checked);
            setError(null);
          }}
          className="mt-0.5"
        />
        <span className="grid gap-0.5">
          <span className="text-xs font-semibold text-text">
            Keep the existing authenticator
          </span>
          <span className="text-[11px] text-muted">
            Change only the password. Leave this unchecked when the
            authenticator is also lost; the account is never left with one
            factor, so this is refused if it has no authenticator enrolled.
          </span>
        </span>
      </label>
      {error ? (
        <p role="alert" className="m-0 text-xs text-danger">
          {error}
        </p>
      ) : null}
      <Button type="submit" disabled={busy}>
        {busy
          ? "Resetting…"
          : keepAuthenticator
            ? "Reset password"
            : "Continue to authenticator setup"}
      </Button>
    </form>
  );
}

/**
 * Maps the route's typed refusals onto copy an operator can act on. The shared
 * presenter keys off status alone, and its 403 line ("you do not have
 * permission") is actively wrong here: the point of this route is that no
 * signed-in account has permission, only the installation credential does.
 */
function resetFailureMessage(caught: unknown, keepAuthenticator: boolean): string {
  if (caught instanceof ApiError) {
    switch (caught.code) {
      case "AUTH_OPERATOR_RECOVERY_REQUIRED":
        return "This needs the installation's operator recovery token. A signed-in account cannot reset an administrator credential.";
      case "RESOURCE_NOT_FOUND":
        return "No enabled administrator account matches that address. Disabled and deleted accounts cannot be recovered this way.";
      case "RESOURCE_VALIDATION_FAILED":
        if (keepAuthenticator) {
          return "That account has no authenticator to keep. Clear the checkbox to enroll a replacement.";
        }
        break;
      case "MFA_INVALID_CODE":
        return "The server rejected the enrollment code. Check the host clock and try the current code.";
      default:
        break;
    }
    if (caught.status === 401) {
      return "That operator recovery token was not accepted.";
    }
  }
  return presentError(caught).message;
}
