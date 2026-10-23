# syntax=docker/dockerfile:1.7
#
# pulsed container image (ADR-0018, PL-OPS-001).
#
#   web    : builds the dashboard into internal/ui/dist with the locked pnpm tree.
#   build  : static Go binaries (CGO_ENABLED=0, -trimpath), cross-compiled on the build platform (no emulation).
#   final  : distroless static (Debian 13), non-root UID/GID 65532, no shell.
#
# Every base image is pinned by digest. Update a digest with:
#   docker buildx imagetools inspect <image>:<tag>      # use the index "Digest:" line
#
# Runtime layout (docs/operator/configuration.md):
#   /usr/local/bin/pulsed, /usr/local/bin/pulse    binaries (root-owned, read-only)
#   /data/pulsed                                   pulsed volume (keys.enc, pulse.db, creds), 0700 65532:65532
#   /data/nats                                     volume shared with the nats container (conf, operator.jwt, tls, jwt/, js/)
#   /run/secrets/pulse_master_key                  compose secret; never on a volume

ARG NODE_IMAGE=docker.io/library/node:24.22.0-trixie-slim@sha256:a6155384446670e9719bdc4fd0ebe4d4b29fb32a8d6aa523da3547a6cfbcf22c
ARG GO_IMAGE=docker.io/library/golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88

# ---------------------------------------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
ENV CI=true COREPACK_ENABLE_DOWNLOAD_PROMPT=0 PNPM_HOME=/pnpm PATH=/pnpm:$PATH
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && corepack install
RUN --mount=type=cache,id=pulse-pnpm-store,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store && pnpm install --frozen-lockfile
COPY web/ ./
RUN mkdir -p /src/internal/ui/dist && pnpm build

# ---------------------------------------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/
COPY --from=web /src/internal/ui/dist/ internal/ui/dist/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for cmd in pulsed pulse; do \
      GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
        -ldflags "-s -w -buildid= \
          -X github.com/aurimasniekis/go-pulse/internal/app.Version=${VERSION} \
          -X github.com/aurimasniekis/go-pulse/internal/app.Commit=${COMMIT} \
          -X github.com/aurimasniekis/go-pulse/internal/app.Date=${BUILD_DATE}" \
        -o /out/$cmd ./cmd/$cmd || exit 1; \
    done
# Directory skeleton (the final image has no shell). Named volumes inherit ownership and mode on first use.
RUN install -d -m 0700 -o 65532 -g 65532 /rootfs/data/pulsed && \
    install -d -m 0755 -o 65532 -g 65532 /rootfs/data/nats

# ---------------------------------------------------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG COMMIT=
LABEL org.opencontainers.image.title="pulsed" \
      org.opencontainers.image.description="Pulse: personal durable event bus on NATS JetStream" \
      org.opencontainers.image.source="https://github.com/aurimasniekis/go-pulse" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian13:nonroot"
COPY --from=build --chown=0:0 --chmod=0555 /out/pulsed /usr/local/bin/pulsed
COPY --from=build --chown=0:0 --chmod=0555 /out/pulse /usr/local/bin/pulse
COPY --from=build /rootfs/ /
ENV PULSE_DATA_DIR=/data/pulsed PULSE_NATS_DIR=/data/nats
USER 65532:65532
WORKDIR /data/pulsed
VOLUME ["/data/pulsed"]
# 8443 public (TLS), 8080 admin (publish on host loopback only), 9464 metrics/health (host loopback only).
EXPOSE 8443/tcp 8080/tcp 9464/tcp
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 CMD ["/usr/local/bin/pulsed", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/pulsed"]
CMD ["serve"]
