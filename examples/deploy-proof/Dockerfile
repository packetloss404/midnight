# syntax=docker/dockerfile:1

FROM golang:1.26.9-bookworm AS build
WORKDIR /src
COPY main.go .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/deploy-proof ./main.go

FROM alpine:3.21
COPY --from=build /out/deploy-proof /usr/local/bin/deploy-proof
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=2s --start-period=5s --retries=3 \
    CMD wget -q -O - http://127.0.0.1:8080/healthz | grep -q '"status":"ok"'
ENTRYPOINT ["/usr/local/bin/deploy-proof"]
