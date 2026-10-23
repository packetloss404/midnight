# Docker Compatibility

Midnight requires Docker Engine API v1.43 or newer and cgroup v2 for the single-VM runtime path.

Unsupported hosts fail preflight with `DOCKER_ENGINE_UNSUPPORTED` or the cgroup v2 doctor check.
