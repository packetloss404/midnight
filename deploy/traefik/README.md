# Midnight + Traefik file-provider contract (Phase 1 Lane 3)

This directory ships the operator-facing pieces that wire Midnight's control
plane to a single, single-active-instance Traefik deployment.

## Directory contract

| Path                                  | Owner              | Mode  | Purpose |
|---------------------------------------|--------------------|-------|---------|
| `/etc/traefik/traefik.yaml`           | operator           | 0644  | Static config (entryPoints, providers, ACME resolvers). Source of this file is `traefik.yaml` next to this README. |
| `/etc/midnight/traefik/dynamic/`      | midnight (uid)     | 0750  | Watched by Traefik's file provider. Midnight is the **only** process that writes here. |
| `/etc/midnight/traefik/dynamic/<group>[.<route>].traefik.dynamic.yaml` | midnight | 0640  | Generated route artifact, one per route. Flat, not nested: Traefik watches only this directory and would never re-read a subdirectory file. Written via `os.Rename` atomic swap. |
| `/var/lib/midnight/acme/`             | midnight           | 0700  | ACME state. `acme.staging.json` and `acme.prod.json` (`0600`) are backed up byte-exact (ADR-007 line 154). |
| `/var/log/midnight/traefik/access.jsonl` | Traefik writes; midnight reads | 0640/0644 | Unbuffered JSON access log tailed by `midnight-server` when `MIDNIGHT_TRAEFIK_ACCESS_LOG_PATH` is set. |

The pinned dynamic directory `/etc/midnight/traefik/dynamic` is the **default
publication root** for `internal/router.LocalRouteArtifactPublicationWriter`
when wired through `internal/router.NewTraefikDynamicPublicationDirectory`.
The directory itself is created by the installer; Midnight refuses to write
artifacts to a directory it does not own.

The Debian installer creates `/var/log/midnight/traefik` as
`root:midnight 2770`. The setgid bit ensures access logs created or replaced by
Traefik inherit group `midnight`; a system Traefik unit must run as root or
include `SupplementaryGroups=midnight`. Container deployments must mount the
same log volume read-write in Traefik and read-only in `midnight-server`. Do not
point the server at stdout-only access logging: restart-safe collection relies
on the file inode and offset checkpoint.

## Worker environment contract

The packaged single-server profile sets
`MIDNIGHT_WORKER_ROUTE_CONFIG_DIR=/etc/midnight/traefik/dynamic`, matching the
file-provider directory above. Operators also configure
`MIDNIGHT_APPS_DOMAIN`, the trusted Traefik API address, the trusted ingress
probe address, and the explicit Traefik container name. Public intent comes
from the deploy/domain API; a fixed worker-wide public host is no longer the
source of truth. The controller reports `ready` only after the file provider
artifact is active in Traefik and a Host-header request through that ingress
does not return a gateway failure.

## Atomic-write protocol

Per ADR-006 (panel: line 159):

> "Dynamic Traefik config files are written via temp-file + atomic rename on
> the same filesystem; never truncated in place. Midnight reserves
> `/.well-known/acme-challenge/*` at maximum router priority on every
> entrypoint serving :80; user routes claiming this path are rejected at
> validation."

Implementation: `internal/router.WriteFileAtomic` opens a temp file in the
same directory as the target, writes the rendered YAML, fsyncs, closes, and
renames over the existing file. The router validator rejects user route
definitions whose path prefix begins with `/.well-known/acme-challenge/`
before any write is attempted.

## Pinned Traefik version

`versions.yaml` pins Traefik at `v3.1.5`. The dynamic config schema and the
middleware vocabulary are version-sensitive: `internal/build/versions.go`
exports `RequiredTraefikVersion`, and the gate refuses to publish to a
mismatched binary. Operators upgrading Traefik must also upgrade Midnight in
lockstep.

## ACME defaults

Two cert resolvers ship: `letsencrypt-staging` (default; Phase 1 integration
tests use this) and `letsencrypt-prod`. Both use DNS-01 with a Cloudflare
provider as the panel-locked default; operators on Route53 swap the
`provider:` block. The `manual` provider is implemented in
`internal/acme/manual_dns.go` and is the documented fallback when no
provider integration exists.

The Cloudflare/Route53 lego providers expect credentials via environment
variables (e.g. `CLOUDFLARE_DNS_API_TOKEN`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`); Phase 1 Lane 3 reads them from the systemd unit
`EnvironmentFile=`. Lane 4's envelope-encrypted secret store will replace
this once it lands; the migration path is documented in
`agile/handoff/phase1-subagents/LANE3_STATUS.md`.

## Generated-domain HTTPS path

For the single-VM MVP, generated service hosts live under the operator's
platform apps-domain:

`<service-token>-<scope-hash>.<apps-domain>`

Midnight publishes only dynamic route artifacts into the file-provider
directory. The static Traefik config above supplies the `web` and `websecure`
entry points plus DNS-01 ACME resolvers; tenant routes do not own provider
configuration and cannot use the Docker provider.

Generated-domain HTTPS depends on a wildcard certificate for
`*.<apps-domain>` issued through DNS-01. The routine Go tests and renderer
tests prove deterministic YAML, atomic publication, reserved ACME paths, and
support-safe diagnostics without touching the network. The live provider proof
is intentionally gated: run the `internal/acme` integration test against
Let's Encrypt staging with a real apps-domain and provider credentials before
switching any route to `letsencrypt-prod`.

Minimum live verification before production traffic:

- `MIDNIGHT_LE_APPS_DOMAIN` points at an operator-controlled zone whose
  generated hostnames resolve to the VM on public DNS.
- Cloudflare or Route53 credentials can create and clean up
  `_acme-challenge.<apps-domain>` TXT records; the manual provider remains a
  fallback, not the happy path.
- Public recursive resolvers see the TXT value before ACME validation.
- CAA at the apps-domain and its parents permits `letsencrypt.org`.
- `acme.staging.json` or `acme.prod.json` exists under
  `/var/lib/midnight/acme/`, is owned by the Traefik runtime user, and is mode
  `0600` before Traefik starts.
- A generated hostname succeeds with a `curl --resolve` probe and presents the
  expected wildcard certificate.
