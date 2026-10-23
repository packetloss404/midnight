# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.9
ARG BUILDKIT_VERSION=v0.28.1
ARG RAILPACK_VERSION=v0.23.0

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/midnight-buildkit ./cmd/midnight-buildkit

FROM debian:bookworm-slim AS tools
ARG TARGETARCH
ARG BUILDKIT_VERSION
ARG RAILPACK_VERSION
ARG BUILDKIT_AMD64_SHA256=2e84057b941488c54575aee57015681f08c71042bd20c739b23879dfad3f2449
ARG BUILDKIT_ARM64_SHA256=40caf32b10cb0766f7440a53fea9cfe15c8116d64f9365c53ff417958a136332
ARG RAILPACK_AMD64_SHA256=e8bffc181c13e68c1c78ec618f1418e60b1602ec548f3ec1054996394ce0f06a
ARG RAILPACK_ARM64_SHA256=d4505d0f5f9f48c6350ad349111e10a1f5ba16c118834c85c6d0ecd630b25c34
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) buildkit_arch=amd64; buildkit_sha="${BUILDKIT_AMD64_SHA256}"; railpack_arch=x86_64; railpack_sha="${RAILPACK_AMD64_SHA256}" ;; \
      arm64) buildkit_arch=arm64; buildkit_sha="${BUILDKIT_ARM64_SHA256}"; railpack_arch=arm64; railpack_sha="${RAILPACK_ARM64_SHA256}" ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    buildkit_asset="buildkit-${BUILDKIT_VERSION}.linux-${buildkit_arch}.tar.gz"; \
    curl -fsSL --retry 3 -o "/tmp/${buildkit_asset}" \
      "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/${buildkit_asset}"; \
    echo "${buildkit_sha}  /tmp/${buildkit_asset}" | sha256sum -c -; \
    tar -xzf "/tmp/${buildkit_asset}" -C /tmp; \
    install -m 0755 /tmp/bin/buildctl /out-buildctl; \
    railpack_asset="railpack-${RAILPACK_VERSION}-${railpack_arch}-unknown-linux-musl.tar.gz"; \
    curl -fsSL --retry 3 -o "/tmp/${railpack_asset}" \
      "https://github.com/railwayapp/railpack/releases/download/${RAILPACK_VERSION}/${railpack_asset}"; \
    echo "${railpack_sha}  /tmp/${railpack_asset}" | sha256sum -c -; \
    tar -xzf "/tmp/${railpack_asset}" -C /tmp; \
    install -m 0755 /tmp/railpack /out-railpack

FROM debian:bookworm-slim
ARG BUILDKIT_VERSION
ARG RAILPACK_VERSION
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 65532 --create-home --shell /usr/sbin/nologin midnight \
    && install -d -o midnight -g midnight \
      /var/lib/midnight/build \
      /var/lib/midnight/build/checkout \
      /var/lib/midnight/buildkit/cache \
      /var/lib/midnight/uploads
COPY --from=build /out/midnight-buildkit /usr/local/bin/midnight-buildkit
COPY --from=tools /out-buildctl /usr/local/bin/buildctl
COPY --from=tools /out-railpack /usr/local/bin/railpack
RUN buildctl --version | grep -F "${BUILDKIT_VERSION}" \
    && railpack --version | grep -F "${RAILPACK_VERSION#v}"
USER midnight:midnight
ENTRYPOINT ["/usr/local/bin/midnight-buildkit"]
CMD ["worker"]
