import {
  Callout,
  CommandBlock,
  DefinitionGrid,
  DocsCard,
  DocsHero,
  DocsPage,
} from "./DocsContent";

const RELEASE_INSTALL = `# Replace VERSION and the archive name with the release you downloaded.
cosign verify-blob midnight_VERSION_SHA256SUMS \\
  --bundle midnight_VERSION_SHA256SUMS.sigstore.json \\
  --certificate-identity-regexp '^https://github\\.com/packetloss404/midnight/\\.github/workflows/release\\.yml@refs/tags/v[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check midnight_VERSION_SHA256SUMS --ignore-missing
tar -xzf midnight_VERSION_linux_amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 midnight "$HOME/.local/bin/midnight"
midnight version`;

const HUMAN_WORKFLOW = `midnight login --api-base https://api.midnight.example --email you@example.com
midnight whoami

cd path/to/your/project
midnight link
midnight status
midnight up
midnight logs -f --build`;

const AUTOMATION_WORKFLOW = `# Create from an authenticated project context. The raw value is shown once.
midnight token create --name ci-deploy

# Supply both the project-scoped credential and its explicit endpoint to the
# automation process. This avoids mixing an environment token with a linked endpoint.
MIDNIGHT_API_BASE=https://api.midnight.example \\
MIDNIGHT_TOKEN=midn_sa_... \\
midnight up --detach`;

const CHANGESET_WORKFLOW = `# Stage a strict ID-bound config-as-code document. This does not apply or deploy it.
midnight config sync --file production-sync.json \
  --idempotency-key sync-production-20260830

# Review and revalidate the exact durable revision.
midnight changeset get --environment-id env_01H...
midnight changeset validate --environment-id env_01H... --revision 1

# Apply desired state atomically. Running containers are unchanged.
midnight changeset apply --environment-id env_01H... --revision 1 \
  --idempotency-key apply-production-20260830`;

const COMMAND_FAMILIES = [
  { term: "login · logout · whoami", detail: "Create, revoke, and inspect the durable human session." },
  { term: "link · unlink · status · open", detail: "Manage the current directory's project, environment, and service context." },
  { term: "up · logs · redeploy · rollback", detail: "Upload the current source, follow logs, or replay an existing deployment." },
  { term: "restart · stop · remove · cancel", detail: "Control or cancel deployment work in the linked context." },
  { term: "project · service · environment", detail: "List and create resources; service also owns config and GitHub source commands." },
  { term: "variable · domain · tcp-proxy", detail: "Manage exact service variables, generated/custom domains, and TCP endpoints." },
  { term: "deploy · observability", detail: "Deploy immutable images and inspect operation events, logs, and service metrics." },
  { term: "usage", detail: "Inspect workspace usage, policy limits, evidence coverage, and source freshness." },
  { term: "volume · template", detail: "Create persistent volumes, backups/restores/policies, and official template deployments." },
  { term: "token · invitation", detail: "Manage project automation credentials and workspace invitation lifecycles." },
  { term: "monitor · cron", detail: "Manage monitors/channels/deliveries and inspect cron occurrences." },
  { term: "config", detail: "Validate, diff, apply, or synchronize a config-as-code document." },
  { term: "changeset", detail: "Review, validate, atomically apply, or discard a durable environment draft." },
  { term: "audit · admin", detail: "Verify audit evidence or perform explicitly operator-scoped recovery and maintenance." },
];

export function CLIReferenceDocs() {
  return (
    <DocsPage>
      <DocsHero
        eyebrow="Command reference"
        title="Midnight CLI"
        description="Deploy and operate applications from a terminal with the same tenant boundaries as the console. Start with a durable human session, link the current repository, then use project-scoped credentials for unattended deploys."
        badges={["midnight", "human sessions", "project links", "CI tokens"]}
      />

      <Callout title="Choose the narrowest credential">
        <p className="m-0">
          Interactive work should use <code>midnight login</code>. Automation should use a
          project-scoped <code>midn_sa_</code> token. The installation-wide recovery flow is kept
          separate under <code>midnight admin authorize-cli</code> and is never a silent fallback for
          developer commands.
        </p>
      </Callout>

      <DocsCard
        title="Install the CLI"
        description="Release archives include the binary, license, CLI documentation, checksums, and static shell completions."
        label="Release path"
      >
        <CommandBlock label="Linux archive" code={RELEASE_INSTALL} />
        <div className="grid gap-2 text-xs leading-relaxed text-muted sm:grid-cols-2">
          <p className="m-0 rounded-lg border border-line bg-surface-2 p-3">
            On macOS, use <code>shasum -a 256 --check</code>. On Windows, compare
            <code> Get-FileHash -Algorithm SHA256</code> with the matching checksum line before
            extracting the ZIP.
          </p>
          <p className="m-0 rounded-lg border border-line bg-surface-2 p-3">
            Homebrew, Scoop, and npm publication remain operator-gated. Protected GitHub tag
            archives include a Sigstore bundle; workflow-dispatch smoke artifacts do not.
          </p>
        </div>
        <div className="grid gap-2 border-t border-line pt-3">
          <h3 className="m-0 text-xs font-semibold text-text">Source installation</h3>
          <p className="m-0 text-xs leading-relaxed text-muted">
            For contributors or environments that intentionally trust a local Go toolchain:
          </p>
          <CommandBlock
            label="Source build"
            code={'go install github.com/midnight/midnight/cmd/midnight@latest\nmidnight version'}
          />
        </div>
      </DocsCard>

      <DocsCard
        title="Login, link, deploy"
        description="Run link and up from the repository directory you intend to deploy."
        label="Human workflow"
      >
        <CommandBlock label="First deploy" code={HUMAN_WORKFLOW} />
        <ol className="m-0 grid gap-2 pl-5 text-xs leading-relaxed text-muted">
          <li>
            <code>login</code> securely prompts for the password and TOTP when required. Remote login
            requires HTTPS; plain HTTP is accepted only for loopback development.
          </li>
          <li>
            <code>link</code> selects an exact project, environment, and service and writes
            <code> .midnight/project.json</code> in the current directory.
          </li>
          <li>
            <code>up</code> honors <code>.gitignore</code> and <code>.midnightignore</code>, creates a
            deterministic compressed upload intent, and follows operation events unless
            <code> --detach</code> is supplied.
          </li>
        </ol>
      </DocsCard>

      <DocsCard
        title="Authentication and local state"
        description="Endpoint and credential resolution are deterministic; ambiguous stored servers fail instead of being guessed."
      >
        <DefinitionGrid
          items={[
            {
              term: "Credential precedence",
              detail: <><code>--token</code> → <code>MIDNIGHT_TOKEN</code> → <code>MIDNIGHT_API_TOKEN</code> → stored server credential.</>,
            },
            {
              term: "Endpoint precedence",
              detail: <><code>--api-base</code> → <code>MIDNIGHT_API_BASE</code> → legacy base URL → current project link → one unambiguous stored server.</>,
            },
            {
              term: "~/.midnight/config.json",
              detail: "Per-server endpoint, identity, and opaque credential-reference metadata. Normal desktop secrets live in the OS keyring.",
            },
            {
              term: "./.midnight/project.json",
              detail: "Repository-local link to the exact API, project, environment, and service. It contains no bearer credential.",
            },
            {
              term: "MIDNIGHT_CONFIG_HOME",
              detail: "Explicit headless/test fallback. It stores credentials in an owner-only file and is never selected automatically after keyring failure.",
            },
            {
              term: "midnight logout",
              detail: "Revokes the human session remotely before local deletion. Network failure preserves local state; --local-only deliberately skips revocation.",
            },
          ]}
        />
      </DocsCard>

      <DocsCard
        title="Project automation"
        description="Automation credentials are bound to one project and cannot manage installation settings or operator routes."
        label="CI"
      >
        <CommandBlock label="Scoped deploy token" code={AUTOMATION_WORKFLOW} />
        <p className="m-0 text-xs leading-relaxed text-muted">
          Creation defaults to a 30-day lifetime and reveals the raw token once. Keep an unchanged
          request body and idempotency key when retrying an uncertain mutation; changing intent with
          the same key is rejected.
        </p>
      </DocsCard>

      <DocsCard
        title="Review and apply environment changes"
        description="Browser edits and config sync converge on one durable revision for variables, runtime, generated networking, and volumes."
        label="Atomic desired state"
      >
        <CommandBlock label="Config sync and changeset" code={CHANGESET_WORKFLOW} />
        <p className="m-0 text-xs leading-relaxed text-muted">
          Apply requires every resource-specific capability represented in the draft and rechecks
          current versions in one transaction. Use <code>midnight changeset discard</code> with the
          active revision and a fresh idempotency key to abandon a draft.
        </p>
      </DocsCard>

      <DocsCard
        title="Command families"
        description="Use midnight <command> --help for the flags and positional arguments accepted by the installed version."
      >
        <DefinitionGrid items={COMMAND_FAMILIES} />
      </DocsCard>

      <DocsCard
        title="Global options"
        description="Framework options may be placed before a command. Supported options at the start of a leaf command are also consumed consistently."
      >
        <DefinitionGrid
          items={[
            { term: "--output, -o", detail: "table (default), json, jsonl, or name." },
            { term: "--quiet, -q", detail: "Print only the primary result." },
            { term: "--no-input", detail: "Never prompt. Pair with complete flags for CI." },
            { term: "--yes, -y", detail: "Approve confirmation prompts explicitly." },
            { term: "--timeout", detail: "Maximum command duration; zero uses the command default." },
            { term: "--debug", detail: "Enable diagnostic output. Do not publish output without reviewing it for sensitive context." },
            { term: "--no-color", detail: "Disable terminal color sequences." },
            { term: "version · completion", detail: "Print the build version or generate Bash, Zsh, Fish, and PowerShell completion scripts." },
          ]}
        />
      </DocsCard>

      <Callout title="Secret input and safe retries" tone="warning">
        <ul className="m-0 grid gap-1.5 pl-5">
          <li>
            Prefer <code>--stdin</code> for variable values. <code>--value</code> can expose a secret
            through shell history and process listings.
          </li>
          <li>
            Compare-and-swap mutations require the current resource version. Refresh rather than
            overwriting a stale editor.
          </li>
          <li>
            A retry may reuse an idempotency key only with the exact same route and body. If the
            intended mutation changes, use a new key.
          </li>
        </ul>
      </Callout>
    </DocsPage>
  );
}
