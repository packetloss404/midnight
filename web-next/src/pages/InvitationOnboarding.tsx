import { useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useAuth } from "@/auth/AuthProvider";
import { ApiError } from "@/api/client";
import { onboardIdentityInvitation } from "@/components/panels/invitations/api";
import { Button, Field, TextInput } from "@/components/panels/form-kit";
import { presentError } from "@/lib/errorPresentation";

// Invitation credentials are entered manually, never carried in a URL,
// persistent storage, query cache, or enrollment response.
export function InvitationOnboarding() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [token, setToken] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [created, setCreated] = useState(false);
  const [destination, setDestination] = useState<string>();
  const signedIn = auth.state.status === "human" || auth.state.status === "operator";

  // Login persists its credential before React commits the new context. Wait
  // for that commit before mounting the protected route's anonymous redirect.
  useEffect(() => {
    if (!destination || auth.state.status !== "human") return;
    void navigate({ to: destination, replace: true } as never).catch(() => {
      setError("Your account is ready. Open the projects page to continue.");
    });
  }, [destination, auth.state.status, navigate]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError(undefined);
    if (password !== confirmation) {
      setError("Passwords must match.");
      return;
    }
    setBusy(true);
    let enrolled = false;
    try {
      const result = await onboardIdentityInvitation({ invitation_token: token.trim(), display_name: name.trim(), password });
      enrolled = true;
      setCreated(true);
      setToken("");
      await auth.login({ email: result.email, password, client_kind: "dashboard" });
      setPassword("");
      setConfirmation("");
      const nextDestination = result.invitation.project_id
        ? `/projects/${encodeURIComponent(result.invitation.project_id)}` : "/";
      setDestination(nextDestination);
    } catch (caught) {
      setError(enrolled
        ? "Your account was created and the invitation accepted. Sign in with your new password to continue."
        : caught instanceof ApiError && caught.code === "INVITATION_ACCOUNT_EXISTS"
          ? "This account already exists. Sign in and accept the invitation. Its password was not changed."
          : caught instanceof ApiError && caught.status === 409
            ? "This invitation is unavailable. Ask its sender for a current invitation."
            : presentError(caught).message);
      setPassword("");
      setConfirmation("");
    } finally {
      setBusy(false);
    }
  }

  return <main className="grid min-h-[100dvh] place-items-center px-4 py-10">
    <section className="grid w-full max-w-md gap-5 rounded-xl border border-line bg-surface p-6 shadow-lg">
      <header className="grid gap-2">
        <h1 className="m-0 text-2xl font-semibold">Join with an invitation</h1>
        <p className="m-0 text-sm text-muted">Create a Midnight account for the email address your sender invited. Your invitation sets your workspace or project access.</p>
      </header>
      <p className="m-0 text-sm text-muted">Already have an account? <a href="/login" className="text-accent underline">Sign in</a> and accept the invitation from the projects page. This form cannot change an existing account’s password.</p>
      {error ? <p role="alert" className="m-0 text-sm text-danger">{error}</p> : null}
      {signedIn ? <p role="status" className="m-0 text-sm text-muted">You are already signed in. Accept an invitation from the <a href="/" className="text-accent underline">projects page</a>, or sign out before creating another account.</p>
        : created ? <p role="status" className="m-0 text-sm">Your account is ready. <a href="/login" className="text-accent underline">Sign in to continue</a>.</p>
        : <form aria-label="Create invited account" className="grid gap-4" onSubmit={submit}>
          <Field label="Invitation credential" htmlFor="onboard-token"><TextInput id="onboard-token" type="password" autoComplete="off" spellCheck={false} value={token} onChange={event => setToken(event.target.value)} required maxLength={128} /></Field>
          <Field label="Your name" htmlFor="onboard-name"><TextInput id="onboard-name" autoComplete="name" value={name} onChange={event => setName(event.target.value)} required maxLength={160} /></Field>
          <Field label="Password" htmlFor="onboard-password" hint="At least 12 characters."><TextInput id="onboard-password" type="password" autoComplete="new-password" value={password} onChange={event => setPassword(event.target.value)} required minLength={12} maxLength={1024} /></Field>
          <Field label="Confirm password" htmlFor="onboard-confirm"><TextInput id="onboard-confirm" type="password" autoComplete="new-password" value={confirmation} onChange={event => setConfirmation(event.target.value)} required minLength={12} maxLength={1024} /></Field>
          <Button type="submit" disabled={busy || !token.trim() || !name.trim() || password.length < 12 || password !== confirmation}>{busy ? "Creating account…" : "Create account and join"}</Button>
        </form>}
    </section>
  </main>;
}
