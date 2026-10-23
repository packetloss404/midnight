import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { ErrorState, Loading } from "@/components/states";
import { usePreviewEnvironments } from "./api";

export function PreviewEnvironmentCapability({ serviceId }: { serviceId: string }) {
  const previews = usePreviewEnvironments(serviceId);
  if (previews.isPending) return <Loading label="Checking preview environment capability…" />;
  if (previews.error) {
    return (
      <ErrorState
        title="Failed to load preview environment capability"
        error={previews.error}
        onRetry={() => previews.refetch()}
      />
    );
  }

  const capability = previews.data?.capability;
  if (!capability) return null;
  if (capability.state === "enabled") {
    const items = previews.data?.items ?? [];
    return (
      <section className="grid gap-2" aria-label="Pull request preview environments">
        <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-1">
          <div className="min-w-0 flex-1 basis-48">
            <h3 className="m-0 text-sm font-semibold text-strong">Pull request previews</h3>
            <p className="m-0 mt-0.5 text-[11px] text-muted">
              GitHub opens, refreshes, and removes isolated environments automatically.
            </p>
          </div>
          <span className="shrink-0 rounded-full border border-success/30 bg-success/10 px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-success">
            Automatic
          </span>
        </div>
        {items.length === 0 ? (
          <p className="m-0 rounded border border-dashed border-line px-3 py-3 text-xs text-muted">
            No open pull requests have preview environments for this service.
          </p>
        ) : (
          <ul className="m-0 grid list-none gap-1.5 p-0">
            {items.map((preview) => (
              <li key={preview.id} className="flex items-center justify-between gap-3 rounded border border-line bg-bg-2 px-3 py-2.5">
                <div className="min-w-0">
                  <div className="flex items-center gap-2 text-xs font-medium text-strong">
                    <span>PR #{preview.pull_request}</span>
                    <span className="rounded bg-bg-3 px-1.5 py-0.5 font-mono text-[10px] text-muted">{preview.head_sha.slice(0, 7)}</span>
                    {preview.from_fork ? <span className="text-[10px] text-warning">fork · isolated</span> : null}
                  </div>
                  <span className="mt-1 block text-[10px] uppercase tracking-wide text-muted">{preview.state}</span>
                </div>
                {preview.url ? (
                  <a className="shrink-0 rounded border border-line px-2.5 py-1.5 text-xs text-accent hover:bg-bg-3" href={preview.url} target="_blank" rel="noreferrer">
                    Open preview
                  </a>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </section>
    );
  }
  return (
    <section className="grid gap-2" aria-label="Pull request preview environment status">
      <CapabilityBoundary
        state="setup-required"
        title="Pull request preview environments"
        detail="GitHub pull request webhooks are authenticated and acknowledged, but this server does not route them or automatically manage environments. No preview was created."
      />
      <ul className="m-0 grid list-none gap-1 p-0 text-[11px] text-muted">
        {capability.blockers.map((blocker) => (
          <li key={blocker.code} className="rounded border border-line bg-bg-2 px-2.5 py-2">
            <span className="font-mono text-[10px] text-subtle">{blocker.code}</span>
            <span className="mt-0.5 block">{blocker.detail}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
