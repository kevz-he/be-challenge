# syntax=docker/dockerfile:1.7
#
# Multi-stage build:
#   builder → distroless static (server, seed and healthcheck binaries)
#
# A single Dockerfile is reused for both the API server and the seeder.
# The seeder binary lives under ./test/seed because it's a test-data
# generator, not a production command. The healthcheck binary is always
# copied so the container can self-report health without a shell.

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# APP_PKG selects which Go package becomes /out/app. Defaults to the API
# server; the seeder stage overrides it to ./test/seed.
ARG APP_PKG=./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ${APP_PKG}
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

# The seeder needs the seed dataset at runtime; we stage it here and the
# `seeder` target copies it. The `server` target ignores it.
RUN mkdir -p /out/testdata && cp testdata/transactions.json /out/testdata/transactions.json && cp testdata/expected_counts.json /out/testdata/expected_counts.json

# Empty /data scaffold: when docker mounts the named volume on top of /data
# for the first time, it inherits the directory's ownership/permissions from
# the image. Without this the volume defaults to root:root and the nonroot
# user cannot create yuno.db / .seeded.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=builder /out/app /app
COPY --from=builder /out/healthcheck /healthcheck
COPY --from=builder --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]

FROM gcr.io/distroless/static-debian12:nonroot AS seeder
COPY --from=builder /out/app /app
COPY --from=builder /out/healthcheck /healthcheck
COPY --from=builder /out/testdata /testdata
COPY --from=builder --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
ENTRYPOINT ["/app"]
