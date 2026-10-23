import { useAuth } from "@/auth/AuthProvider";
import { useState } from "react";
import { Button, Card, InlineError } from "@/components/panels/form-kit";
import { newIdempotencyKey } from "@/components/panels/create/api";
import { Empty, ErrorState, Loading } from "@/components/states";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import {
  canRedriveProjectDelivery,
  useProjectWebhookDeliveries,
  useRedriveProjectWebhookDelivery,
} from "./api";

export function ProjectWebhookDeliveries({ projectId }: { projectId: string }) {
  const auth = useAuth();
  const installationAuthority = auth.operatorFeaturesAvailable;
  const human = auth.state.status === "human";
  const access = useProjectAccess(projectId, undefined, human && installationAuthority);
  const exactHumanAccess =
    human &&
    access.data?.principal_kind === "human" &&
    access.data.project_id === projectId &&
    access.data.environment_id === undefined;
  const canRead =
    installationAuthority &&
    (!human ||
      (exactHumanAccess && access.data?.permissions.includes("project.read") === true));
  const canRedrive =
    canRead &&
    (!human || access.data?.permissions.includes("deployment.trigger.git") === true);
  const authority = human ? "human" : "operator";
  const deliveries = useProjectWebhookDeliveries(projectId, authority, canRead);
  const redrive = useRedriveProjectWebhookDelivery(projectId, authority);
  const [redriveKeys, setRedriveKeys] = useState<Record<string, string>>({});

  if (!installationAuthority) return null;
  if (human && access.isPending) {
    return (
      <Card title="Webhook deliveries" description="Project-scoped webhook delivery recovery.">
        <Loading label="Checking project webhook access…" />
      </Card>
    );
  }
  if (human && (access.error || !canRead)) {
    return (
      <Card title="Webhook deliveries" description="Project-scoped webhook delivery recovery.">
        <ErrorState
          title="Webhook access unavailable"
          error={access.error ?? new Error("Midnight could not verify exact project read access.")}
          onRetry={() => void access.refetch()}
        />
      </Card>
    );
  }
  return (
    <Card
      title="Webhook deliveries"
      description="Server-scoped recovery view of the 50 most recent incoming deliveries for this project."
    >
      {deliveries.isPending ? (
        <Loading label="Verifying webhook delivery ownership…" />
      ) : deliveries.error ? (
        <ErrorState
          title="Webhook ownership could not be verified"
          error={deliveries.error}
          onRetry={() => void deliveries.refetch()}
        />
      ) : deliveries.data?.length === 0 ? (
        <Empty
          title="No recent deliveries"
          detail="No recent delivery targets belong to this project."
          action={<Button variant="secondary" onClick={() => void deliveries.refetch()}>Refresh deliveries</Button>}
        />
      ) : (
        <ul className="m-0 grid list-none gap-2 p-0">
          {(deliveries.data ?? []).map((delivery) => {
            const exclusive = canRedriveProjectDelivery(delivery, projectId);
            return (
              <li key={delivery.id} className="grid gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3 text-xs">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <p className="m-0 font-semibold text-text">{delivery.provider} · {delivery.event_type}</p>
                    <p className="m-0 mt-1 font-mono text-[10px] text-subtle">{delivery.id}</p>
                  </div>
                  <span className="rounded-full border border-line px-2 py-1 font-mono text-[10px] uppercase text-muted">{delivery.state.replace("_", " ")}</span>
                </div>
                <p className="m-0 text-muted">
                  {delivery.targets.length} target{delivery.targets.length === 1 ? "" : "s"} · {delivery.route_attempt}/{delivery.max_route_attempts} route attempts
                </p>
                {delivery.reason_code || delivery.last_error ? (
                  <p className="m-0 text-muted">{delivery.reason_code ?? delivery.last_error}</p>
                ) : null}
                {delivery.state === "dead_letter" ? (
                  exclusive && canRedrive ? (
                    <div><Button disabled={redrive.isPending} onClick={() => {
                      const idempotencyKey = redriveKeys[delivery.id] ?? newIdempotencyKey();
                      setRedriveKeys((current) => ({ ...current, [delivery.id]: idempotencyKey }));
                      void redrive.mutateAsync({ detail: delivery, idempotencyKey })
                        .then(() => setRedriveKeys((current) => {
                          const next = { ...current }; delete next[delivery.id]; return next;
                        }))
                        .catch(() => undefined);
                    }}>{redrive.isPending ? "Redriving…" : "Redrive delivery"}</Button></div>
                  ) : exclusive ? (
                    <p role="note" className="m-0 text-muted">Redrive requires deployment-trigger permission for this project.</p>
                  ) : (
                    <p role="note" className="m-0 text-muted">Redrive is unavailable here because this delivery also targets another project.</p>
                  )
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      {redrive.error ? <InlineError error={redrive.error} /> : null}
    </Card>
  );
}
