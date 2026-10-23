import { useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/auth/AuthProvider";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import { Button, Card, InlineError } from "@/components/panels/form-kit";

export function GithubWorkspaceSetup({ projectId }: { projectId: string }) {
  const auth = useAuth();
  const human = auth.state.status === "human";
  const access = useProjectAccess(projectId, undefined, human);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const exactProject = access.data?.project_id === projectId;
  const canInstall =
    human &&
    exactProject &&
    access.data?.workspace_role === "admin" &&
    typeof access.data.workspace_id === "string" &&
    access.data.workspace_id.length > 0;

  async function beginSetup() {
    if (!canInstall || busy) return;
    setBusy(true);
    setError(null);
    try {
      const result = await api.createGithubSetup(access.data!.workspace_id);
      const installURL = new URL(result.install_url);
      const expiresAt = Date.parse(result.expires_at);
      if (
        installURL.protocol !== "https:" ||
        !installURL.hostname ||
        !Number.isFinite(expiresAt) ||
        expiresAt <= Date.now()
      ) {
        throw new Error("Midnight returned an invalid GitHub installation handoff.");
      }
      window.location.assign(installURL.toString());
    } catch (setupError) {
      setError(setupError);
      setBusy(false);
    }
  }

  return (
    <Card
      title="GitHub App"
      description="Install or update repository access for this project’s workspace. Midnight verifies the GitHub user and re-reads every installation detail before granting access."
      action={
        canInstall ? (
          <Button onClick={() => void beginSetup()} disabled={busy}>
            {busy ? "Opening GitHub…" : "Install GitHub App"}
          </Button>
        ) : undefined
      }
    >
      {access.isPending && human ? (
        <p className="m-0 text-xs text-muted">Checking workspace access…</p>
      ) : canInstall ? (
        <p className="m-0 text-xs text-muted">
          GitHub will return to Midnight after installation. The handoff expires
          in ten minutes and can be used only once.
        </p>
      ) : (
        <p className="m-0 text-xs text-muted">
          A workspace administrator human session is required to install or
          change repository access.
        </p>
      )}
      {error ? <InlineError error={error} /> : null}
    </Card>
  );
}
