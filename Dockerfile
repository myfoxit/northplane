# syntax=docker/dockerfile:1

# --- Stage 1: build the React UI -------------------------------------------
FROM node:22-alpine AS ui
WORKDIR /ui
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Stage 2: build the Go binary (UI embedded via go:embed) ---------------
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the committed UI with the freshly built one.
COPY --from=ui /ui/dist ./internal/web/dist
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/northplaned ./cmd/northplaned \
 && CGO_ENABLED=0 go build -trimpath \
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
