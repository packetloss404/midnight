# syntax=docker/dockerfile:1

FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X github.com/midnight/midnight/internal/version.Version=${VERSION} -X github.com/midnight/midnight/internal/version.Commit=${COMMIT} -X github.com/midnight/midnight/internal/version.Date=${DATE}" \
      -o /out/midnight-registry ./cmd/midnight-registry

FROM golang:1.26.10-bookworm AS distribution
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOBIN=/out \
    go install github.com/distribution/distribution/v3/cmd/registry@v3.0.0

FROM debian:bookworm-slim AS rootfs
RUN mkdir -p /var/lib/midnight/registry && chown -R 65532:65532 /var/lib/midnight

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/midnight-registry /usr/local/bin/midnight-registry
COPY --from=distribution /out/registry /usr/local/bin/registry
COPY --from=rootfs --chown=nonroot:nonroot /var/lib/midnight /var/lib/midnight
COPY --chown=nonroot:nonroot deploy/debian/registry.yml /etc/midnight/registry.yml
ENV REGISTRY_HTTP_ADDR=:5000
EXPOSE 5000
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/midnight-registry"]
CMD ["serve", "--registry", "/usr/local/bin/registry", "--config", "/etc/midnight/registry.yml"]
