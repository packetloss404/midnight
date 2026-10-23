import {
  Callout,
  CommandBlock,
  DefinitionGrid,
  DocsCard,
  DocsHero,
  DocsPage,
} from "./DocsContent";

const PACKAGE_INSTALL = `# Verify the downloaded release bundle checksum before extracting it.
RELEASE_BUNDLE="midnight-release-<version>.tar.gz"
EXPECTED_SHA256="$(awk 'NR == 1 { print $1 }' "$RELEASE_BUNDLE.sha256")"
printf '%s  %s\\n' "$EXPECTED_SHA256" "$RELEASE_BUNDLE" | sha256sum --check -
mkdir "midnight-release-<version>"
tar -xzf "$RELEASE_BUNDLE" -C "midnight-release-<version>"
cd "midnight-release-<version>"

# Stop unless the repository and project website publish the same real,
# non-placeholder signing fingerprint. Import that exact trusted key first.
gpg --keyserver hkps://keys.openpgp.org \\
  --recv-keys <PUBLISHED_RELEASE_FINGERPRINT>
gpg --verify \\
  midnight_<version>_<arch>.deb.asc \\
  midnight_<version>_<arch>.deb

sudo apt-get update
sudo apt-get install ./midnight_<version>_<arch>.deb
sudoedit /etc/midnight/midnight.env`;

const START_SERVICES = `sudo systemctl enable --now \\
  midnight-server midnight-worker midnight-agent midnight-buildkit \\
  midnight-buildkit-worker midnight-registry

sudo systemctl --no-pager --full status midnight-agent
sudo journalctl -u midnight-agent --since "10 minutes ago" --no-pager`;

const LOCAL_AGENT = `# The checked-in Compose stack opts its Postgres server into development auth.
docker compose -f docker-compose.local.yml up -d

# Run the host agent only on a development machine with Docker and cgroup v2.
MIDNIGHT_AGENT_HMAC_SECRET=dev-agent-hmac-secret \\
MIDNIGHT_SERVER_URL=http://127.0.0.1:8080 \\
go run ./cmd/midnight-agent`;

export function AgentSetupDocs() {
  return (
    <DocsPage>
      <DocsHero
        eyebrow="Runtime guide"
        title="Set up a Midnight agent"
        description="Connect a Docker host to the control plane with an identity scoped to that exact agent. The packaged single-host path provisions credentials and service wiring; manual configuration is for additional hosts and controlled development."
        badges={["per-agent credential", "Docker Engine", "cgroup v2", "systemd"]}
      />

      <Callout title="Treat the agent as host administration" tone="warning">
        <p className="m-0">
          The agent runs as root in the packaged systemd unit and talks to Docker. Docker API access is
          host-root equivalent. Install it only on trusted hosts, keep its credential owner-readable,
          and never substitute Midnight&apos;s installation-wide recovery bearer.
        </p>
      </Callout>

      <DocsCard
        title="Packaged single-host installation"
        description="Recommended production path for the supported Debian layout. The package configures files, but deliberately does not start services."
        label="Production"
      >
        <ol className="m-0 grid gap-3 pl-5 text-xs leading-relaxed text-muted">
          <li>
            Verify the release bundle checksum, cross-check the release-key fingerprint published by
            the repository and project website, then verify the detached package signature before
            running anything as root. The current repository fingerprint file is an untrusted
            placeholder, so stop until a real matching fingerprint is published. After verification,
            install the package and review the shared environment file. Supply the Postgres DSN,
            pinned BuildKit and registry binaries, image trust policy, and route settings required by
            your installation.
          </li>
          <li>
            Confirm the installer-created <code>/etc/midnight/agent.credential</code> and
            <code> /etc/midnight/agent-credentials.json</code> exist. The first is read only by the
            local agent; the second is the server keyring. They are independent of
            <code> MIDNIGHT_API_BEARER_TOKEN</code>.
          </li>
          <li>
            Review <code>/var/lib/midnight/doctor-postinst.json</code>, rerun
            <code> midnight-doctor --json --live</code> with the host evidence required by your
            environment, and do not enable services while any fatal check remains.
          </li>
        </ol>
        <CommandBlock label="Install and review" code={PACKAGE_INSTALL} />
      </DocsCard>

      <DocsCard
        title="Agent configuration"
        description="The systemd unit reads /etc/midnight/midnight.env. These are the agent-owned settings and transport controls."
      >
        <DefinitionGrid
          items={[
            {
              term: "MIDNIGHT_AGENT_ID",
              code: true,
              detail: "Stable host identity. The package defaults to local-agent; every additional host needs a distinct ID.",
            },
            {
              term: "MIDNIGHT_SERVER_URL",
              code: true,
              detail: "Control-plane origin. The loopback HTTP default is local-only; use HTTPS for a remote or production server.",
            },
            {
              term: "MIDNIGHT_AGENT_CREDENTIAL_FILE",
              code: true,
              detail: "Preferred agent-local bearer/signing credential. Group or other permission bits cause agent startup to fail.",
            },
            {
              term: "MIDNIGHT_RUNTIME_ROOT",
              code: true,
              detail: "Agent state root. The package sets /var/lib/midnight/agent.",
            },
            {
              term: "--ca-bundle",
              code: true,
              detail: "PEM CA bundle for a private control-plane certificate authority.",
            },
            {
              term: "--client-cert / --client-key",
              code: true,
              detail: "Optional mTLS client identity. Configure both together.",
            },
          ]}
        />
        <p className="m-0 text-xs leading-relaxed text-muted">
          <code>midnight-agent --dry-run</code> validates base configuration and exits without
          dialing the control plane. It does not load the credential, probe Docker, or prove network
          admission; use it only as an early syntax check.
        </p>
      </DocsCard>

      <DocsCard
        title="Start and verify"
        description="The production boot path fails closed on missing credentials, unsupported Docker, runtime recovery failure, or rejected registration."
        label="After doctor passes"
      >
        <CommandBlock label="Enable and inspect" code={START_SERVICES} />
        <DefinitionGrid
          items={[
            { term: "1 · Host gate", detail: "The agent requires cgroup v2 and a reachable, supported Docker Engine." },
            { term: "2 · Recovery", detail: "It opens the Docker adapter and reconciles previously owned runtime state before registration." },
            { term: "3 · Registration", detail: "It authenticates over the configured transport as the exact MIDNIGHT_AGENT_ID." },
            { term: "4 · Control loop", detail: "It heartbeats every five seconds, polls desired state, reconciles, and reports signed observations until SIGTERM." },
          ]}
        />
      </DocsCard>

      <DocsCard
        title="Add or rotate a host"
        description="Manual multi-host setup must keep server and agent material aligned without sharing credentials between identities."
        label="Production"
      >
        <div className="grid gap-3 text-xs leading-relaxed text-muted md:grid-cols-2">
          <div className="grid content-start gap-2 rounded-lg border border-line bg-surface-2 p-3">
            <h3 className="m-0 text-xs font-semibold text-text">Server keyring</h3>
            <p className="m-0">
              Add at least 32 random bytes for the new ID to the JSON file named by
              <code> MIDNIGHT_AGENT_CREDENTIALS_FILE</code>. Production Postgres boot fails when a
              real, non-empty keyring is missing or weak.
            </p>
            <CommandBlock
              label="Keyring shape"
              code={'{\n  "version": 1,\n  "agents": {\n    "worker-a": ["<at-least-32-random-bytes>"]\n  }\n}'}
            />
          </div>
          <div className="grid content-start gap-2 rounded-lg border border-line bg-surface-2 p-3">
            <h3 className="m-0 text-xs font-semibold text-text">Agent host</h3>
            <p className="m-0">
              Install only that host&apos;s value in its credential file with mode 0400. Set the
              matching agent ID and HTTPS server URL, then restart the server before starting the
              new agent.
            </p>
            <p className="m-0">
              For rotation, temporarily list old and new values for the same ID, restart the server,
              update the agent, then remove the old value. A credential for one ID cannot cross into
              another agent&apos;s requests.
            </p>
          </div>
        </div>
      </DocsCard>

      <DocsCard
        title="Local development compatibility"
        description="The checked-in Compose stack opts in explicitly. This mode is not suitable for a deployed or multi-host installation."
        label="Developer only"
      >
        <CommandBlock label="Local repository" code={LOCAL_AGENT} />
        <ul className="m-0 grid gap-1.5 pl-5 text-xs leading-relaxed text-muted">
          <li>
            <code>MIDNIGHT_AGENT_AUTH_DEVELOPMENT=1</code> is a server-side escape hatch for a
            Postgres development stack; memory storage automatically uses development credentials.
          </li>
          <li>
            <code>MIDNIGHT_AGENT_HMAC_SECRET</code> and <code>--hmac-secret</code> are shared-secret
            compatibility inputs for local development only.
          </li>
          <li>
            <code>--insecure</code> skips TLS verification and is development-only. It emits a warning
            when used with HTTPS.
          </li>
        </ul>
      </DocsCard>
    </DocsPage>
  );
}
