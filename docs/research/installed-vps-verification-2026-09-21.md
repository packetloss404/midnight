# Installed VPS verification — 2026-09-21

Observed at `2026-09-22T03:47:45Z` on the private single-VM installation. Host
identity, addresses, credentials, DSNs, and absolute home paths are omitted.

## Artifact identity

| Field | Observed value |
| --- | --- |
| Source and merge commit | `f0c132c36b1d5d8f466637e633c6dfa68e17ab6e` |
| Debian package | `midnight 0.1.0~alpha-6 amd64` |
| Package SHA-256 | `bad89103707f987220bf5c2ac5b300bac6d384f359db7d82c6d64e1105571bb5` |
| Package provenance verifier | `PASS (0.1.0~alpha-6, 0.1.0-alpha, f0c132c36b1d5d8f466637e633c6dfa68e17ab6e)` |
| Compatibility endpoint build | `f0c132c36b1d5d8f466637e633c6dfa68e17ab6e` |

The package was built from a clean detached checkout created from a complete
commit-bound Git bundle. Installation used `dpkg -i` over the existing
alpha-6 package and applied zero migrations.

## Installed observations

- All six packaged units reported `active`: server, worker, agent, BuildKit,
  BuildKit worker, and registry.
- `systemctl --failed --no-legend` returned no failed units.
- The live install-readiness report had `exit_code=0`; checks
  `OR-CHK-DOC-010`, `013`, `015`, `018`, and `019` all passed.
- A direct metadata-only database query returned migration maximum/count
  `71|71`.
- The registry `/v2/` probe returned `{}` and the BuildKit Unix socket existed.
- `/api/v1alpha/compat` reported server version `0.1.0-alpha`, API version
  `v1alpha`, and the exact merge commit above.
- `midnight admin reset-password --help` exposed the packaged administrator
  recovery command and flags. A no-input call without `--email` exited 64 with
  the expected validation message and performed no credential mutation.

During package replacement, BuildKit and the registry reported nonzero exits
when systemd stopped their wrapper processes. Both restarted, passed their live
listener/socket checks, and no unit remained failed. This stop-path behavior is
retained here rather than hidden by the final active state.

## Evidence boundary

This report proves package provenance, installed build identity, schema state,
unit/readiness state, registry and BuildKit availability, and packaged CLI
registration at the observation time. It does not prove:

- a new application build or deployment from this candidate;
- public DNS, firewall/NAT, managed TLS, or an external HTTPS response;
- restart, rollback, audit, or administrator-recovery acceptance;
- private-GitHub webhook, checkout, deployment-status, and fault recovery;
- off-host backup, source destruction, restore, and application readback;
- production readiness, ongoing health after the observation, or Railway
  feature parity.

The September 5 `MID-116` and `MID-117` reports remain the narrower fresh-host
and source-to-Traefik-HTTP evidence. They are not upgraded to this commit by
this package verification.
