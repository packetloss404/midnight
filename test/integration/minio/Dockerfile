# Integration-only S3 fixture. Upstream removed its public binary images.
# Build the final upstream source revision instead of depending on a warm cache.
FROM golang:1.26.10-bookworm AS build
ENV CGO_ENABLED=0 GOMAXPROCS=4 GOFLAGS=-p=2
RUN GOBIN=/out go install github.com/minio/minio@7aac2a2c5b7c882e68c1ce017d8256be2feea27f

FROM debian:bookworm-slim
COPY --from=build /out/minio /usr/local/bin/minio
EXPOSE 9000 9001
ENTRYPOINT ["/usr/local/bin/minio"]
