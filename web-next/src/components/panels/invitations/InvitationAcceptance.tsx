import { useState, type FormEvent } from "react";
import { useAuth } from "@/auth/AuthProvider";
import {
  Button,
  Card,
  Field,
  InlineError,
  TextInput,
} from "@/components/panels/form-kit";
import {
  type IdentityInvitation,
  useAcceptIdentityInvitation,
} from "./api";

export function InvitationAcceptance() {
  const auth = useAuth();

  if (auth.state.status !== "human") return null;

  return <InvitationAcceptanceForm />;
}

function InvitationAcceptanceForm() {
  const acceptInvitation = useAcceptIdentityInvitation();
  const [invitationToken, setInvitationToken] = useState("");
  const [validationError, setValidationError] = useState<string>();
  const [submitError, setSubmitError] = useState<Error | null>(null);
  const [accepted, setAccepted] = useState<IdentityInvitation | null>(null);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setValidationError(undefined);
    setSubmitError(null);
    setAccepted(null);
    const token = invitationToken.trim();
    if (!token.startsWith("midn_inv_")) {
      setValidationError(
        "Paste a complete Midnight invitation credential beginning with midn_inv_.",
      );
      return;
    }
    try {
      const result = await acceptInvitation.mutateAsync(token);
      setInvitationToken("");
      setAccepted(result);
      acceptInvitation.reset();
    } catch (error) {
      setSubmitError(
        error instanceof Error
          ? error
          : new Error("Invitation acceptance failed."),
      );
      // Clear the secret-bearing mutation variables; the input remains only
      // in this form so the user can decide whether to retry.
      acceptInvitation.reset();
    }
  }

  return (
    <Card
      title="Accept invitation"
      description="Redeem a manually delivered invitation credential as your signed-in account."
    >
      {accepted ? (
        <div
          role="status"
          aria-live="polite"
          className="grid gap-3 rounded-[var(--r-sm)] border border-accent/40 bg-accent-soft px-4 py-4 text-sm"
        >
          <div>
            <strong className="text-accent">Invitation accepted</strong>
            <p className="m-0 mt-1 text-xs leading-5 text-muted">
              Your{" "}
              {accepted.project_id
                ? `${accepted.project_role ?? "project"} role`
                : `${accepted.workspace_role ?? "workspace"} role`}{" "}
              is active. The project list will refresh automatically.
            </p>
          </div>
          <div>
            <Button
              variant="secondary"
              onClick={() => setAccepted(null)}
            >
              Accept another invitation
            </Button>
          </div>
        </div>
      ) : (
        <form className="grid gap-3" onSubmit={(event) => void submit(event)}>
          <Field
            label="Invitation credential"
            htmlFor="identity-invitation-credential"
            hint="Credentials are matched to the email address on your current Midnight account."
          >
            <TextInput
              id="identity-invitation-credential"
              type="password"
              autoComplete="off"
              spellCheck={false}
              disabled={acceptInvitation.isPending}
              value={invitationToken}
              onChange={(event) => {
                setInvitationToken(event.target.value);
                setValidationError(undefined);
                setSubmitError(null);
                acceptInvitation.reset();
              }}
              placeholder="midn_inv_…"
              className="font-mono"
            />
          </Field>
          <p className="m-0 text-[11px] leading-5 text-subtle">
            Paste the credential itself. Midnight does not accept invitation
            tokens from a URL or query string and does not send invitation
            email.
          </p>
          {validationError ? <InlineError error={validationError} /> : null}
          {submitError ? <InlineError error={submitError} /> : null}
          <div>
            <Button
              type="submit"
              className="w-full sm:w-auto"
              disabled={
                acceptInvitation.isPending || !invitationToken.trim()
              }
            >
              {acceptInvitation.isPending
                ? "Accepting…"
                : "Accept invitation"}
            </Button>
          </div>
        </form>
      )}
    </Card>
  );
}
