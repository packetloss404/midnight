# syntax=docker/dockerfile:1

FROM golang:1.26.9-bookworm AS build
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
      -o /out/midnight-agent ./cmd/midnight-agent

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/midnight-agent /usr/local/bin/midnight-agent
ENTRYPOINT ["/usr/local/bin/midnight-agent"]
