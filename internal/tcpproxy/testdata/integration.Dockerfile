# syntax=docker/dockerfile:1
FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags=integration -trimpath -o /out/tcp-proxy-helper ./tools/tcp-proxy-integration-helper

FROM alpine:3.21
COPY --from=build /out/tcp-proxy-helper /usr/local/bin/tcp-proxy-helper
ENTRYPOINT ["/usr/local/bin/tcp-proxy-helper"]
