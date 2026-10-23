# systemd Units

S02 owns Phase 0 systemd units for Midnight services.

Phase 0 installs seven managed service units:

- `midnight-server.service`: API/control-plane process.
- `midnight-worker.service`: background job worker.
- `midnight-agent.service`: node-local runtime agent.
- `midnight-buildkit.service`: BuildKit daemon used for image builds.
- `midnight-buildkit-worker.service`: source build queue consumer.
- `midnight-registry.service`: local OCI registry used by the platform.
- `midnight-github-status-worker.service`: optional outbound GitHub deployment
  status publisher; operators enable it only when the GitHub App is configured.

Units read common configuration from `/etc/midnight/midnight.env` and keep
mutable state under `/var/lib/midnight`. The service binaries are expected in
`/usr/bin` for the package skeleton; later packaging work may replace these
paths with generated install-time values.

The single-server Docker runtime profile starts `midnight-worker.service` as
the `midnight` user with supplementary `docker` group access. That is required
for the worker's Docker runtime backend and should be treated as host-root
equivalent access.
