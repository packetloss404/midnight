# syntax=docker/dockerfile:1
#
# Local development image for the deploy worker (cmd/midnight-worker).
#
# The worker source is bind-mounted at /workspace by docker-compose.local.yml
# and run with `go run`, so a code change is picked up by restarting the
# container — no image rebuild. What this image exists for is the toolchain the
# worker needs around that: the Go compiler, git, CA certificates and the
# Docker CLI. Those used to be apt-installed on EVERY container start, which
# cost 30-60s per restart and made the deploy worker the slowest part of the
# local loop. They belong in a layer, like build-worker already does.
#
# GO_VERSION must track go.mod (docker-compose.local.yml passes the same
# ${MIDNIGHT_GO_IMAGE_TAG} it pins x-go-image to).
ARG GO_VERSION=1.26.9

FROM golang:${GO_VERSION}-bookworm

# docker.io provides the Docker CLI. The worker itself drives the Engine over
# the mounted socket, but the CLI is what an operator (and the deploy runbooks)
# reach for when inspecting a workload from inside this container, and it is
# what the previous inline apt-get installed.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      ca-certificates \
      docker.io \
      git \
    && rm -rf /var/lib/apt/lists/* \
    && docker --version \
    && git --version

WORKDIR /workspace
ENV PATH=/usr/local/go/bin:/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Runs as root: the worker needs the bind-mounted /var/run/docker.sock, and the
# host socket's gid is not stable across developer machines.
ENTRYPOINT ["go", "run", "./cmd/midnight-worker"]
CMD ["serve"]
