# Debian Packaging

S02 owns `.deb` package configuration and maintainer scripts.

## Version and provenance contract

Private alpha maintenance packages use a Debian revision independently from
the product version. For example, package `0.1.0~alpha-6` is the sixth Debian
build of product version `0.1.0-alpha`; every shipped binary must report the
latter. A package build is accepted only from a clean tracked git commit. Each
binary records the full commit, commit time, and clean-tree state in Go build
metadata, while the server exposes the same commit and date through its
compatibility response.

`make package-deb` retains one complete binary artifact set under
`build/deb/`: the installable `.deb`, the generated debug-symbol package
(`.deb` on Debian or `.ddeb` on Ubuntu), and the `.changes` and `.buildinfo`
manifests that reference both payloads. These private maintenance artifacts are unsigned. Public
distribution still requires the protected signing workflow and the release
verification policy in `docs/release-verify.md`.

Alpha package layout:

- `/etc/midnight/midnight.env`: shared service configuration.
- `/etc/midnight/kek`: 32-byte variable-encryption key, owned by `midnight`
  with mode `0400`.
- `/etc/midnight/automation-token.key`: independent verifier key for
  project-scoped automation credentials, owned by `root:midnight` with mode
  `0440`.
- `/etc/midnight/invitation.key`: independent verifier key for one-time
  identity invitation credentials, owned by `root:midnight` with mode `0440`.
- `/etc/midnight/variable-intent.key`: independent HMAC key for value-bearing
  tenant-variable idempotency fingerprints, owned by `root:midnight` with mode
  `0440`.
- `/etc/midnight/registry.yml`: local OCI registry configuration.
- `/usr/bin/midnight-recovery`: offline encrypted platform backup and
  disaster-restore utility (uses `pg_dump`/`pg_restore` from
  `postgresql-client`).
- `/usr/bin/midnight-github-status-worker`: optional leased GitHub deployment
  status publisher; enable its systemd unit only after the GitHub App settings
  and `statuses:write` permission are configured.
- `/etc/midnight/traefik/dynamic`: Traefik file-provider route publication root.
- `/etc/docker/daemon.json`: installer-managed merge target for Docker daemon defaults.
- `/usr/lib/systemd/system/midnight-*.service`: service units from `deploy/systemd`.
- `/usr/share/doc/midnight/README.Debian`: package notes and operational defaults.

The package must create the `midnight` system user/group, create
`/var/lib/midnight`, `/var/log/midnight`, `/run/midnight`, and the configured
Traefik dynamic route directory, install default configuration when absent,
generate the KEK, API bearer token, cursor key, automation-token verifier key,
invitation verifier key, and variable-intent key, run available migrations when a DSN is
configured, run `midnight-doctor --json --install-readiness --live`, then run
`systemctl daemon-reload`.

## One-time alpha-2 upgrade bridge

`0.1.0~alpha-2` predates the maintainer-script active-unit snapshot. Upgrade
that exact installed revision only through the source-tree bridge, passing the
already-built package as its sole argument:

```sh
sudo ./deploy/debian/upgrade-alpha2-bridge \
  "$(pwd)/build/deb/midnight_0.1.0~alpha-6_amd64.deb"
```

The helper refuses non-root execution, symlink package inputs, a package other
than `midnight`, and any installed version other than exactly
`0.1.0~alpha-2`. Immediately before it replaces itself with `dpkg`, it records
only the fixed Midnight unit allowlist that `systemctl is-active` reports as
active. It never treats enabled units as active and never accepts caller-chosen
unit names. The new postinst stops the complete fixed unit set in one systemd
transaction before migration, then restarts only the captured active set after
configuration, migration, Doctor, and `systemctl daemon-reload` succeed.

If package configuration, daemon reload, or service restart fails, do not edit
or delete `/run/midnight-package-upgrade/active-units`. Correct the reported
problem and resume configuration:

```sh
sudo dpkg --configure -a
```

The snapshot is mode `0600`, owned by root, validated against the fixed
allowlist on every retry, and removed only after the complete saved set starts
successfully. A retry therefore preserves the alpha-2 pre-upgrade active set
even though those services are stopped. For deliberate rollback after an
unrecoverable unpack failure, repair the package state first and use the same
validated snapshot to identify which units were previously active; never use
`systemctl is-enabled` as a substitute.

Service enablement remains an explicit operator step. The post-install report
is labeled `live=true` and `profile=install-readiness`; it contains exactly
`OR-CHK-DOC-010`, `013`, `015`, `018`, and `019`. Host checks are deliberately
outside that profile, not reported as if they ran. Operators should review the
report, rerun the full doctor with explicit host fixtures/hints, configure route
host/TLS values and the worker Docker runtime, and only then run:

```sh
systemctl enable --now midnight-server midnight-worker midnight-agent midnight-buildkit midnight-buildkit-worker midnight-registry
```
