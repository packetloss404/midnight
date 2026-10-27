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
      -o /out/midnight-worker ./cmd/midnight-worker
RUN install -d -m 0700 -o 65532 -g 65532 /out/state/audit

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/midnight-worker /usr/local/bin/midnight-worker
COPY --from=build --chown=65532:65532 /out/state/ /var/lib/midnight/
# Mount durable state here; losing the signing key while retaining signed
# history must fail closed rather than silently establish a new identity.
VOLUME ["/var/lib/midnight"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/midnight-worker"]
CMD ["serve"]
