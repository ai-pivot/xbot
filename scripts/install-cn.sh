#!/usr/bin/env bash
# install-cn.sh — xbot installer for mainland China users
#
# Proxies all GitHub downloads through a CDN mirror.
# Default mirror: ghfast.top (verified working in mainland China).
#
# Usage (one-liner — pick any mirror that works for you):
#
#   # Option 1: via ghfast.top (default)
#   curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
#
#   # Option 2: via gh-proxy.com
#   curl -fsSL https://gh-proxy.com/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
#
#   # Option 3: clone and run locally
#   git clone https://ghfast.top/https://github.com/ai-pivot/xbot.git
#   cd xbot && bash scripts/install-cn.sh
#
# Environment variables (all optional):
#   GH_MIRROR   — force a specific mirror host (e.g. ghfast.top)
#   All variables from install.sh (INSTALL_PATH, MODE, CHANNEL, VERSION, etc.)
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Mirror configuration — ordered by reliability in mainland China
# ---------------------------------------------------------------------------
DEFAULT_MIRRORS="ghfast.top gh-proxy.com ghps.cc"

# ---------------------------------------------------------------------------
# Colors
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[1;36m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Download a file through a mirror, with content validation.
# Returns 0 on success, 1 on failure.
# All log messages go to stderr to keep stdout clean.
# ---------------------------------------------------------------------------
try_download() {
    local mirror="$1"       # e.g. "ghfast.top" or "" (direct)
    local raw_url="$2"      # e.g. "https://raw.githubusercontent.com/..."
    local dest="$3"         # local file path to write to

    local url
    if [ -n "$mirror" ]; then
        url="https://${mirror}/${raw_url}"
    else
        url="$raw_url"
    fi

    info "Trying to download from ${url}..." >&2
    if curl -fsSL --connect-timeout 10 --max-time 30 -o "$dest" "$url" 2>/dev/null; then
        # Verify the download is a real shell script (not an empty/error page)
        if [ -s "$dest" ] && head -1 "$dest" 2>/dev/null | grep -qE '^#!'; then
            return 0
        fi
        warn "Downloaded file is not a valid shell script, trying next..." >&2
    fi
    return 1
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
echo ""
echo "  ╔══════════════════════════════════════════════════╗"
echo "  ║     xbot-cli Installer (China Mirror Mode)      ║"
echo "  ╚══════════════════════════════════════════════════╝"
echo ""

# Step 1: Determine mirror
if [ -z "${GH_MIRROR:-}" ]; then
    GH_MIRROR="ghfast.top"
    info "Using default mirror: ${CYAN}${GH_MIRROR}${NC}"
else
    info "Using mirror: ${CYAN}${GH_MIRROR}${NC}"
fi

echo ""

# Step 2: Check for local install.sh (cloned repo)
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "${script_dir}/install.sh" ]; then
    INSTALL_SH="${script_dir}/install.sh"
    info "Using local install.sh from repository"
else
    # Download install.sh — try all mirrors with fallback
    tmpdir=$(mktemp -d)
    install_sh="${tmpdir}/install.sh"

    urls=(
        "https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh"
        "https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh"
    )

    found=false

    # Try the selected mirror first, then all others, then direct
    for m in "$GH_MIRROR" $DEFAULT_MIRRORS ""; do
        for raw_url in "${urls[@]}"; do
            if try_download "$m" "$raw_url" "$install_sh"; then
                GH_MIRROR="$m"  # update to the one that actually worked
                chmod +x "$install_sh"
                INSTALL_SH="$install_sh"
                found=true
                break 2
            fi
        done
    done

    if [ "$found" = false ]; then
        error "Failed to download install.sh. Please check your network or set GH_MIRROR manually."
    fi
fi

# Step 3: Run install.sh with GH_MIRROR set (it uses gh_url() internally)
export GH_MIRROR
info "Launching installer..."
echo ""

bash "$INSTALL_SH" "$@"
