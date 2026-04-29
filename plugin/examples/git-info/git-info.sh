#!/bin/bash
# git-info.sh — outputs widget content for xbot git-info plugin
# Format: "style|text" (style: dim, ok, warn, err, info, accent, or omit for normal)
#
# Examples:
#   ok|git:main ✓          — clean repo
#   warn|git:feat/x Δ3      — 3 changed files
#   dim|git: —               — not in a git repo

set -euo pipefail

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || true
if [ -z "$branch" ] || [ "$branch" = "HEAD" ]; then
    echo "dim|git: —"
    exit 0
fi

# Count pending changes
changes=$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ') || changes=0
# Count unpushed commits
ahead=$(git rev-list --count @{u}..HEAD 2>/dev/null) || ahead=0
# Count unpulled commits  
behind=$(git rev-list --count HEAD..@{u} 2>/dev/null) || behind=0

status=""
[ "$changes" -gt 0 ] && status="${status}Δ${changes} "
[ "$ahead" -gt 0 ]   && status="${status}↑${ahead} "
[ "$behind" -gt 0 ]  && status="${status}↓${behind} "

if [ -z "$status" ]; then
    echo "ok|git:${branch} ✓"
elif [ "$changes" -gt 0 ]; then
    echo "warn|git:${branch} ${status}"
else
    echo "info|git:${branch} ${status}"
fi
