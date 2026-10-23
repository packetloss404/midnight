import type { ReactNode } from "react";
import {
  Callout,
  CommandBlock,
  DefinitionGrid,
  DocsCard,
  DocsHero,
  DocsPage,
} from "./DocsContent";

type HTTPMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export type FeaturedEndpoint = {
  method: HTTPMethod;
  path: string;
  operationId: string;
  summary: string;
};

export type FeaturedEndpointGroup = {
  title: string;
  detail: string;
  endpoints: readonly FeaturedEndpoint[];
};

export const FEATURED_ENDPOINT_GROUPS: readonly FeaturedEndpointGroup[] = [
  {
    title: "Identity and scope",
    detail: "Create a human session, identify the caller, and discover only the tenant resources it may see.",
    endpoints: [
      { method: "POST", path: "/auth/sessions", operationId: "createIdentitySession", summary: "Create a human API session" },
      { method: "GET", path: "/auth/me", operationId: "getIdentityPrincipal", summary: "Read the verified principal" },
      { method: "GET", path: "/workspaces", operationId: "listIdentityWorkspaces", summary: "List accepted workspaces" },
      { method: "GET", path: "/projects/{project_id}/access", operationId: "getProjectAccess", summary: "Inspect effective project capabilities" },
    ],
  },
  {
    title: "Core resources",
    detail: "Build the workspace → project → environment → service hierarchy used by every tenant deployment.",
    endpoints: [
      { method: "GET", path: "/projects", operationId: "listProjects", summary: "List visible projects" },
      { method: "POST", path: "/projects", operationId: "createProject", summary: "Create a project" },
      { method: "GET", path: "/environments", operationId: "listEnvironments", summary: "List visible environments" },
      { method: "POST", path: "/environments", operationId: "createEnvironment", summary: "Create an environment" },
      { method: "GET", path: "/services", operationId: "listServices", summary: "List visible services" },
      { method: "POST", path: "/services", operationId: "createService", summary: "Create a service" },
    ],
  },
  {
    title: "Desired state",
    detail: "Stage, validate, and atomically apply runtime, write-only variable, generated-networking, and volume intent. Apply does not restart a workload.",
    endpoints: [
      { method: "GET", path: "/environments/{environment_id}/changeset", operationId: "getEnvironmentChangeset", summary: "Review the active durable draft" },
      { method: "PUT", path: "/environments/{environment_id}/changeset", operationId: "putEnvironmentChangeset", summary: "Create or replace the draft with CAS" },
      { method: "POST", path: "/environments/{environment_id}/changeset/validate", operationId: "validateEnvironmentChangeset", summary: "Revalidate resource authority and versions" },
      { method: "POST", path: "/environments/{environment_id}/changeset/apply", operationId: "applyEnvironmentChangeset", summary: "Atomically commit all draft items" },
      { method: "POST", path: "/environments/{environment_id}/changeset/discard", operationId: "discardEnvironmentChangeset", summary: "Discard without changing desired state" },
      { method: "POST", path: "/config/sync", operationId: "syncEnvironmentConfig", summary: "Stage a config-as-code document" },
      { method: "GET", path: "/services/{service_id}/environments/{environment_id}/config", operationId: "getServiceEnvironmentConfig", summary: "Read effective service configuration" },
      { method: "PUT", path: "/services/{service_id}/environments/{environment_id}/config", operationId: "putServiceEnvironmentConfig", summary: "Replace service configuration" },
      { method: "GET", path: "/services/{service_id}/environments/{environment_id}/variables", operationId: "listTenantServiceEnvironmentVariables", summary: "List redaction-safe variable metadata" },
      { method: "POST", path: "/services/{service_id}/environments/{environment_id}/variables", operationId: "createTenantServiceEnvironmentVariable", summary: "Create a write-only variable" },
      { method: "PATCH", path: "/services/{service_id}/environments/{environment_id}/variables/{variable_id}", operationId: "updateTenantServiceEnvironmentVariable", summary: "Rotate a variable with CAS" },
      { method: "DELETE", path: "/services/{service_id}/environments/{environment_id}/variables/{variable_id}", operationId: "deleteTenantServiceEnvironmentVariable", summary: "Tombstone a variable with CAS" },
    ],
  },
  {
    title: "Deploy and observe",
    detail: "Queue immutable inputs, then use the returned operation as the source of progress truth.",
    endpoints: [
      { method: "POST", path: "/services/{id}/deployments", operationId: "createServiceDeployment", summary: "Queue a digest-pinned image" },
      { method: "POST", path: "/services/{id}/up", operationId: "createServiceUp", summary: "Upload source and queue a build" },
      { method: "GET", path: "/services/{id}/deployments", operationId: "listServiceDeployments", summary: "List service deployments" },
      { method: "POST", path: "/services/{service_id}/deployments/{deployment_id}/actions/{action}", operationId: "createDeploymentAction", summary: "Queue an admitted deployment action" },
      { method: "GET", path: "/operations/{id}", operationId: "getOperation", summary: "Read operation state" },
      { method: "GET", path: "/operations/{id}/events", operationId: "streamOperationEvents", summary: "Stream operation events" },
      { method: "GET", path: "/operations/{id}/logs", operationId: "getOperationLogs", summary: "Read backed operation logs" },
    ],
  },
  {
    title: "Safe automation",
    detail: "Issue project-bound CI credentials and bind only workspace-granted GitHub repositories.",
    endpoints: [
      { method: "GET", path: "/projects/{project_id}/automation-tokens", operationId: "listProjectAutomationTokens", summary: "List token metadata" },
      { method: "POST", path: "/projects/{project_id}/automation-tokens", operationId: "createProjectAutomationToken", summary: "Mint a project automation token" },
      { method: "DELETE", path: "/projects/{project_id}/automation-tokens/{token_id}", operationId: "revokeProjectAutomationToken", summary: "Revoke an automation token" },
      { method: "GET", path: "/projects/{project_id}/github/repositories", operationId: "listProjectGitHubRepositories", summary: "List workspace-granted repositories" },
      { method: "PUT", path: "/services/{service_id}/github", operationId: "putServiceGitHubSourceLink", summary: "Bind a verified service source" },
    ],
  },
] as const;

const AUTHENTICATION_SAMPLE = `umask 077
LOGIN_FILE="$(mktemp)"
trap 'rm -f "$LOGIN_FILE"' EXIT

cat >"$LOGIN_FILE" <<'JSON'
{
  "email": "operator@example.com",
  "password": "<password>",
  "totp_code": "123456",
  "client_kind": "api"
}
JSON

# Replace the placeholders in the owner-only temporary file. Keep totp_code
# for an installation-administrator login, where MFA is mandatory.
\${EDITOR:-vi} "$LOGIN_FILE"

curl --fail-with-body \\
  -X POST "$MIDNIGHT_URL/api/v1alpha/auth/sessions" \\
  -H 'Content-Type: application/json' \\
  --data-binary "@$LOGIN_FILE"`;

const CREATE_PROJECT_SAMPLE = `curl --fail-with-body \\
  -X POST "$MIDNIGHT_URL/api/v1alpha/projects" \\
  -H "Authorization: Bearer $MIDNIGHT_TOKEN" \\
  -H 'Content-Type: application/json' \\
  -H "Idempotency-Key: $REQUEST_KEY" \\
  --data '{
    "workspace_id": "wrk_example",
    "slug": "payments",
    "display_name": "Payments"
  }'`;

const DEPLOY_SAMPLE = `curl --fail-with-body \\
  -X POST "$MIDNIGHT_URL/api/v1alpha/services/$SERVICE_ID/deployments" \\
  -H "Authorization: Bearer $MIDNIGHT_TOKEN" \\
  -H 'Content-Type: application/json' \\
  -H "Idempotency-Key: $REQUEST_KEY" \\
  --data "{
    \"environment_id\": \"$ENVIRONMENT_ID\",
    \"image\": \"registry.example/payments@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"
  }"`;

const FOLLOW_OPERATION_SAMPLE = `# Poll the authoritative state
curl --fail-with-body \\
  "$MIDNIGHT_URL/api/v1alpha/operations/$OPERATION_ID" \\
  -H "Authorization: Bearer $MIDNIGHT_TOKEN"

# Or follow the server-sent event stream
curl -N \\
  "$MIDNIGHT_URL/api/v1alpha/operations/$OPERATION_ID/events?after=0&stream=sse" \\
  -H "Authorization: Bearer $MIDNIGHT_TOKEN"`;

const CAS_SAMPLE = `# GET returns: ETag: "3"
curl --fail-with-body \\
  -X PATCH "$MIDNIGHT_URL/api/v1alpha/services/$SERVICE_ID/environments/$ENVIRONMENT_ID/variables/$VARIABLE_ID" \\
  -H "Authorization: Bearer $MIDNIGHT_TOKEN" \\
  -H 'Content-Type: application/json' \\
  -H "Idempotency-Key: $REQUEST_KEY" \\
  -H 'If-Match: "3"' \\
  --data '{"expected_version":3,"value":"<new-value>"}'`;

const PAGINATION_SAMPLE = `# Installation-admin collection. Default limit: 50; maximum: 200.
curl --get --fail-with-body \\
  "$MIDNIGHT_URL/api/v1alpha/operations" \\
  -H "Authorization: Bearer $MIDNIGHT_ADMIN_TOKEN" \\
  --data-urlencode 'limit=50' \\
  --data-urlencode 'status=RUNNING'

# Resume with the same filters and page size.
curl --get --fail-with-body \\
  "$MIDNIGHT_URL/api/v1alpha/operations" \\
  -H "Authorization: Bearer $MIDNIGHT_ADMIN_TOKEN" \\
  --data-urlencode 'limit=50' \\
  --data-urlencode 'status=RUNNING' \\
  --data-urlencode "cursor=$NEXT_CURSOR"`;

const PROBLEM_SAMPLE = `HTTP/1.1 412 Precondition Failed
Content-Type: application/problem+json

{
  "type": "https://midnight.dev/docs/errors#PRECONDITION_FAILED",
  "title": "Precondition failed",
  "status": 412,
  "detail": "If-Match does not match the current resource version.",
  "code": "PRECONDITION_FAILED",
  "request_id": "req_example",
  "docs_url": "https://midnight.dev/docs/errors#PRECONDITION_FAILED",
  "error": {
    "code": "PRECONDITION_FAILED",
    "message": "If-Match does not match the current resource version.",
    "request_id": "req_example",
    "docs_url": "https://midnight.dev/docs/errors#PRECONDITION_FAILED"
  }
}`;

const AUTHORITY_ROWS = [
  ["Human session", "midn_user_*", "Dashboard, CLI, and tenant product APIs admitted by current grants."],
  ["Project automation", "midn_sa_*", "Narrow CI deploy/up intake and same-project deployment or operation reads."],
  ["Installation admin", "midn_user_* / recovery", "Global installation APIs; recovery remains break-glass compatibility."],
  ["Runtime agent", "per-agent secret", "Only the exact agent registration, heartbeat, desired-state, and status routes."],
] as const;

export function APIReferenceDocs() {
  const endpointCount = FEATURED_ENDPOINT_GROUPS.reduce((sum, group) => sum + group.endpoints.length, 0);

  return (
    <DocsPage>
      <div id="overview" className="scroll-mt-20 grid gap-5">
          <DocsHero
            eyebrow="REST API"
            title="Practical API reference"
            description={`The shortest reliable path through Midnight's control plane. This guide highlights ${endpointCount} high-value operations; the served OpenAPI document remains the complete and authoritative contract.`}
            badges={["v1alpha", "/api/v1alpha", "RFC 9457 errors"]}
          />
          <DefinitionGrid
            items={[
              { term: "Base path", detail: "/api/v1alpha", code: true },
              { term: "Request data", detail: "application/json", code: true },
              { term: "Errors", detail: "application/problem+json", code: true },
              {
                term: "Canonical contract",
                detail: (
                  <a href="/api/v1alpha/openapi.yaml" className="font-semibold text-accent hover:underline">
                    Download served OpenAPI ↓
                  </a>
                ),
              },
            ]}
          />
          <Callout title="Alpha means contract evolution" tone="warning">
            <p className="m-0">
              Generate clients from <code>/api/v1alpha/openapi.yaml</code> on the installation you
              call. Midnight is Railway-inspired, but it is not a Railway-compatible or drop-in API.
            </p>
          </Callout>
      </div>

        <ReferenceSection id="authentication" eyebrow="Access" title="Authenticate with one explicit credential class">
          <p>
            Send the raw credential as <code>Authorization: Bearer &lt;token&gt;</code>. A route accepts
            only the security schemes declared on its OpenAPI operation; credentials do not widen or
            fall back to another authority class.
          </p>
          <div className="overflow-x-auto rounded-lg border border-line">
            <table className="w-full min-w-[42rem] border-collapse text-left text-xs">
              <thead className="bg-bg text-muted">
                <tr>
                  <th className="border-b border-line px-4 py-3 font-semibold">Credential</th>
                  <th className="border-b border-line px-4 py-3 font-semibold">Shape</th>
                  <th className="border-b border-line px-4 py-3 font-semibold">Intended authority</th>
                </tr>
              </thead>
              <tbody>
                {AUTHORITY_ROWS.map(([name, shape, purpose]) => (
                  <tr key={name} className="border-b border-line-soft last:border-0">
                    <th scope="row" className="px-4 py-3 font-semibold text-text">{name}</th>
                    <td className="px-4 py-3 font-mono text-[11px] text-accent">{shape}</td>
                    <td className="px-4 py-3 leading-relaxed text-muted">{purpose}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <CommandBlock label="Create a human API session" code={AUTHENTICATION_SAMPLE} />
          <p className="text-xs">
            The access token is returned once. Treat login files and the response as secrets; do not
            commit them or place raw credentials in logs.
          </p>
        </ReferenceSection>

        <ReferenceSection id="first-request" eyebrow="Workflow" title="Create, deploy, then observe">
          <p>
            Tenant mutations derive their workspace and project authority from durable server state.
            Supply opaque IDs returned by the API instead of reconstructing them from slugs.
          </p>
          <div className="grid gap-4">
            <CommandBlock label="1 · Create a project" code={CREATE_PROJECT_SAMPLE} />
            <CommandBlock label="2 · Queue a private image deployment" code={DEPLOY_SAMPLE} />
            <CommandBlock label="3 · Follow the returned operation" code={FOLLOW_OPERATION_SAMPLE} />
          </div>
          <Callout title="202 Accepted is not deployment success" headingLevel={3}>
            <p className="m-0">
              A queued response proves durable intake only. Wait for the operation to reach
              <code> SUCCEEDED</code>, then inspect deployment, health, and route evidence separately.
            </p>
          </Callout>
        </ReferenceSection>

        <ReferenceSection id="reliability" eyebrow="Retries and concurrency" title="Make mutations safe under ambiguity">
          <div className="grid gap-4 sm:grid-cols-2">
            <ReliabilityCard title="Idempotency-Key" badge="retry identity">
              Generate a stable opaque key for one logical mutation. Retry the same concrete route and
              exact body with that key. Reusing it with a different body fails; an authorized replay
              returns the stored terminal status, body, and selected headers with
              <code> Idempotent-Replayed: true</code>.
            </ReliabilityCard>
            <ReliabilityCard title="ETag + If-Match" badge="state identity">
              For operations that advertise conditional writes, read the strong ETag from the exact
              item GET. Send it in <code>If-Match</code> together with the matching
              <code> expected_version</code>. A missing validator returns 428; a stale, weak, malformed,
              or mismatched validator returns 412.
            </ReliabilityCard>
          </div>
          <CommandBlock label="Conditional variable rotation" code={CAS_SAMPLE} />
          <p className="text-xs">
            Fetch again after a 412 and reconcile the new representation before retrying. Do not
            automatically overwrite changes made by another actor.
          </p>
        </ReferenceSection>

        <ReferenceSection id="pagination" eyebrow="Collections" title="Treat cursors as opaque, scoped capabilities">
          <p>
            Paginated collections return <code>{`{"data": [...], "page": {"next_cursor": "..."}}`}</code>.
            A cursor is integrity-protected and bound to the authenticated actor, tenant, collection,
            filters, ordering, and page size. Resume it only with the same query; a null cursor means
            the collection is exhausted.
          </p>
          <p>
            The operation-list example below is an installation-admin surface. Ordinary human and
            project-automation credentials can read an exact authorized operation by ID, but they
            cannot enumerate the installation-wide operation collection.
          </p>
          <CommandBlock label="Resume an operation query" code={PAGINATION_SAMPLE} />
          <p className="text-xs">
            Operation pages also return <code>RateLimit-Limit</code>, <code>RateLimit-Remaining</code>,
            and <code>RateLimit-Reset</code>. Honor <code>Retry-After</code> when a route supplies it.
          </p>
        </ReferenceSection>

        <ReferenceSection id="errors" eyebrow="Failures" title="Branch on stable problem codes">
          <p>
            Non-success JSON responses use{" "}
            <a
              href="https://www.rfc-editor.org/rfc/rfc9457.html"
              target="_blank"
              rel="noopener noreferrer"
              className="font-semibold text-accent hover:underline"
            >
              RFC 9457 problem details
            </a>
            . Use the HTTP status for generic
            behavior and <code>code</code> or <code>type</code> for Midnight-specific handling. Keep
            <code>request_id</code> for support; never parse the human-readable <code>detail</code> field.
          </p>
          <CommandBlock label="Precondition failure" code={PROBLEM_SAMPLE} />
          <Callout
            title="The nested error object is alpha compatibility only"
            tone="warning"
            headingLevel={3}
          >
            <p className="m-0">
              v1alpha repeats legacy fields under <code>error</code>. New clients should read the
              top-level RFC 9457 members; the stable v1 projection omits the nested envelope.
            </p>
          </Callout>
        </ReferenceSection>

        <ReferenceSection id="endpoint-map" eyebrow="Curated surface" title="The operations most integrations need">
          <p>
            This map is intentionally selective. Follow the canonical OpenAPI for request schemas,
            exact security alternatives, response headers, and the rest of the control-plane surface.
          </p>
          <div className="grid gap-4 xl:grid-cols-2">
            {FEATURED_ENDPOINT_GROUPS.map((group) => (
              <DocsCard
                key={group.title}
                title={group.title}
                description={group.detail}
                headingLevel={3}
              >
                <ul className="m-0 grid list-none gap-1 p-0">
                  {group.endpoints.map((endpoint) => (
                    <li key={endpoint.operationId} className="grid gap-1 rounded-[var(--r-sm)] border border-transparent px-2 py-2 hover:border-line hover:bg-bg">
                      <div className="flex min-w-0 items-center gap-2">
                        <MethodBadge method={endpoint.method} />
                        <code className="min-w-0 overflow-x-auto whitespace-nowrap text-[11px] text-text">{endpoint.path}</code>
                      </div>
                      <span className="pl-[4.4rem] text-[11px] leading-relaxed text-muted">{endpoint.summary}</span>
                    </li>
                  ))}
                </ul>
              </DocsCard>
            ))}
          </div>
        </ReferenceSection>

        <footer className="flex flex-wrap items-center justify-between gap-3 border-t border-line py-6 text-xs text-muted">
          <span>Contract: Midnight API v1alpha</span>
          <span>Canonical document: <code>/api/v1alpha/openapi.yaml</code></span>
        </footer>
    </DocsPage>
  );
}

function ReferenceSection({
  id,
  eyebrow,
  title,
  children,
}: {
  id: string;
  eyebrow: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <section id={id} className="scroll-mt-20 grid gap-5 text-sm leading-7 text-muted [&>p]:m-0 [&_code]:font-mono [&_code]:text-[0.9em] [&_code]:text-text">
      <header className="grid gap-2">
        <p className="m-0 font-mono text-[11px] font-semibold uppercase tracking-wider text-accent">{eyebrow}</p>
        <h2 className="m-0 text-2xl font-semibold tracking-tight text-text">{title}</h2>
      </header>
      {children}
    </section>
  );
}

function ReliabilityCard({
  title,
  badge,
  children,
}: {
  title: string;
  badge: string;
  children: ReactNode;
}) {
  return (
    <article className="grid content-start gap-3 rounded-lg border border-line bg-surface p-4">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="m-0 text-sm font-semibold text-text">{title}</h3>
        <span className="rounded-full border border-line-strong bg-bg px-2 py-0.5 font-mono text-[9px] uppercase tracking-wide text-muted">
          {badge}
        </span>
      </header>
      <p className="m-0 text-xs leading-relaxed text-muted">{children}</p>
    </article>
  );
}

function MethodBadge({ method }: { method: HTTPMethod }) {
  const styles: Record<HTTPMethod, string> = {
    GET: "border-info/35 bg-info-soft text-info",
    POST: "border-accent/35 bg-accent-soft text-accent",
    PUT: "border-warn/35 bg-warn-soft text-warn",
    PATCH: "border-[var(--branch)]/35 bg-[var(--branch-soft)] text-[var(--branch)]",
    DELETE: "border-danger/35 bg-danger-soft text-danger",
  };
  return (
    <span
      className={`inline-flex w-[3.9rem] shrink-0 justify-center rounded border px-1.5 py-0.5 font-mono text-[9px] font-bold tracking-[0.08em] ${styles[method]}`}
    >
      {method}
    </span>
  );
}
