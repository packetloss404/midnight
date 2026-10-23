# syntax=docker/dockerfile:1

FROM node:22-bookworm-slim AS web
WORKDIR /src/web-next
COPY web-next/package.json web-next/package-lock.json ./
RUN npm ci
COPY web-next/ ./
RUN npm run build

FROM golang:1.26.9-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Stage the console where internal/api/webui_embed.go embeds it from, so the
# server binary below is self-contained: one artifact that serves its own UI.
# The staging dir is git-ignored, so clear whatever the build context carried.
RUN rm -rf internal/api/webdist && mkdir -p internal/api/webdist
COPY --from=web /src/web-next/dist/ ./internal/api/webdist/
RUN test -f internal/api/webdist/index.html
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X github.com/midnight/midnight/internal/version.Version=${VERSION} -X github.com/midnight/midnight/internal/version.Commit=${COMMIT} -X github.com/midnight/midnight/internal/version.Date=${DATE}" \
      -o /out/midnight-server ./cmd/midnight-server \
    && install -d -m 0750 /out/uploads

FROM gcr.io/distroless/static-debian12:nonroot
# No console files are shipped alongside the binary and MIDNIGHT_WEB_DIST_DIR is
# deliberately unset: the console is compiled into midnight-server. Operators
# who need to serve a different bundle can still set MIDNIGHT_WEB_DIST_DIR and
# mount it.
COPY --from=build /out/midnight-server /usr/local/bin/midnight-server
# Seed an empty named volume with the same numeric identity used by both the
# API and build-worker images. Without this directory, a fresh runtime-created
# mountpoint is root-owned and the nonroot API cannot initialize source
# artifact staging before the worker has touched the volume.
COPY --from=build --chown=65532:65532 /out/uploads /var/lib/midnight/uploads
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/midnight-server"]
CMD ["serve", "--listen", ":8080"]
