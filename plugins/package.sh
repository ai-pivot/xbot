#!/usr/bin/env bash
# package.sh — build per-platform tarballs of the built-in plugins.
#
# Produces dist/xbot-plugins-{os}-{arch}.tar.gz for every release platform
# (same matrix as the xbot-cli binaries). Tarball layout matches the layout
# xbot-cli setup extracts into $XBOT_HOME/plugins/builtin/:
#
#   xbot.genui/{plugin.json, bin/genui-plugin}
#   xbot.git-fancy/{plugin.json, bin/git-fancy-plugin, web/index.js, ...}
#   xbot.ambience/{plugin.json}
#   .xbot-version            (release version stamp, informational)
#
# Generic across plugins/: every plugins/*/ dir with a plugin.json is included —
#   runtime "stdio"/"grpc" → cross-compiled Go binary (entry from plugin.json)
#   runtime "script"        → plugin.json only
#   web/ dir in the plugin source (if any) → copied verbatim
# Extra web assets (esbuild bundles built from web/src/plugins/*, e.g.
# git-fancy's panel/commit views) are merged from --web-dist-dir/<plugin-id>/.
#
# Usage (release CI / local):
#   plugins/package.sh [--out dist] [--web-dist-dir build/plugin-web] \
#                     [--version v1.2.3] \
#                     [--platforms "linux/amd64 linux/arm64 darwin/arm64 windows/amd64 windows/arm64"]
#
# Requires: go, jq, tar. Cross-compilation needs CGO_ENABLED=0 only (plugins
# are pure Go stdio/grpc processes).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGINS_DIR="$SCRIPT_DIR"
OUT_DIR="dist"
WEB_DIST_DIR=""
VERSION="dev"
PLATFORMS="linux/amd64 linux/arm64 darwin/arm64 windows/armd64 windows/armd64"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --out) OUT_DIR="$2"; shift 2 ;;
        --web-dist-dir) WEB_DIST_DIR="$2"; shift 2 ;;
        --version) VERSION="$2"; shift 2 ;;
        --platforms) PLATFORMS="$2"; shift 2 ;;
        *) echo "unknown flag: $1" >&2; exit 1 ;;
    esac
done

command -v go >/dev/null 2>&1 || { echo "package.sh: go is required" >&2; exit 1; }

# json_get FILE KEY DEFAULT — read a top-level string field from a JSON file.
# Uses jq when available, falls back to python3 (CI has jq; dev machines may not).
json_get() {
    local f="$1" key="$2" def="$3"
    if command -v jq >/dev/null 2>&1; then
        jq -r --arg def "$def" ".$key // \$def" "$f"
    else
        python3 -c 'import json,sys
try:
    d = json.load(open(sys.argv[1]))
    v = d.get(sys.argv[2])
    print(v if isinstance(v, str) else sys.argv[3])
except Exception:
    print(sys.argv[3])' "$f" "$key" "$def"
    fi
}

mkdir -p "$OUT_DIR"

# Collect plugin metadata once: id, runtime, entry binary (relative path
# without the ./ prefix, e.g. bin/genui-plugin).
# NOTE: source dirs use dashes (plugins/xbot-genui) while plugin IDs use dots
# (xbot.genui) — the tarball layout must use the ID (discovery expects
# $XBOT_HOME/plugins/builtin/<id>/), so we track source dir and ID separately.
declare -a PLUGIN_IDS=()
declare -A PLUGIN_SRC=()
declare -A PLUGIN_RUNTIME=()
declare -A PLUGIN_ENTRY=()

for plugin_json in "$PLUGINS_DIR"/*/plugin.json; do
    [[ -f "$plugin_json" ]] || continue
    plugin_dir="$(dirname "$plugin_json")"
    id="$(json_get "$plugin_json" id "")"
    runtime="$(json_get "$plugin_json" runtime "script")"
    entry="$(json_get "$plugin_json" entry "")"
    [[ -n "$id" ]] || { echo "package.sh: $plugin_json has no .id, skipping" >&2; continue; }
    PLUGIN_IDS+=("$id")
    PLUGIN_SRC[$id]="$plugin_dir"
    PLUGIN_RUNTIME[$id]="$runtime"
    PLUGIN_ENTRY[$id]="$entry"
    echo "  plugin: $id (src=$(basename "$plugin_dir") runtime=$runtime entry=${entry:-none})"
done

if [[ ${#PLUGIN_IDS[@]} -eq 0 ]]; then
    echo "package.sh: no plugins found in $PLUGINS_DIR" >&2
    exit 1
fi

built_any=0
for platform in $PLATFORMS; do
    goos="${platform%%/*}"
    goarch="${platform##*/}"
    staging="$(mktemp -d)"
    trap 'rm -rf "$staging"' EXIT

    for id in "${PLUGIN_IDS[@]}"; do
        plugin_dir="${PLUGIN_SRC[$id]}"
        dest="$staging/$id"
        mkdir -p "$dest"
        cp "$plugin_dir/plugin.json" "$dest/plugin.json"

        # Go-runtime plugins: cross-compile the entry binary.
        runtime="${PLUGIN_RUNTIME[$id]}"
        entry="${PLUGIN_ENTRY[$id]}"
        case "$runtime" in
            stdio|grpc)
                [[ -n "$entry" ]] || { echo "package.sh: $id ($runtime) has no entry" >&2; exit 1; }
                bin_rel="${entry#./}"
                if [[ "$goos" == "windows" ]]; then
                    bin_rel="${bin_rel}.exe"
                fi
                mkdir -p "$dest/$(dirname "$bin_rel")"
                out_bin="$dest/$bin_rel"
                (cd "$plugin_dir" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
                    go build -trimpath -o "$out_bin" .) \
                    || { echo "package.sh: go build failed for $id ($platform)" >&2; exit 1; }
                chmod +x "$out_bin"
                ;;
            script)
                : # plugin.json only (frontend builtin / script runtime)
                ;;
            *)
                echo "package.sh: $id has unknown runtime '$runtime', copying plugin.json only" >&2
                ;;
        esac

        # Plugin-local web assets shipped in the plugin source (web/ dir).
        if [[ -d "$plugin_dir/web" ]]; then
            mkdir -p "$dest/web"
            cp -R "$plugin_dir/web/." "$dest/web/"
        fi

        # Extra web assets from --web-dist-dir (esbuild bundles built from
        # web/src/plugins/*, e.g. git-fancy panel/commit views).
        if [[ -n "$WEB_DIST_DIR" && -d "$WEB_DIST_DIR/$id" ]]; then
            mkdir -p "$dest/web"
            cp -R "$WEB_DIST_DIR/$id/." "$dest/web/"
            echo "  web assets merged: $id ← $WEB_DIST_DIR/$id"
        fi
    done

    # Version stamp (informational; xbot-cli setup writes its own install stamp).
    echo "$VERSION" > "$staging/.xbot-version"

    out="$OUT_DIR/xbot-plugins-${goos}-${goarch}.tar.gz"
    tar -czf "$out" -C "$staging" .
    rm -rf "$staging"
    trap - EXIT
    built_any=1
    echo "  packaged: $out ($(du -h "$out" | cut -f1))"
done

if [[ "$built_any" -eq 0 ]]; then
    echo "package.sh: no platforms built" >&2
    exit 1
fi
echo "package.sh: done → $OUT_DIR/xbot-plugins-{os}-{arch}.tar.gz"
