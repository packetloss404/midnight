# Domain TLS Failure Runbook

Status: single-VM MVP runbook.

Use this runbook when `CertExpiringSoon` fires, when ACME issuance fails, or when domain probes
report a TLS handshake error for a generated apps-domain hostname.

## Preconditions

- The affected generated hostname is recorded in the routes table.
- The platform apps-domain is known, and generated hostnames are expected under
  `<service-token>-<scope-hash>.<apps-domain>`, with exactly one generated label
  beneath the apps-domain.
- Operator has access to the ACME state file referenced in the platform backup set
  (`/var/lib/midnight/acme/acme.staging.json` or
  `/var/lib/midnight/acme/acme.prod.json`).
- The `acme.issuance` feature flag is currently enabled (verify via `midnight feature list`).

## Triage steps

1. Run `midnight support-bundle --include=events,config --preview` and confirm the domain in
   question appears in the manifest. Run the full bundle with `--out` if you intend to share.
2. Inspect the latest events for the domain. Look for `RESTORE_ACME_THROTTLED`,
   `AGENT_CLOCK_SKEW`, or any domain-specific failure code.
3. If `CertExpiringSoon` is also firing, force a renewal once and observe ACME state transitions.
   Do not retry in a tight loop — Let's Encrypt staging is throttled at 20/hour per the
   `RESTORE_ACME_THROTTLED` budget.
4. If renewal fails because of clock skew, verify `chronyd` or `systemd-timesyncd` health on the
   affected host before retrying.
5. Confirm the generated hostname resolves to the single VM from public DNS. If the apps-domain
   advertises AAAA, confirm the VM and Docker baseline are actually IPv6-ready before treating
   IPv6 failures as certificate failures.
6. Confirm Traefik loaded the generated route artifact
   `/etc/midnight/traefik/dynamic/<group>[.<route>].traefik.dynamic.yaml` and that the router
   uses `websecure` for TLS traffic. Check the loaded router through the Traefik API
   (`curl "$MIDNIGHT_WORKER_TRAEFIK_API_URL/api/http/routers"`), not just the file on disk: an
   artifact left in a `<group>/` subdirectory by an older release is loaded only at Traefik
   startup and is never refreshed.
7. For apps-domain wildcard failures, verify the DNS-01 chain before retrying ACME:
   provider credentials, `_acme-challenge.<apps-domain>` TXT visibility through public recursive
   resolvers, and CAA permission for `letsencrypt.org`.

## Recovery

- Byte-exact restore: when the failure is caused by corrupted ACME state, restore the matching
  `acme.staging.json` or `acme.prod.json` from backup with mode `0600` and confirm SHA-256
  before starting Traefik.
- Reissue: when the previous ACME state cannot be recovered, remove the damaged ACME state from
  the active path and let the controller stage reissuance through the throttled queue.
- Manual challenge: only as a last resort, satisfy a DNS-01 challenge by hand using the
  diagnostics surfaced by the support bundle.

## Verification

The generated hostname handshake succeeds end-to-end, presents a wildcard certificate for the
apps-domain, and `midnight_certificate_expires_in_seconds` for the domain is greater than 7 days
(`> 604800`). When DNS is still propagating, also verify with
`curl --resolve <host>:443:<vm-ip> https://<host>/` so routing and TLS can be separated from
public DNS lag.

## Rollback

If a manual change made the situation worse, restore the previous ACME state file from backup
using the offline restore plan and rerun the ACME smoke probe before declaring recovery.

## Linked failure codes

- `BACKUP_VERIFY_CHECKSUM_MISMATCH`
- `AGENT_CLOCK_SKEW`
- `BACKUP_OFFHOST_MISSING`
- `WILDCARD_RENEWAL_FAILED`
