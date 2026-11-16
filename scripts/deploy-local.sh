#!/usr/bin/env bash
# Local deploy of the xbot binary to this machine's supervisor unit.
#
# Guards learned the hard way (2026-11-14):
#   * a failed commit must ABORT the deploy — the first version kept going after a
#     lint failure and restarted the service with the SAME binary (pointless restart);
#   * if the built binary is byte-identical to the running one, do NOT restart.
#
# Usage: bash scripts/deploy-local.sh [--force]
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

FORCE=0
[ "${1:-}" = "--force" ] && FORCE=1
SC=/nix/store/5mc40v8qa34jyilh5jgsfi1sc42f77hv-python3.8-supervisor-4.2.2/bin/supervisorctl
CONF=/mlplatform/supervisord/supervisord.conf
BIN=/home/smith/go/bin/xbot-cli
TAG=$(date -u +%Y%m%dT%H%M%SZ)
DIR=/tmp/xbot-deploy-$TAG
umask 022
mkdir -p "$DIR"

if [ -n "$(git status --porcelain)" ]; then
  echo "ABORT: working tree dirty — commit first (deploying uncommitted code makes rollback ambiguous)"
  git status --short | head -10
  exit 1
fi
git rev-parse HEAD > "$DIR/commit"
echo "[deploy] commit=$(cat "$DIR/commit") dir=$DIR"

go test ./... -count=1 > "$DIR/tests.log" 2>&1 || { echo "ABORT: go test failed"; tail -20 "$DIR/tests.log"; exit 1; }
go build -o /tmp/xbot-cli-new ./cmd/xbot-cli > "$DIR/build.log" 2>&1 || { echo "ABORT: build failed"; cat "$DIR/build.log"; exit 1; }
EXPECTED=$(sha256sum /tmp/xbot-cli-new | awk '{print $1}')
echo "$EXPECTED" > "$DIR/build.sha256"

STATUS=$(sudo -n "$SC" -c "$CONF" status xbot-server)
OLD_PID=$(printf '%s\n' "$STATUS" | sed -n 's/.*pid \([0-9][0-9]*\),.*/\1/p')
RUNNING_SHA=$(sha256sum "/proc/$OLD_PID/exe" 2>/dev/null | awk '{print $1}')
echo "[deploy] running_pid=$OLD_PID running_sha=$RUNNING_SHA built_sha=$EXPECTED"

if [ "$RUNNING_SHA" = "$EXPECTED" ] && [ "$FORCE" != "1" ]; then
  echo "NOOP: built binary is identical to the running one — no restart needed (use --force to restart anyway)"
  printf 'NOOP\n' > "$DIR/result"
  exit 0
fi

umask 077
cp -p "$BIN" "$BIN.pre-$TAG"
mv "$BIN" "$BIN.new-$TAG"
mv /tmp/xbot-cli-new "$BIN"
sudo -n "$SC" -c "$CONF" restart xbot-server

NEW_PID=''; HTTP=''
for _ in $(seq 1 90); do
  ST=$(sudo -n "$SC" -c "$CONF" status xbot-server 2>&1 || true)
  NEW_PID=$(printf '%s\n' "$ST" | sed -n 's/.*RUNNING.*pid \([0-9][0-9]*\),.*/\1/p')
  HTTP=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:16000/ || true)
  [ -n "$NEW_PID" ] && [ "$NEW_PID" != "$OLD_PID" ] && [ "$HTTP" = 200 ] && break
  sleep 1
done
RUN_SHA=$(sha256sum "/proc/$NEW_PID/exe" 2>/dev/null | awk '{print $1}')
echo "[deploy] old_pid=$OLD_PID new_pid=$NEW_PID http=$HTTP running_sha=$RUN_SHA"
[ "$RUN_SHA" = "$EXPECTED" ] || { echo "ABORT: running sha mismatch"; exit 1; }

bash /home/smith/.xbot/court/bin/verify-deploy.sh "$EXPECTED" > "$DIR/verify.log" 2>&1
TAIL=$(tail -3 "$DIR/verify.log" | tr '\n' ' ')
echo "[deploy] verify: $TAIL"
grep -q 'pass=' "$DIR/verify.log" || { echo "ABORT: verify produced no verdict"; exit 1; }
printf 'OK\n' > "$DIR/result"
echo "[deploy] OK (backup: $BIN.pre-$TAG)"
