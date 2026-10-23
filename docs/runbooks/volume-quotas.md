# Volume Capacity and Host Disk Safety

Status: requested capacity is advisory; per-volume kernel quota enforcement is
not implemented.

Midnight persists `size_bytes` as desired volume configuration. The current
Docker/local runtime does not assign and verify an XFS project quota or an ext4
quota identity for each volume. The control plane therefore must not describe
`size_bytes` as a hard limit, billable usage, or proof that one tenant cannot
fill the host filesystem.

## What operators can verify

Monitor the backing filesystem and Midnight's disk-pressure alerts:

```sh
findmnt -no SOURCE,FSTYPE,SIZE,AVAIL,USE%,OPTIONS /var/lib/midnight
df -h /var/lib/midnight
```

Enabling `prjquota` on XFS or quota support on ext4 can be useful host
preparation, but it does not make Midnight enforce per-volume quotas. There is
no doctor check that proves project-ID allocation, quota assignment, or a
write-beyond-limit failure for a Midnight volume.

## Incident response

If the filesystem approaches capacity:

1. Stop or reject new volume-heavy workloads using operator policy.
2. Identify large volume paths with host filesystem tools.
3. Back up and remove data only with the tenant owner's authorization.
4. Expand the backing filesystem or move data to a larger managed mount.
5. Verify services and backup jobs after recovery.

Do not claim that single-writer volume arbitration protects host capacity. It
serializes lifecycle changes, not bytes written by running containers.

## Enforcement completion gate

Hard volume limits require all of the following before the UI or usage API may
show `enforceable: true`: stable per-volume quota IDs, transactional assignment,
runtime mount integration, read-back verification, write-beyond-limit tests,
restart reconciliation, backup/restore handling, and a doctor check that fails
closed when the host lacks the required kernel capability.
