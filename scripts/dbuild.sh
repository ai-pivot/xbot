#!/bin/bash
# Quick Docker build check for xbot refactor
# Usage: ./scripts/dbuild.sh [test|build|vet]
set -e
cd /home/cjw/xbot
IMG="golang:1.26-alpine"
case "${1:-build}" in
  build)
    docker run --rm -v "$(pwd):/build" -w /build "$IMG" \
      sh -c "apk add --no-cache git >/dev/null 2>&1 && CGO_ENABLED=0 go build -buildvcs=false ./... 2>&1" \
      && echo "✅ BUILD OK" || echo "❌ BUILD FAILED"
    ;;
  test)
    docker run --rm -v "$(pwd):/build" -w /build "$IMG" \
      sh -c "apk add --no-cache git >/dev/null 2>&1 && CGO_ENABLED=0 go test -buildvcs=false -count=1 ./... 2>&1" | tail -60
    ;;
  vet)
    docker run --rm -v "$(pwd):/build" -w /build "$IMG" \
      sh -c "apk add --no-cache git >/dev/null 2>&1 && CGO_ENABLED=0 go vet -buildvcs=false ./... 2>&1" | tail -30
    ;;
esac
