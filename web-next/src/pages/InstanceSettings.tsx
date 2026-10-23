// Instance settings (`/settings`). Single-VM "server" concerns: administrator
// authority, GitHub App / installation connection, VM/host inventory, and the
// running version.
//
// Reads: GET /dashboard/summary + GET /dashboard/runtime-inventory
// (components/panels/instance/api.ts) and the shared
// `useGithubInstallations`. The GitHub App is optional; a 501 renders a calm
// "not configured" status rather than an error.

import { useGithubInstallations } from "@/api/hooks";
import { useAuth } from "@/auth/AuthProvider";
import { Loading, ErrorState } from "@/components/states";
import { Card } from "@/components/panels/form-kit";
import { isGithubUnconfigured } from "@/components/panels/github/api";
import {
  useDashboardSummary,
  useRuntimeInventory,
} from "@/components/panels/instance/api";
import { InvitationAcceptance } from "@/components/panels/invitations/InvitationAcceptance";
import { SupportProjectCard } from "@/components/SupportProjectCard";
import { DomainSetup } from "@/components/panels/instance/DomainSetup";

export function InstanceSettings() {
  const auth = useAuth();
  const summary = useDashboardSummary();
  const inventory = useRuntimeInventory();
  const github = useGithubInstallations({ retry: false });

  const githubConfigured = !isGithubUnconfigured(github.error);
  const installations = github.data ?? [];

  return (
    <section className="mx-auto flex w-full max-w-3xl flex-col gap-5 p-[var(--main-gutter)]">
      <header>
        <p className="m-0 font-mono text-[11px] uppercase tracking-wider text-accent">Instance</p>
        <h1 className="m-0 mt-1 text-2xl font-semibold tracking-tight">Server settings</h1>
        <p className="m-0 mt-1 text-muted">Host, version, and connection status for this Midnight instance.</p>
      </header>

      <InvitationAcceptance />

      <DomainSetup />

      {/* Version / update status */}
      <Card
        title="Version"
        description="The API build serving this dashboard."
        action={<StatusDot ok={summary.data?.status === "ready"} label={summary.data?.status ?? "…"} />}
      >
        {summary.isPending ? (
          <Loading label="Loading version…" />
        ) : summary.error ? (
          <ErrorState title="Failed to load version" error={summary.error} onRetry={() => summary.refetch()} />
        ) : (
          <dl className="m-0 grid grid-cols-2 gap-3 text-xs sm:grid-cols-3">
            <Stat label="Server version" value={summary.data?.server_version} mono />
            <Stat label="API version" value={summary.data?.api_version} mono />
            <Stat label="Projects" value={summary.data?.project_count} />
            <Stat label="Services" value={summary.data?.service_count} />
            <Stat label="Activity" value={summary.data?.operation_count} />
            <Stat label="Audit" value={summary.data?.audit_status} />
          </dl>
        )}
      </Card>

      {/* Installation administrator authority */}
      <Card
        title="Administrator access"
        description="Server administration uses the same expiring, revocable Midnight session as the rest of the console."
      >
        <div className="flex items-start gap-3 text-xs">
          <StatusDot
            ok={
              auth.state.status === "operator" ||
              auth.state.status === "developer" ||
              (auth.state.status === "human" &&
                auth.state.me.principal.instance_admin)
            }
            label={
              auth.state.status === "operator"
                ? "Emergency recovery"
                : auth.state.status === "developer"
                  ? "Local development"
                  : "Installation administrator"
            }
          />
          <p className="m-0 text-muted">
            {auth.state.status === "human"
              ? `${auth.state.me.principal.display_name || auth.state.me.principal.email} can manage this Midnight installation without a separate operator token.`
              : auth.state.status === "operator"
                ? "Emergency recovery is active because no administrator session is being used."
                : "Authentication is disabled for this local development instance."}
          </p>
        </div>
      </Card>

      {/* GitHub App */}
      <Card
        title="GitHub App"
        description="Connects repositories for push-to-deploy."
        action={
          <StatusDot
            ok={githubConfigured && !github.isPending}
            label={github.isPending ? "Checking…" : githubConfigured ? "Connected" : "Not configured"}
          />
        }
      >
        {github.isPending ? (
          <Loading label="Checking GitHub App…" />
        ) : !githubConfigured ? (
          <p className="m-0 text-xs text-muted">
            No GitHub App is configured on this instance. Container-image and template deploys still
            work; connect an App to enable deploying from repositories.
          </p>
        ) : github.error ? (
          <ErrorState title="Failed to reach GitHub" error={github.error} onRetry={() => github.refetch()} />
        ) : installations.length === 0 ? (
          <p className="m-0 text-xs text-muted">
            The App is configured but not installed on any account yet. Install it on a GitHub
            account or org to list repositories.
          </p>
        ) : (
          <ul className="m-0 grid list-none gap-1.5 p-0">
            {installations.map((inst) => (
              <li
                key={inst.id}
                className="flex items-center justify-between gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs"
              >
                <span className="font-mono text-text">
                  {inst.account || inst.account_login || inst.id}
                </span>
                {inst.target_type ? <span className="text-subtle">{inst.target_type}</span> : null}
              </li>
            ))}
          </ul>
        )}
      </Card>

      {/* Hosts / VM inventory */}
      <Card
        title="Runtime hosts"
        description="Agents reporting into this control plane."
        action={
          inventory.data?.schema_version ? (
            <span className="font-mono text-[10px] text-subtle">{inventory.data.schema_version}</span>
          ) : null
        }
      >
        {inventory.isPending ? (
          <Loading label="Loading host inventory…" />
        ) : inventory.error ? (
          <ErrorState
            title="Failed to load inventory"
            error={inventory.error}
            onRetry={() => inventory.refetch()}
          />
        ) : (inventory.data?.hosts ?? []).length === 0 ? (
          <p className="m-0 text-xs text-muted">
            No hosts are currently registered. Start a Midnight agent on this VM to see it here.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left text-xs">
              <thead className="text-[10px] uppercase tracking-wider text-subtle">
                <tr>
                  <th className="px-2 py-1.5 font-semibold">Host</th>
                  <th className="px-2 py-1.5 font-semibold">Profile</th>
                  <th className="px-2 py-1.5 font-semibold">Docker</th>
                  <th className="px-2 py-1.5 font-semibold">Doctor</th>
                  <th className="px-2 py-1.5 font-semibold">Version</th>
                </tr>
              </thead>
              <tbody>
                {(inventory.data?.hosts ?? []).map((h) => (
                  <tr key={h.agent_id || h.name} className="border-t border-line-soft text-text">
                    <td className="px-2 py-1.5 font-mono">{h.name}</td>
                    <td className="px-2 py-1.5 text-muted">{h.profile || "—"}</td>
                    <td className="px-2 py-1.5 text-muted">{h.docker || "—"}</td>
                    <td className="px-2 py-1.5 text-muted">{h.doctor || "—"}</td>
                    <td className="px-2 py-1.5 font-mono text-muted">{h.version || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <SupportProjectCard />
    </section>
  );
}

function Stat({ label, value, mono }: { label: string; value?: string | number; mono?: boolean }) {
  return (
    <div className="grid gap-0.5">
      <dt className="text-[10px] uppercase tracking-wider text-subtle">{label}</dt>
      <dd className={"m-0 text-text " + (mono ? "font-mono" : "")}>
        {value === undefined || value === "" ? "—" : value}
      </dd>
    </div>
  );
}

function StatusDot({ ok, label }: { ok: boolean; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-[11px] font-medium text-muted" role="status">
      <span
        aria-hidden
        className={
          "h-2 w-2 rounded-full " + (ok ? "bg-[var(--ok,#3fb950)]" : "bg-[var(--warn,#d29922)]")
        }
      />
      {label}
    </span>
  );
}
