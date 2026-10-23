import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useIdentityWorkspaces } from "@/api/hooks";
import { useAuth } from "@/auth/AuthProvider";
import { SupportProjectCard } from "@/components/SupportProjectCard";
import { Card } from "@/components/panels/form-kit";
import {
  useUsageSummary,
  type KnownUsageDimension,
  type UsageDimension,
  type UsageLine,
  type UsageWindow,
} from "@/components/panels/usage/api";
import { ErrorState, Loading } from "@/components/states";

type Period = "month" | "7d" | "30d";

const DIMENSIONS: Record<KnownUsageDimension, { label: string; detail: string }> = {
  source_artifact_bytes: {
    label: "Source artifacts",
    detail: "Retained source bytes recorded by the artifact store.",
  },
  project_count: { label: "Projects", detail: "Active projects in this workspace." },
  service_count: { label: "Services", detail: "Active services across workspace projects." },
  environment_count: {
    label: "Environments",
    detail: "Active deployment environments across workspace projects.",
  },
  volume_count: {
    label: "Volumes",
    detail: "Volumes Midnight is tracking; the size figure is for reference only.",
  },
  build_seconds: {
    label: "Build time",
    detail: "Time from the start of each source build to the moment it finished.",
  },
  cpu_seconds: {
    label: "CPU time",
    detail: "Derived from bounded runtime sample intervals; gaps are never interpolated.",
  },
  memory_byte_seconds: {
    label: "Memory time",
    detail: "Derived from observed memory gauges over bounded intervals.",
  },
  public_http_requests: {
    label: "Public requests",
    detail: "Requests observed by the public Traefik access surface only.",
  },
  public_http_response_bytes: {
    label: "Public response bytes",
    detail: "Proxy-observed public bytes, not generic or private-network egress.",
  },
};

export function UsageLimits() {
  const auth = useAuth();
  const workspaces = useIdentityWorkspaces({ enabled: auth.state.status === "human" });
  const [workspaceId, setWorkspaceId] = useState("");
  const [period, setPeriod] = useState<Period>("month");
  const window = useMemo(() => usageWindow(period, new Date()), [period]);
  const summary = useUsageSummary(workspaceId || undefined, window);

  useEffect(() => {
    const available = workspaces.data ?? [];
    if (available.some((workspace) => workspace.id === workspaceId)) return;
    setWorkspaceId(available[0]?.id ?? "");
  }, [workspaceId, workspaces.data]);

  if (auth.state.status !== "human") {
    return (
      <Page>
        <Card
          title="Workspace sign-in required"
          description="Usage is tenant-scoped and can only be read through an authenticated Midnight account."
        >
          <p className="m-0 text-sm text-muted">
            Emergency recovery does not grant a browser a workspace identity. Sign in with your
            workspace account to see your usage and limits.
          </p>
        </Card>
        <SupportProjectCard />
      </Page>
    );
  }

  return (
    <Page>
      <header className="grid gap-1">
        <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">
          Workspace usage
        </p>
        <h1 className="m-0 text-2xl font-semibold tracking-tight">Usage &amp; limits</h1>
        <p className="m-0 max-w-2xl text-sm text-muted">
          Self-hosted showback and safety limits. Midnight distinguishes enforced values from
          estimated figures, partial figures, and figures it cannot measure; it does not estimate a bill.
        </p>
      </header>

      <Card title="Scope and period" description="Every read is authorized against the selected workspace membership.">
        {workspaces.isPending ? (
          <Loading label="Loading workspaces…" />
        ) : workspaces.error ? (
          <ErrorState title="Could not load workspaces" error={workspaces.error} onRetry={() => workspaces.refetch()} />
        ) : (workspaces.data ?? []).length === 0 ? (
          <p className="m-0 text-sm text-muted">No workspace is available to this account.</p>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="grid gap-1.5 text-xs font-semibold text-text">
              Workspace
              <select
                aria-label="Workspace"
                value={workspaceId}
                onChange={(event) => setWorkspaceId(event.currentTarget.value)}
                className="h-10 rounded-[var(--r-sm)] border border-line bg-surface px-3 text-sm text-text"
              >
                {(workspaces.data ?? []).map((workspace) => (
                  <option key={workspace.id} value={workspace.id}>
                    {workspace.display_name || workspace.slug}
                  </option>
                ))}
              </select>
            </label>
            <label className="grid gap-1.5 text-xs font-semibold text-text">
              Period
              <select
                aria-label="Period"
                value={period}
                onChange={(event) => setPeriod(event.currentTarget.value as Period)}
                className="h-10 rounded-[var(--r-sm)] border border-line bg-surface px-3 text-sm text-text"
              >
                <option value="month">Current month</option>
                <option value="7d">Last 7 days</option>
                <option value="30d">Last 30 days</option>
              </select>
            </label>
          </div>
        )}
      </Card>

      {workspaceId ? (
        summary.isPending ? (
          <Loading label="Adding up your usage…" />
        ) : summary.error ? (
          <ErrorState title="Could not load usage" error={summary.error} onRetry={() => summary.refetch()} />
        ) : summary.data ? (
          <UsageEvidence summary={summary.data} />
        ) : null
      ) : null}

      <Card
        title="Policy behavior"
        description="Installation safety policy, not a subscription or payment entitlement."
      >
        <ul className="m-0 grid gap-2 pl-5 text-xs leading-relaxed text-muted">
          <li>Deletes, downscales, cleanup, and recovery remain available when a limit is reached.</li>
          <li>Estimated CPU, memory, and traffic figures are for information only and never stop a workload.</li>
          <li>Volume size remains informational until a host quota driver proves enforcement.</li>
          <li>Figures Midnight cannot measure are labelled as such rather than shown as zero.</li>
        </ul>
      </Card>

      <SupportProjectCard />
    </Page>
  );
}

function Page({ children }: { children: ReactNode }) {
  return <section className="mx-auto flex w-full max-w-5xl flex-col gap-5 p-[var(--main-gutter)]">{children}</section>;
}

function UsageEvidence({ summary }: { summary: import("@/components/panels/usage/api").UsageSummary }) {
  return (
    <>
      <section aria-labelledby="usage-resources-title" className="grid gap-3">
        <div className="flex flex-wrap items-end justify-between gap-2">
          <div>
            <h2 id="usage-resources-title" className="m-0 text-sm font-semibold text-text">Resources</h2>
            <p className="m-0 mt-1 text-xs text-muted">
              Through {formatTimestamp(summary.as_of)} · policy {summary.policy_profile.replaceAll("_", " ")}
            </p>
          </div>
          <span className="font-mono text-[10px] text-muted">
            {formatTimestamp(summary.window_start)} – {formatTimestamp(summary.window_end)}
          </span>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {summary.usage.map((line) => <UsageCard key={line.dimension} line={line} />)}
        </div>
      </section>
      <Card title="What these numbers mean" description="What Midnight can and cannot measure for this workspace.">
        <ul className="m-0 grid gap-1.5 pl-5 text-xs leading-relaxed text-muted">
          {summary.disclosures.map((disclosure) => <li key={disclosure}>{disclosure}</li>)}
        </ul>
      </Card>
    </>
  );
}

function UsageCard({ line }: { line: UsageLine }) {
  const definition = usageDimensionDefinition(line.dimension);
  const badge = evidenceBadge(line);
  const hasLimit = line.limit !== undefined;
  const showsProgress = hasLimit && (line.limit as number) > 0 && line.coverage.status !== "unavailable";
  const ratio = showsProgress ? Math.min(1, line.used / (line.limit as number)) : undefined;
  return (
    <article className="grid min-h-44 content-between gap-4 rounded-lg border border-line bg-surface p-4">
      <div className="grid gap-2">
        <div className="flex items-start justify-between gap-2">
          <h3 className="m-0 break-words text-sm font-semibold text-text">{definition.label}</h3>
          <span className={`rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${badge.className}`}>
            {badge.label}
          </span>
        </div>
        <p className="m-0 text-xs leading-relaxed text-muted">{definition.detail}</p>
      </div>
      <div className="grid gap-2">
        <p className="m-0 font-mono text-xl font-semibold text-text">
          {line.coverage.status === "unavailable" ? "Not measured" : formatValue(line.used, line.unit)}
        </p>
        {hasLimit ? (
          <>
            {showsProgress ? (
              <div
                role="progressbar"
                aria-label={`${definition.label} usage`}
                aria-valuemin={0}
                aria-valuemax={line.limit}
                aria-valuenow={Math.min(line.used, line.limit as number)}
                aria-valuetext={`${formatValue(line.used, line.unit)} of ${formatValue(line.limit as number, line.unit)}`}
                className="h-1.5 overflow-hidden rounded-full bg-surface-3"
              >
                <span className="block h-full rounded-full bg-accent" style={{ width: `${Math.round((ratio ?? 0) * 100)}%` }} />
              </div>
            ) : null}
            <p className="m-0 text-[11px] text-muted">
              {line.enforceable ? "Hard limit" : "Reference limit"}: {formatValue(line.limit as number, line.unit)}
            </p>
          </>
        ) : (
          <p className="m-0 text-[11px] text-muted">No enforced limit</p>
        )}
        {line.coverage.note ? <p className="m-0 text-[11px] text-muted">{line.coverage.note}</p> : null}
        {line.coverage.missing_intervals > 0 ? (
          <p className="m-0 text-[11px] text-warn">{line.coverage.missing_intervals} gap{line.coverage.missing_intervals === 1 ? "" : "s"} in measurement</p>
        ) : null}
      </div>
    </article>
  );
}

function usageDimensionDefinition(
  dimension: UsageDimension,
): { label: string; detail: string } {
  if (dimension in DIMENSIONS) {
    return DIMENSIONS[dimension as KnownUsageDimension];
  }
  const words = dimension.replace(/[_-]+/g, " ").trim();
  return {
    label: words ? words[0].toUpperCase() + words.slice(1) : "Additional usage",
    detail: "This usage dimension is reported by the server; this console has no local description for it yet.",
  };
}

function evidenceBadge(line: UsageLine): { label: string; className: string } {
  if (line.coverage.status === "unavailable") {
    return { label: "Unavailable", className: "border-line-strong bg-surface-2 text-muted" };
  }
  if (line.coverage.status === "partial") {
    return { label: "Incomplete", className: "border-warn/40 bg-warn-soft text-warn" };
  }
  if (line.enforceable) {
    return { label: "Enforced", className: "border-accent/40 bg-accent-soft text-accent" };
  }
  return { label: "Measured", className: "border-line-strong bg-surface-2 text-text" };
}

function usageWindow(period: Period, now: Date): UsageWindow {
  const until = new Date(now);
  const since = new Date(now);
  if (period === "month") {
    since.setUTCDate(1);
    since.setUTCHours(0, 0, 0, 0);
  } else {
    since.setUTCDate(since.getUTCDate() - (period === "7d" ? 7 : 30));
  }
  return { since: since.toISOString(), until: until.toISOString() };
}

function formatTimestamp(value: string): string {
  const date = new Date(value);
  return Number.isFinite(date.getTime())
    ? new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: "UTC" }).format(date)
    : "Date unavailable";
}

function formatValue(value: number, unit: UsageLine["unit"]): string {
  if (unit === "bytes") return formatBytes(value);
  if (unit === "byte-seconds") {
    return value >= 3_600
      ? `${formatBytes(value / 3_600)}·h`
      : `${formatBytes(value)}·s`;
  }
  if (unit === "seconds") {
    if (value >= 3_600) return `${(value / 3_600).toFixed(value >= 36_000 ? 0 : 1)} h`;
    if (value >= 60) return `${(value / 60).toFixed(value >= 600 ? 0 : 1)} min`;
    return `${value.toLocaleString()} s`;
  }
  return value.toLocaleString();
}

function formatBytes(value: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let amount = value;
  let index = 0;
  while (amount >= 1024 && index < units.length - 1) {
    amount /= 1024;
    index += 1;
  }
  return `${amount.toFixed(index === 0 || amount >= 10 ? 0 : 1)} ${units[index]}`;
}

export const usageLimitsTestHelpers = {
  usageWindow,
  formatValue,
  evidenceBadge,
  usageDimensionDefinition,
  formatTimestamp,
};
