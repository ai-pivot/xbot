#!/bin/bash
# Run a TUI simulation scenario
# Usage: run-scenario.sh <scenario.json> [output.json] [width] [height]
set -euo pipefail

SCENARIO="${1:?Usage: run-scenario.sh <scenario.json> [output.json] [width] [height]}"
OUTPUT="${2:-}"
WIDTH="${3:-}"
HEIGHT="${4:-}"

SIM_BIN="/tmp/xbot-tui-sim"
XBOT_SRC="/home/user/src/xbot"

# Compile if needed
if [ ! -x "$SIM_BIN" ] || [ "$XBOT_SRC/channel/cli_sim_test.go" -nt "$SIM_BIN" ]; then
    echo "Compiling simulator..." >&2
    (cd "$XBOT_SRC" && go test -c -o "$SIM_BIN" ./channel/) || { echo "Compile failed" >&2; exit 1; }
fi

# If width/height specified, inject into scenario
if [ -n "$WIDTH" ] || [ -n "$HEIGHT" ]; then
    TMPFILE=$(mktemp /tmp/sim-scenario-XXXXXX.json)
    jq --arg w "${WIDTH:-0}" --arg h "${HEIGHT:-0}" '
        if $w != "0" then .config.width = ($w | tonumber) else . end |
        if $h != "0" then .config.height = ($h | tonumber) else . end
    ' "$SCENARIO" > "$TMPFILE"
    SCENARIO="$TMPFILE"
    trap "rm -f $TMPFILE" EXIT
fi

if [ -n "$OUTPUT" ]; then
    XBOT_SIM_SCENARIO="$SCENARIO" XBOT_SIM_OUTPUT="$OUTPUT" "$SIM_BIN" -test.run TestSimMain 2>/dev/null
else
    XBOT_SIM_SCENARIO="$SCENARIO" "$SIM_BIN" -test.run TestSimMain 2>/dev/null
fi
