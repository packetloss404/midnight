import { ErrorState, Loading } from "@/components/states";
import { useCronOccurrences, type CronOccurrence } from "./settings/cron";

export function CronOccurrenceHistory({ serviceId, environmentId, enabled }: { serviceId: string; environmentId: string; enabled: boolean }) {
  const query = useCronOccurrences(serviceId, environmentId, enabled);
  if (!enabled) return null;
  if (query.isPending) return <section className="rounded-lg border border-line bg-surface px-4 py-4"><Loading label="Loading cron history…" /></section>;
  if (query.error) return <ErrorState title="Failed to load cron history" error={query.error} onRetry={() => query.refetch()} />;
  if (!query.data?.length) return <section className="rounded-lg border border-line bg-surface px-4 py-4"><h3 className="m-0 text-sm font-semibold text-text">Cron history</h3><p className="mt-1 text-xs text-muted">No runs yet. This job uses a five-field UTC schedule with a five-minute minimum; runs start on their own and cannot be triggered by hand.</p></section>;
  return <section className="grid gap-3 rounded-lg border border-line bg-surface px-4 py-4"><header><h3 className="m-0 text-sm font-semibold text-text">Cron history</h3><p className="m-0 text-xs text-muted">Runs start on their own (five-field UTC, five-minute minimum). Each row shows the final result and any retry; there are no manual controls.</p></header><div className="grid gap-2">{query.data.map((item) => <Occurrence key={item.id} item={item} />)}</div></section>;
}

function Occurrence({ item }: { item: CronOccurrence }) {
  const due = new Date(item.due_at).toLocaleString();
  const retry = item.retry_at ? `Retry scheduled ${new Date(item.retry_at).toLocaleString()}` : null;
  const failure = item.failure_code ? `${item.failure_code}${item.failure_message ? `: ${item.failure_message}` : ""}` : null;
  const evidence = item.state === "failed" ? (failure ?? "FAILED") : item.state === "succeeded" ? "Completed successfully" : item.state === "retry_wait" ? [failure, retry].filter(Boolean).join(" · ") || "Retry queued" : item.state === "running" ? "Running" : "Waiting to run";
  const color = item.state === "failed" ? "text-danger" : item.state === "retry_wait" ? "text-warning" : item.state === "succeeded" ? "text-accent" : "text-muted";
  return <article className="rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-2"><div className="flex flex-wrap justify-between gap-2 text-xs"><span className="font-mono text-subtle">{due}</span><strong className={color}>{item.state.replace("_", " ")}</strong></div><p className={`m-0 mt-1 text-xs ${color}`}>{evidence}</p><span className="text-[11px] text-subtle">Attempt {item.attempt}/{item.max_attempts}{item.exit_code !== undefined ? ` · exit ${item.exit_code}` : ""}</span></article>;
}
