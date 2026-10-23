# Railway parity VM validation handoff — installed single-host proof

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Checkpoint: 2026-07-29 (VM observed in UTC on 2026-07-30). This handoff records
the first clean installed-system exercise of the current `main` tree. It does
not contain credentials, DSNs, private keys, or bearer tokens.

## VM state

- Host: operator-provided Ubuntu 24.04.4 LTS amd64 VM (`pdian02ap08`).
- Root storage was expanded from the original small root LV to a 123G ext4
  filesystem; the VM has ample free space for image/build acceptance runs.
- Passwordless sudo is configured for the automation account and validated with
  `sudo -n true`.
- PostgreSQL 16, Docker Engine 29.6.2/API 1.55, Node 22, BuildKit 0.28.1,
  Distribution registry 3.0.0, Railpack 0.23.0, and Traefik 3.1.5 are
  installed and verified on the VM.
- Midnight PostgreSQL storage is migrated through schema version 53.
- `midnight-server`, `midnight-worker`, `midnight-agent`,
  `midnight-buildkit`, `midnight-buildkit-worker`, `midnight-registry`,
  PostgreSQL, Docker, and Traefik are active; `systemctl --failed` is empty.

## Repository fixes from this exercise

- `midnight-buildkit` now loads the package's file-backed PostgreSQL DSN when
  the environment does not contain an inline DSN. This prevents a packaged
  build worker from restart-looping on a normal `/etc/midnight/db.dsn` install.
- The Docker baseline is consistent across the package, preflight classifier,
  tests, and install docs: `live-restore`, `no-new-privileges`, and `overlay2`
  are explicit baseline keys.
- Debian `postinst` creates `/var/lib/midnight/registry` as `midnight`-owned
  before the registry is started, and creates a group-readable Traefik access
  log without truncating existing contents on upgrades.

## Evidence completed

- `go test ./...` passes.
- Live doctor has no fatal findings. The only warnings are the expected
  non-FQDN VM hostname and the absence of an off-host backup destination.
- `surface-agreement` passes against the installed API and PostgreSQL,
  including REST/CLI contract checks, agent registration, runtime inventory,
  operation/audit evidence, and restore-status honesty.
- A tiny immutable image was built with Docker and pushed to the local
  registry. The registry push completed at digest
  `sha256:d4fa095209feaa811fba74d215321b43d5fda6c6bce11e85b2eec0e5a8b8c72e`.
- A real private deployment using that digest returned `202`, was delivered
  through the durable outbox, started a Docker container, passed readiness,
  and reached `SUCCEEDED`. The container returned `Midnight VM deployment OK`
  from its HTTP server; runtime inventory reported `1/1` ready.
- A public-route attempt started and cleaned up the candidate correctly, then
  failed closed with `ROUTE_PUBLICATION_FAILED` because the operator route did
  not have an exact durable public binding and ownership evidence. This is not
  a public DNS/TLS success claim.
- The database template acceptance remains an honest `501 NOT_IMPLEMENTED`
  until volume attachment, readiness, and database smoke checks are wired.

## Remaining gates

1. The operator's next external step is an NKC public address with TCP `80`
   and `443` NATed to `10.80.13.20`. Once the Cloudflare zone/domain exists,
   bind a service-managed generated domain or complete custom-domain
   ownership proof, then repeat the public HTTP route proof through Traefik.
2. Run the delegated-DNS ACME staging proof, install/acknowledge the Traefik
   certificate resolver, and verify restart, renewal, and public `:443` from an
   external vantage point. Production CA issuance remains a separate explicit
   gate.
3. Configure an off-host backup destination and run the real volume/database
   backup plus restore proof; local manifest/status checks do not claim copied
   bytes or point-in-time recovery.
4. Rebuild and reinstall the package from the committed tree on a fresh VM so
   the registry and access-log postinst fixes are proven without manual repair.

## Resume commands

From the repository root:

```sh
go test ./...
sh -n deploy/debian/postinst
git status --short
```

On a fresh VM, follow
[`operator-install-path.md`](../install/operator-install-path.md), then
[`live-deploy-proof.md`](../runtime/live-deploy-proof.md). Never copy the VM's
DSN, generated keys, bearer token, or temporary setup password into evidence or
the repository.
