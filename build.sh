#!/usr/bin/env bash
# Build portable, self-contained binaries -- one file per platform, each with
# the entire frontend compiled in.
#
# What you get is a single executable with no runtime dependencies at all: no
# Docker, no Go toolchain, no ./static directory, no language.yaml, no libc.
# Copy it to a machine, run it, and it serves the app. That is possible
# because of three choices working together:
#
#   CGO_ENABLED=0   static linking, so there is no libc version to match
#   go:embed        the frontend lives inside the binary (see embed.go)
#   bolt            the database is one file the binary creates on first run
#
# Docker is still the better answer for a long-lived internet-facing
# deployment -- it is where the read-only filesystem, dropped capabilities,
# and restart policy come from. These binaries are for everything else:
# handing a colleague a working copy, an internal box with no container
# runtime, a Raspberry Pi, a USB stick.
#
# Usage:
#   ./build.sh                  build every target below
#   ./build.sh linux/amd64      build one
#   ./build.sh darwin/arm64 linux/arm64
set -euo pipefail
cd "$( dirname "${BASH_SOURCE[0]}" )"

BINARY_NAME="vocab-trainer"
OUTPUT_DIR="dist"

# Add or remove freely. linux/arm is 32-bit (Raspberry Pi OS 32-bit and
# similar); linux/arm64 covers modern Pis, AWS Graviton, and Apple Silicon VMs.
ALL_TARGETS=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/amd64"
  "linux/arm64"
  "linux/arm"
  "windows/amd64"
  "windows/arm64"
)

if [ $# -gt 0 ]; then
  TARGETS=( "$@" )
else
  TARGETS=( "${ALL_TARGETS[@]}" )
fi

# Version metadata, stamped into the binary and readable later with
# `./vocab-trainer version`. Being able to ask a binary what it is beats
# guessing from a filename once a few copies are in circulation.
VERSION="${VERSION:-$( git describe --tags --always --dirty 2>/dev/null || echo "dev" )}"
COMMIT="$( git rev-parse --short HEAD 2>/dev/null || echo "unknown" )"
# Honour SOURCE_DATE_EPOCH so a build can be byte-for-byte reproducible.
BUILD_DATE="$( date -u -r "${SOURCE_DATE_EPOCH:-$( date +%s )}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u +%Y-%m-%dT%H:%M:%SZ )"

# -s -w drop the symbol and DWARF tables (smaller, less detail for anyone
# poking at a deployed binary). -trimpath keeps build-machine paths out of it,
# which also makes the output reproducible across checkout locations.
LDFLAGS="-s -w"
LDFLAGS="$LDFLAGS -X main.version=${VERSION}"
LDFLAGS="$LDFLAGS -X main.commit=${COMMIT}"
LDFLAGS="$LDFLAGS -X main.buildDate=${BUILD_DATE}"

mkdir -p "$OUTPUT_DIR"

echo "Building ${BINARY_NAME} ${VERSION} (${COMMIT})"
echo ""

FAILED=()
for TARGET in "${TARGETS[@]}"; do
  GOOS="${TARGET%%/*}"
  GOARCH="${TARGET##*/}"

  OUTPUT="${OUTPUT_DIR}/${BINARY_NAME}_${VERSION}_${GOOS}_${GOARCH}"
  [ "$GOOS" = "windows" ] && OUTPUT="${OUTPUT}.exe"

  printf "  %-22s " "${GOOS}/${GOARCH}"
  if CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
       go build -trimpath -ldflags="$LDFLAGS" -o "$OUTPUT" . 2>/tmp/build_err_$$; then
    SIZE="$( du -h "$OUTPUT" | cut -f1 | tr -d ' ' )"
    echo "ok  ${SIZE}  ${OUTPUT}"
  else
    echo "FAILED"
    sed 's/^/      /' /tmp/build_err_$$ >&2
    FAILED+=( "$TARGET" )
  fi
  rm -f /tmp/build_err_$$
done

# Checksums, so whoever receives a binary can confirm it is the one you built.
# Handing someone an executable is a trust decision; give them a way to check.
if command -v shasum >/dev/null 2>&1; then
  ( cd "$OUTPUT_DIR" && shasum -a 256 "${BINARY_NAME}"_* > SHA256SUMS 2>/dev/null ) || true
elif command -v sha256sum >/dev/null 2>&1; then
  ( cd "$OUTPUT_DIR" && sha256sum "${BINARY_NAME}"_* > SHA256SUMS 2>/dev/null ) || true
fi

echo ""
if [ ${#FAILED[@]} -gt 0 ]; then
  echo "Failed: ${FAILED[*]}" >&2
  exit 1
fi

cat <<NOTE
Done. ${OUTPUT_DIR}/ holds one self-contained binary per platform, plus
SHA256SUMS.

To run one:

  ./${OUTPUT_DIR}/${BINARY_NAME}_${VERSION}_\$(go env GOOS)_\$(go env GOARCH)

That is the whole command. No environment variables are required and no files
need to sit beside it -- the frontend is compiled in, and on first run it
creates its own state directory:

  ~/.config/${BINARY_NAME}/          (or \$XDG_CONFIG_HOME/${BINARY_NAME}/)
    app.db          the database
    secret.key      generated once, mode 0600
    storage/        application files
    config.yaml     optional; see config.example.yaml

Point it somewhere else with APP_DIR=/srv/${BINARY_NAME}. Ask a built binary
where it is looking with:

  ./${BINARY_NAME} manage paths

Two things to pass on with any binary you hand someone:

  * Back up that whole directory. secret.key encrypts the database, so
    app.db without it is unreadable -- and either one alone is useless.
  * SECURE_COOKIES must be true anywhere real, behind an HTTPS proxy. It
    defaults to true; set it false only for local http://localhost, or the
    browser will drop the session cookie and login will look like it silently
    does nothing.

The binary is also the admin CLI:

  ./${BINARY_NAME} manage list-users
  ./${BINARY_NAME} manage reissue-login -user-id 1
  ./${BINARY_NAME} version

If ./static or a language.yaml in the app directory exist, those are used
instead of the embedded copies -- which is what makes editing the frontend
during development work, and lets an operator reword the UI without a rebuild.
NOTE
