import { useEffect, useState } from "react";
import type { DeploymentResource } from "@/api/client";
import { useAuth } from "@/auth/AuthProvider";
import { loadDiagnosis, type DiagnosisReport } from "./api";

export function DeploymentDiagnosis({ deployment }: { deployment: DeploymentResource }) {
  if (!deployment.operation_id || ![deployment.operation_state, deployment.status].some((state) => state?.toUpperCase() === "FAILED")) return null;
  return <Report deployment={deployment} />;
}

function Report({ deployment }: { deployment: DeploymentResource }) {
  const { state: auth } = useAuth();
  const principal = auth.status === "human" ? `${auth.me.principal.user_id}:${auth.me.principal.session_id}` : auth.status;
  const scope = JSON.stringify([principal, deployment.project_id, deployment.service_id, deployment.environment_id, deployment.deployment_id ?? deployment.id, deployment.operation_id]);
  const [result, setResult] = useState<{ scope: string; report?: DiagnosisReport; error?: boolean }>({ scope });
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempts = 0;
    setResult({ scope });
    async function refresh() {
      try {
        const report = await loadDiagnosis(deployment, controller.signal);
        if (controller.signal.aborted) return;
        setResult({ scope, report: report ?? undefined });
        // Revalidate displayed reports as well as waiting for late attachment.
        if (report || ++attempts < 60) timer = setTimeout(() => void refresh(), 5_000);
      } catch {
        if (!controller.signal.aborted) setResult({ scope, error: true });
      }
    }
    if (["human", "operator", "developer"].includes(auth.status)) void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  // The canonical tuple covers all resource changes; no credential enters state
  // or a query-cache key. A previous scope is hidden during the effect handoff.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, revision]);
  const current = result.scope === scope ? result : undefined;
  return <section aria-label="Failure diagnosis" className="grid gap-3 rounded-lg border border-line bg-surface-2 p-4">
    <h3 className="m-0 text-sm font-semibold text-text">Failure diagnosis</h3>
    <p className="m-0 text-xs text-muted">AI-generated report. Verify suggestions before acting.</p>
    {current?.report ? current.report.text.map((text, index) => <div key={index} className="whitespace-pre-wrap break-words text-sm text-text">{text}</div>) :
      <p role="status" className="m-0 text-sm text-muted">{current?.error ? "Diagnosis unavailable. It may be disabled or access may have changed." : "No diagnosis attached yet. Reports can arrive after a deployment fails; generation requires a configured AI provider."}</p>}
    <button type="button" className="justify-self-start text-xs text-accent" onClick={() => setRevision((value) => value + 1)}>Refresh diagnosis</button>
  </section>;
}
