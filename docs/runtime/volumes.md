# Runtime Volumes

Volume support starts as pure agent planning. `cmd/midnight-agent/internal/runtime` can validate `VolumeMount` entries on a container spec and emit `attach_volume` or `detach_volume` reconcile steps only when `RuntimeObservations.Capabilities.Volumes` is true.

The planner does not create volumes, call Docker, mount filesystems, or mutate storage. Runtime adapters that support volumes are responsible for reporting capability and executing the ordered steps idempotently.

Volume planning order:

- Attach requested volumes after image availability checks and before container creation.
- Detach volumes after the old container is removed.
- Skip all volume steps when the backend does not report volume support.

Volume mounts require a non-empty volume ID, an absolute target path, and unique target paths within one container spec.
