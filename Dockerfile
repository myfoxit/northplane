# syntax=docker/dockerfile:1

# Multi-arch: the UI, docs and Go stages run on the BUILD platform and
# cross-compile for the TARGET platform (Go does that natively, the static
# binaries have no C dependencies), so `docker buildx build --platform
# linux/amd64,linux/arm64` needs no emulation. Only the tiny distroless runtime
# stage is per-target.

# --- Stage 1: build the React UI -------------------------------------------
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /ui
COPY web/package.json web/package-lock.json ./
# --legacy-peer-deps: the lockfile pins typescript@6 while openapi-typescript
# still declares a peer range of ^5.x. npm 11 (CI/local) tolerates this; the
# older npm bundled in node:22-alpine hard-fails `npm ci` on the peer
# conflict. `npm ci` installs the exact lockfile tree either way — this flag
# only skips the cosmetic peer-conflict abort so the image build matches CI.
RUN npm ci --legacy-peer-deps
COPY web/ ./
RUN npm run build

# --- Stage 1b: build the documentation site (Astro Starlight) --------------
# Served by northplaned itself at /docs/ — every image ships the manual that
# matches its own version, offline-capable. Same legacy-peer-deps rationale
# as the UI stage (npm bundled with node:22-alpine).
FROM --platform=$BUILDPLATFORM node:22-alpine AS docs
WORKDIR /docs
COPY docs/package.json docs/package-lock.json ./
RUN npm ci --legacy-peer-deps
COPY docs/ ./
RUN npm run build:embed

# --- Stage 2: build the Go binary (UI + docs embedded via go:embed) --------
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the committed UI with the freshly built one; stage the docs build.
COPY --from=ui /ui/dist ./internal/web/dist
COPY --from=docs /docs/dist ./internal/docs/dist
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/northplaned ./cmd/northplaned \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/np ./cmd/np \
 && mkdir -p /data

# --- Stage 3: minimal runtime ----------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/northplaned /usr/local/bin/northplaned
COPY --from=build /out/np /usr/local/bin/np
# Data dir owned by the distroless nonroot user (uid 65532) so the embedded
# SQLite + NP-TSDB are writable without running as root.
COPY --from=build --chown=65532:65532 /data /var/lib/northplane

# Data dir for the embedded SQLite + NP-TSDB; override with NORTHPLANE_DATA_DIR
# or a config file. Runs as the distroless 'nonroot' user (uid 65532) — the
# platform-aware default data dir resolves under its home when not root.
ENV NORTHPLANE_DATA_DIR=/var/lib/northplane
# Bind all interfaces so -p port mapping works (loopback inside the container
# namespace is unreachable from the host). The server still refuses plaintext
# on this non-loopback listener unless TLS is configured, or it runs behind a
# TLS-terminating proxy (NORTHPLANE_TRUST_PROXY=true — see docker-compose.yml),
# or NORTHPLANE_TLS_INSECURE=true is set explicitly for dev.
ENV NORTHPLANE_LISTEN=:8443
VOLUME /var/lib/northplane
EXPOSE 8443

ENTRYPOINT ["/usr/local/bin/northplaned"]
CMD ["serve"]
