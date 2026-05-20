#!/usr/bin/env bash
# install-cn.sh — xbot installer for mainland China users
#
# Automatically selects a reachable GitHub CDN mirror and proxies all
# downloads through it.  Zero configuration required.
#
# Usage (one-liner — pick any mirror that works for you):
#
#   # Option 1: via ghfast.top
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
#   MIRROR_LIST — space-separated list of mirrors to try (override defaults)
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
# Detect the best reachable mirror (3s timeout per candidate)
# ---------------------------------------------------------------------------
pick_mirror() {
    local mirrors="${MIRROR_LIST:-$DEFAULT_MIRRORS}"

    for m in $mirrors; do
        if curl -fsSL --connect-timeout 3 --max-time 5 -o /dev/null \
            "https://${m}/https://github.com" 2>/dev/null; then
            echo "$m"
            return
        fi
    done

    # All mirrors failed
    echo ""
}

# ---------------------------------------------------------------------------
# Locate the real install.sh (local repo or download via mirror)
# ---------------------------------------------------------------------------
find_install_sh() {
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

    # 1. Check if install.sh exists alongside this script (cloned repo)
    if [ -f "${script_dir}/install.sh" ]; then
        echo "${script_dir}/install.sh"
        return
    fi

    # 2. Download install.sh through the selected mirror
    local tmpdir
    tmpdir=$(mktemp -d)
    # trap cleanup in the caller (main) so tmpdir lives until install.sh finishes

    local install_sh="${tmpdir}/install.sh"
    local urls=(
        "https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh"
        "https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh"
    )

    for raw_url in "${urls[@]}"; do
        local proxied_url
        if [ -n "${GH_MIRROR:-}" ]; then
            proxied_url="https://${GH_MIRROR}/${raw_url}"
        else
            proxied_url="$raw_url"
        fi
        info "Trying to download install.sh from ${proxied_url}..."
        if curl -fsSL --connect-timeout 10 --max-time 30 -o "$install_sh" "$proxied_url" 2>/dev/null; then
            chmod +x "$install_sh"
            echo "$install_sh"
            return
        fi
    done

    error "Failed to download install.sh. Please check your network or set GH_MIRROR manually."
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
echo ""
echo "  ╔══════════════════════════════════════════════════╗"
echo "  ║     xbot-cli Installer (China Mirror Mode)      ║"
echo "  ╚══════════════════════════════════════════════════╝"
echo ""

# Step 1: Select mirror (unless user forced one)
if [ -z "${GH_MIRROR:-}" ]; then
    info "Auto-detecting best CDN mirror..."
    GH_MIRROR=$(pick_mirror)
fi

if [ -n "$GH_MIRROR" ]; then
    info "Using mirror: ${CYAN}${GH_MIRROR}${NC}"
else
    warn "No CDN mirror reachable — will try direct GitHub."
    warn "If download fails, set GH_MIRROR manually:"
    warn "  GH_MIRROR=ghfast.top bash scripts/install-cn.sh"
fi

echo ""

# Step 2: Find the real install.sh
INSTALL_SH=$(find_install_sh)

# Step 3: Run install.sh with GH_MIRROR set (it uses gh_url() internally)
export GH_MIRROR
info "Launching installer..."
echo ""

bash "$INSTALL_SH" "$@"
