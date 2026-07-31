#!/usr/bin/env bash
# Build frontend and sync to xbot's static serving directories.
# Usage: ./build-and-sync.sh
set -euo pipefail

WEB_DIR="$(cd "$(dirname "$0")" && pwd)"
DIST_DIR="$WEB_DIR/dist"
TARGETS=()

# Binary-relative: <exe_dir>/web/dist/ (Docker image layout / install layout)
if [ -d "$(dirname "$(command -v xbot 2>/dev/null || echo /root/.local/bin/xbot)")/web/dist" ]; then
  TARGETS+=("$(dirname "$(command -v xbot 2>/dev/null || echo /root/.local/bin/xbot)")/web/dist")
fi
# Common fallback
[ -d /root/.local/bin/web/dist ] && TARGETS+=("/root/.local/bin/web/dist")
[ -d /root/.xbot/web/dist ] && TARGETS+=("/root/.xbot/web/dist")

echo "▶ Building frontend..."
cd "$WEB_DIR"
npm run build 2>&1

echo ""
echo "▶ Syncing dist to serving directories..."
for t in "${TARGETS[@]}"; do
  if [ "$t" = "$DIST_DIR" ]; then continue; fi
  echo "  → $t"
  # Clean old assets to avoid stale chunks
  rm -rf "$t/assets"
  cp -r "$DIST_DIR"/* "$t/"
done

echo ""
echo "✓ Done. Refresh :8082 to see changes."
