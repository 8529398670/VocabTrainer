#!/usr/bin/env bash
# Build the container image.
#
# Split from dockerRun.sh so that building and running are separately
# runnable: CI builds without starting anything, and a redeploy that only
# needs a restart does not have to rebuild. dockerRun.sh calls this script, so
# the common case is still one command.
#
# Usage:
#   ./dockerBuild.sh              build vocab-trainer:latest
#   TAG=v1.2.3 ./dockerBuild.sh   build and additionally tag v1.2.3
set -euo pipefail
cd "$( dirname "${BASH_SOURCE[0]}" )"

IMAGE_NAME="vocab-trainer"

# Version metadata, stamped into the binary exactly as build.sh does, so
# `docker exec <name> /app/server version` is as informative as a portable
# binary's.
VERSION="${VERSION:-$( git describe --tags --always --dirty 2>/dev/null || echo "dev" )}"
COMMIT="$( git rev-parse --short HEAD 2>/dev/null || echo "unknown" )"
BUILD_DATE="$( date -u +%Y-%m-%dT%H:%M:%SZ )"

echo "Building ${IMAGE_NAME}:latest (${VERSION})"
docker build \
  --build-arg "VERSION=${VERSION}" \
  --build-arg "COMMIT=${COMMIT}" \
  --build-arg "BUILD_DATE=${BUILD_DATE}" \
  -t "${IMAGE_NAME}:latest" \
  .

if [ -n "${TAG:-}" ]; then
  docker tag "${IMAGE_NAME}:latest" "${IMAGE_NAME}:${TAG}"
  echo "Also tagged ${IMAGE_NAME}:${TAG}"
fi

echo "Built ${IMAGE_NAME}:latest"
