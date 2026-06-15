# syntax=docker/dockerfile:1

# 1. Build the static binary. The committed web/dist is embedded via go:embed,
#    so the image is fully self-contained and no Node stage is required.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/dockercmd ./cmd/dockercmd
# Pre-create the data dir; the COPY --chown below gives it to the nonroot uid so
# a fresh named volume inherits writable ownership.
RUN mkdir -p /out/data

# 2. Minimal runtime: distroless static ships CA certificates (needed for the
#    update check, registry and SMTP TLS) and has no shell or package manager.
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/koduj-dev/docker-commander" \
      org.opencontainers.image.description="Monitor and control Docker from one self-contained binary." \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/dockercmd /usr/local/bin/dockercmd
COPY --from=build --chown=65532:65532 /out/data /data
# Listen on all interfaces inside the container; store data on the volume.
ENV DC_HOST=0.0.0.0 \
    DC_DATA_DIR=/data
EXPOSE 8470
VOLUME ["/data"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dockercmd"]
