#!/bin/bash
# file-diff.sh — generates a unified diff as markdown for xbot progress panel
#
# Output format: "md|" prefix + ```diff code block (plain text, no ANSI)
# glamour will render the diff code block with syntax highlighting.

set -euo pipefail

tool_name="${XBOT_TOOL_NAME:-}"
tool_input="${XBOT_TOOL_INPUT:-}"

# Extract file path from tool_input JSON
file_path=""
if [ -n "$tool_input" ]; then
    file_path=$(echo "$tool_input" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('path', ''), end='')
except: pass
" 2>/dev/null) || true
fi

[ -z "$file_path" ] && exit 0

case "$tool_name" in
    FileCreate|Write)
        # New file — show first 15 lines as added
        content=$(echo "$tool_input" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    c = d.get('content', '')
    lines = c.split('\n')[:15]
    for l in lines:
        print('+' + l)
    if c.count('\n') > 15:
        print('... (truncated)')
except: pass
" 2>/dev/null) || true

        if [ -n "$content" ]; then
            echo "md|"
            echo "\`\`\`diff"
            echo "--- /dev/null"
            echo "+++ $file_path (new file)"
            echo "$content"
            echo "\`\`\`"
        fi
        ;;

    FileReplace|FileEdit)
        old_str=$(echo "$tool_input" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('old_string', ''), end='')
except: pass
" 2>/dev/null) || true

        new_str=$(echo "$tool_input" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('new_string', ''), end='')
except: pass
" 2>/dev/null) || true

        [ -z "$old_str" ] && [ -z "$new_str" ] && exit 0

        old_file=$(mktemp)
        new_file=$(mktemp)
        trap 'rm -f "$old_file" "$new_file"' EXIT

        printf '%s' "$old_str" > "$old_file"
        printf '%s' "$new_str" > "$new_file"

        raw_diff=$(diff -u --label "a/$file_path" --label "b/$file_path" "$old_file" "$new_file" 2>/dev/null || true)

        if [ -n "$raw_diff" ]; then
            echo "md|"
            echo "\`\`\`diff"
            echo "$raw_diff" | head -40
            total=$(echo "$raw_diff" | wc -l)
            if [ "$total" -gt 40 ]; then
                echo "... ($(( total - 40 )) more lines)"
            fi
            echo "\`\`\`"
        fi
        ;;

    *)
        exit 0
        ;;
esac
