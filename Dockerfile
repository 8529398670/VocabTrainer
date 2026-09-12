# syntax=docker/dockerfile:1

# --- build stage -----------------------------------------------------------
# Only this stage needs the Go toolchain, the module cache, and the source.
# None of it survives into the shipped image.
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copy the module files alone first. Docker caches this layer, so an edit to
# the Go source re-runs the build but not the dependency download.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a statically linked binary with no libc dependency,
# which is what lets the runtime image stay this small and avoids the
# musl-vs-glibc surprises that come with cross-compiling against Alpine.
# -trimpath keeps build-machine paths out of the binary; -s -w drop the debug
# symbol tables, which shrinks it and removes a little detail from anyone
# poking at a compromised container.
# Version metadata, passed by dockerBuild.sh so that
# `docker exec <name> /app/server version` is as informative as a portable
# binary built by build.sh.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# One binary. The frontend is compiled into it by go:embed (see embed.go),
# and account administration is the "manage" subcommand -- so the image needs
# no static directory, no language.yaml, and no second executable.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/server .

# --- runtime stage ---------------------------------------------------------
FROM alpine:3.24 AS runtime

# ca-certificates is the only package added: without it any outbound HTTPS
# this app grows later fails with an opaque certificate error.
RUN apk add --no-cache ca-certificates \
 && addgroup -S app \
 && adduser -S -G app -h /app -s /sbin/nologin app

WORKDIR /app

COPY --from=builder /out/server ./

# APP_DIR is set explicitly because the default (~/.config/<app>) is meant for
# a binary run by a person on a workstation. A container has no meaningful
# home directory, and the whole point here is that state lives in a mounted
# volume -- so /app/data it is. The database, app storage, and an optional
# config.yaml all land inside it.
#
# STATIC_DIR is deliberately left at its default (./static relative to
# WORKDIR), which does not exist in this image, so the embedded frontend is
# used. Mounting over either path overrides it -- that is the supported way to
# run the shipped UI with different wording:
#   -v ./language.yaml:/app/data/language.yaml:ro
ENV APP_DIR=/app/data \
    PORT=8080

# Only the data directory is writable, and only by the app user. Everything
# else stays owned by root and read-only to the process -- so even a bug that
# lets someone run code in here cannot rewrite the binary or the frontend.
# dockerRun.sh mounts /app/data as a volume and runs the container with
# --read-only, which is what makes that guarantee real rather than a
# convention.
RUN mkdir -p /app/data && chown app:app /app/data

# Note: this ownership survives into a *named* volume, but a bind mount
# shadows it with the host's ownership. dockerRun.sh uses a bind mount, so the
# database stays visible on the host for backups, and chowns that directory to
# this user before starting the container. Running this image against a bind
# mount without that step fails with "permission denied" on app.db.

USER app
EXPOSE 8080

# Hits the trivial health endpoint rather than a real page, so a slow database
# query can never get a healthy container killed.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8080/api/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/server"]
