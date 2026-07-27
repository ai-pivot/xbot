#!/bin/bash
# Docker test environment for xbot — isolated, does NOT touch ~/.xbot
# Usage:
#   ./test-docker.sh build    — build xbot binary in Docker
#   ./test-docker.sh test     — run go test in Docker
#   ./test-docker.sh serve    — run xbot serve in Docker (port 18082)
#   ./test-docker.sh lint     — run golangci-lint in Docker
#   ./test-docker.sh all      — build + test + serve smoke test

set -e

IMAGE_NAME="xbot-test"
GO_IMAGE="golang:1.25-alpine"
TEST_PORT=18082
PROJECT_DIR="/home/cjw/xbot"

case "${1:-all}" in
  build)
    echo "🔨 Building xbot in Docker..."
    docker run --rm -v "$PROJECT_DIR:/build" -w /build "$GO_IMAGE" \
      sh -c "apk add --no-cache git && CGO_ENABLED=0 go build -o /build/xbot-test ."
    echo "✅ Build complete: xbot-test"
    ;;

  test)
    echo "🧪 Running go test in Docker..."
    docker run --rm -v "$PROJECT_DIR:/build" -w /build "$GO_IMAGE" \
      sh -c "apk add --no-cache git && CGO_ENABLED=0 go test ./... 2>&1" | tail -80
    ;;

  lint)
    echo "🔍 Running golangci-lint in Docker..."
    docker run --rm -v "$PROJECT_DIR:/build" -w /build "$GO_IMAGE" \
      sh -c "apk add --no-cache git && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest && golangci-lint run ./... 2>&1" | tail -40
    ;;

  serve)
    echo "🚀 Starting xbot serve in Docker (port $TEST_PORT)..."
    mkdir -p /tmp/xbot-test-data
    cat > /tmp/xbot-test-data/config.json << 'CONF'
{
  "server": { "host": "0.0.0.0", "port": 8082 },
  "web": { "host": "0.0.0.0", "port": 8082 },
  "llm": {
    "provider": "openai",
    "base_url": "https://api.deepseek.com",
    "api_key": "test-placeholder",
    "model": "deepseek-chat"
  },
  "log": { "level": "info" }
}
CONF
    docker run --rm -d --name xbot-test \
      -p $TEST_PORT:8082 \
      -e XBOT_HOME=/tmp/xbot-test-data \
      -v /tmp/xbot-test-data:/tmp/xbot-test-data \
      -v "$PROJECT_DIR/xbot-test:/app/xbot" \
      "$GO_IMAGE" sh -c "/app/xbot serve" 2>/dev/null || \
    echo "Note: need to build first. Run: ./test-docker.sh build"
    sleep 3
    echo "Testing http://127.0.0.1:$TEST_PORT/ ..."
    curl -s -o /dev/null -w "HTTP %{http_code}" http://127.0.0.1:$TEST_PORT/ || echo " (failed)"
    echo ""
    ;;

  all)
    "$0" build
    echo ""
    "$0" test
    ;;

  *)
    echo "Usage: $0 {build|test|serve|lint|all}"
    exit 1
    ;;
esac
