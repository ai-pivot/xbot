#!/bin/bash
# TUI Simulation runner — supports file and stdin input
# Usage:
#   run-sim.sh scenario.json [--trace] [--output result.json]
#   echo '{"config":{"width":120,"height":40},"steps":[...]}' | run-sim.sh - [--trace]
set -euo pipefail

SIM_BIN="/tmp/xbot-tui-sim"
XBOT_SRC="/home/user/src/xbot"

# Compile if needed
if [ ! -x "$SIM_BIN" ] || [ "$XBOT_SRC/channel/cli_sim_test.go" -nt "$SIM_BIN" ]; then
    (cd "$XBOT_SRC" && go test -c -o "$SIM_BIN" ./channel/) 2>/dev/null || { echo "Compile failed" >&2; exit 1; }
fi

SCENARIO=""
TRACE=""
OUTPUT=""

# Read from stdin if arg is "-"
if [ "${1:-}" = "-" ]; then
    SCENARIO=$(mktemp /tmp/sim-stdin-XXXXXX.json)
    cat > "$SCENARIO"
    trap "rm -f $SCENARIO" EXIT
    shift
else
    SCENARIO="${1:?Usage: run-sim.sh <scenario.json> [--trace] [--output result.json]}"
    shift || true
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        --trace) TRACE="1"; shift ;;
        --output) OUTPUT="$2"; shift 2 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

CMD="XBOT_SIM_SCENARIO=$SCENARIO"
[ -n "$TRACE" ] && CMD="$CMD XBOT_SIM_TRACE=1"
[ -n "$OUTPUT" ] && CMD="$CMD XBOT_SIM_OUTPUT=$OUTPUT"

eval "$CMD $SIM_BIN -test.run TestSimMain" 2>/dev/null
