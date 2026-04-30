#!/bin/bash
# file-diff.sh — outputs a diff summary widget for xbot file-diff plugin
#
# Environment variables available from PostToolUse trigger:
#   XBOT_TOOL_NAME   — the tool that was used (e.g. "FileReplace")
#   XBOT_TOOL_OUTPUT — the tool execution result (may contain diff info)
#   XBOT_TOOL_INPUT  — JSON of the tool input parameters
#   XBOT_WORK_DIR    — the current working directory
#
# Format: "style|text" (style: dim, ok, warn, err, info, accent, or omit for normal)
#
# Examples:
#   ok|✏ FileReplace: 3 lines changed in main.go
#   warn|✏ FileCreate: new file created: config.yaml
#   dim|— no changes detected

set -euo pipefail

tool_name="${XBOT_TOOL_NAME:-}"
tool_output="${XBOT_TOOL_OUTPUT:-}"
tool_input="${XBOT_TOOL_INPUT:-}"
work_dir="${XBOT_WORK_DIR:-.}"

# Extract file path from tool_input JSON
file_path=""
if [ -n "$tool_input" ]; then
    # Simple JSON extraction — works for both {"path":"..."} and {"old_string":"...","new_string":"...","path":"..."}
    file_path=$(echo "$tool_input" | grep -oP '"path"\s*:\s*"\K[^"]+' 2>/dev/null | head -1) || true
fi

# Determine the action
case "$tool_name" in
    FileCreate|Write)
        if [ -n "$file_path" ]; then
            echo "ok|✎ created: ${file_path}"
        else
            echo "ok|✎ file created"
        fi
        ;;
    FileReplace|FileEdit)
        # Try to extract line counts from the output
        if [ -n "$file_path" ]; then
            echo "info|✎ edited: ${file_path}"
        else
            echo "info|✎ file edited"
        fi
        ;;
    *)
        echo "dim|✎ ${tool_name}"
        ;;
esac
