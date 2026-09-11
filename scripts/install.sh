#!/usr/bin/env bash
set -euo pipefail

REPO="ai-pivot/xbot"
FALLBACK_REPO="CjiW/xbot"
BINARY="xbot-cli"
# GitHub CDN mirror for users behind the GFW (set by install-cn.sh or manually)
# e.g. GH_MIRROR=ghfast.top  →  https://ghfast.top/https://github.com/...
GH_MIRROR="${GH_MIRROR:-}"
# Default to user-local install (no sudo required)
INSTALL_PATH="${INSTALL_PATH:-$HOME/.local/bin}"
XBOT_HOME="${XBOT_HOME:-$HOME/.xbot}"
CONFIG_PATH="${CONFIG_PATH:-$XBOT_HOME/config.json}"
SERVICE_NAME="xbot-server"
DEFAULT_PORT="${PORT:-8082}"
CHANNEL="${CHANNEL:-}"  # stable, beta, or nightly

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || error "Missing required command: $1"
}

# Proxy a GitHub URL through the configured CDN mirror (if any).
# Usage: gh_url "https://github.com/ai-pivot/xbot/releases/download/v1.0/file"
# If GH_MIRROR is set, returns "https://${GH_MIRROR}/https://github.com/..."
# Otherwise returns the original URL unchanged.
# NOTE: CDN mirrors only proxy github.com / raw.githubusercontent.com,
# NOT api.github.com — API calls always go direct.
gh_url() {
    local url="$1"
    if [ -n "$GH_MIRROR" ] && echo "$url" | grep -qv 'api\.github\.com'; then
        echo "https://${GH_MIRROR}/${url}"
    else
        echo "$url"
    fi
}

detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "$os" in
        linux) ;;
        darwin) ;;
        *) error "Unsupported OS: $os. Use the PowerShell installer on Windows." ;;
    esac
    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) error "Unsupported architecture: $arch. Only amd64 and arm64 are supported." ;;
    esac
    echo "${os}-${arch}"
}

# Resolve version based on channel.
# For stable: uses /releases/latest (non-prerelease).
# For nightly: fixed tag "nightly" (CI overwrites it each merge to master).
#   Falls back to API lookup only if the fixed tag download fails.
# For beta: lists releases, finds latest v*-*beta* tag.
resolve_version() {
    if [ -n "${VERSION:-}" ]; then
        echo "$VERSION"
        return
    fi

    local ch="${CHANNEL:-stable}"
    local tag

    case "$ch" in
        stable)
            # /releases/latest returns the latest non-prerelease release
            tag=$(curl -fsSL "$(gh_url "https://api.github.com/repos/${REPO}/releases/latest")" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
            if [ -z "$tag" ]; then
                tag=$(curl -fsSL "$(gh_url "https://api.github.com/repos/${FALLBACK_REPO}/releases/latest")" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
            fi
            ;;
        nightly)
            # CI uses a fixed "nightly" tag — no API call needed.
            # This avoids requiring api.github.com access from China.
            tag="nightly"
            ;;
        beta)
            tag=$(curl -fsSL "$(gh_url "https://api.github.com/repos/${REPO}/releases?per_page=20")" 2>/dev/null | grep '"tag_name"' | grep -o '"v[^"]*-beta\.[0-9]*"' | head -1 | tr -d '"')
            if [ -z "$tag" ]; then
                tag=$(curl -fsSL "$(gh_url "https://api.github.com/repos/${FALLBACK_REPO}/releases?per_page=20")" 2>/dev/null | grep '"tag_name"' | grep -o '"v[^"]*-beta\.[0-9]*"' | head -1 | tr -d '"')
            fi
            ;;
        *)
            error "Unknown channel: $ch. Use 'stable', 'beta', or 'nightly'."
            ;;
    esac

    [ -n "$tag" ] || error "Failed to determine latest version for channel '$ch'. Set VERSION env var explicitly."
    echo "$tag"
}

# Interactive channel selection menu.
# Sets CHANNEL variable directly.
ask_channel() {
    if [ -n "$CHANNEL" ]; then
        case "$CHANNEL" in
            stable|beta|nightly) ;;
            *) error "Invalid CHANNEL='${CHANNEL}'. Use 'stable', 'beta', or 'nightly'." ;;
        esac
        return
    fi
    # Non-interactive: default to stable
    if [ -n "${NONINTERACTIVE:-}" ] || ! [ -c /dev/tty ] 2>/dev/null || ! [ -r /dev/tty ] 2>/dev/null; then
        info "Non-interactive mode (no /dev/tty). Defaulting to stable channel."
        info "Set CHANNEL=nightly or CHANNEL=beta to use a different channel."
        CHANNEL=stable
        return
    fi
    echo ""
    echo "Choose release channel:"
    echo "  1) stable   - Official releases (recommended)"
    echo "  2) beta     - Pre-release versions for testing"
    echo "  3) nightly  - Latest development builds (may be unstable)"
    printf "Select [1/2/3] (default 1): "
    local choice
    read -r choice </dev/tty || choice=1
    case "${choice:-1}" in
        3) CHANNEL=nightly ;;
        2) CHANNEL=beta ;;
        *) CHANNEL=stable ;;
    esac
}

# Non-interactive: use MODE env var. Interactive: prompt user.
# Sets MODE variable directly (no command substitution) so that prompts
# always reach the user's terminal, even when piped (curl | bash).
ask_mode() {
    # Env var takes priority (for non-interactive / CI usage)
    if [ -n "${MODE:-}" ]; then
        case "$MODE" in
            standalone|server-client) ;;
            *) error "Invalid MODE='${MODE}'. Use 'standalone' or 'server-client'." ;;
        esac
        return
    fi
    # In piped mode, stdin is the curl pipe. We need /dev/tty to talk to the user.
    if [ -n "${NONINTERACTIVE:-}" ] || ! [ -c /dev/tty ] 2>/dev/null || ! [ -r /dev/tty ] 2>/dev/null; then
        info "Non-interactive mode (no /dev/tty). Defaulting to standalone."
        info "Set MODE=server-client to install server-client mode."
        MODE=standalone
        return
    fi
    echo ""
    echo "Choose install mode:"
    echo "  1) standalone      - CLI runs locally in-process"
    echo "  2) server-client   - install local server service, CLI connects remotely"
    printf "Select [1/2] (default 1): "
    local choice
    read -r choice </dev/tty || choice=1
    case "${choice:-1}" in
        2) MODE=server-client ;;
        *) MODE=standalone ;;
    esac
}

# Generate random hex token without external dependencies
random_token() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 16
        return
    fi
    # Fallback: read from /dev/urandom (available on all Linux/macOS)
    if [ -r /dev/urandom ]; then
        od -A n -t x1 -N 16 /dev/urandom | tr -d ' \n'
        return
    fi
    # Last resort: python3
    if command -v python3 >/dev/null 2>&1; then
        python3 -c 'import secrets; print(secrets.token_hex(16))'
        return
    fi
    error "Cannot generate random token: need openssl, /dev/urandom, or python3"
}

backup_config() {
    if [ -f "$CONFIG_PATH" ]; then
        mkdir -p "$XBOT_HOME"
        local ts backup
        ts="$(date +%Y%m%d-%H%M%S)"
        backup="${CONFIG_PATH}.bak.${ts}"
        cp "$CONFIG_PATH" "$backup"
        info "Backed up existing config to ${backup}"
    fi
}

# Write config.json using jq (preferred) or python3 fallback.
write_config() {
    local mode="$1" port="$2" token="$3"
    mkdir -p "$XBOT_HOME"

    if command -v jq >/dev/null 2>&1; then
        write_config_jq "$mode" "$port" "$token"
    elif command -v python3 >/dev/null 2>&1; then
        write_config_python3 "$mode" "$port" "$token"
    else
        error "Need jq or python3 to write config.json"
    fi
}

write_config_jq() {
    local mode="$1" port="$2" token="$3"
    local changes=() preserved=()

    # Create config if missing or invalid
    if [ ! -f "$CONFIG_PATH" ] || ! jq empty "$CONFIG_PATH" 2>/dev/null; then
        echo '{}' > "$CONFIG_PATH"
    fi

    # Ensure top-level sections exist
    jq '{server: (.server // {}), web: (.web // {}), cli: (.cli // {}), admin: (.admin // {}), agent: (.agent // {})} * .' \
        "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"

    # _jq_set writes a value to config.json using jq.
    # Uses --argjson for JSON types (numbers, booleans) and --arg for strings.
    _jq_set() {
        local section="$1" key="$2" value="$3"
        case "$value" in
            true|false)
                jq --argjson v "$value" ".${section}.${key} = \$v" "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"
                ;;
            [0-9]*)
                # Only use --argjson for pure integers (no dots, no dashes)
                if [[ "$value" =~ ^[0-9]+$ ]]; then
                    jq --argjson v "$value" ".${section}.${key} = \$v" "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"
                else
                    jq --arg v "$value" ".${section}.${key} = \$v" "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"
                fi
                ;;
            *)
                jq --arg v "$value" ".${section}.${key} = \$v" "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"
                ;;
        esac
    }

    _set_if_missing() {
        local section="$1" key="$2" value="$3"
        local current
        current=$(jq -r ".${section}.${key} // empty" "$CONFIG_PATH" 2>/dev/null)
        if [ -z "$current" ]; then
            _jq_set "$section" "$key" "$value"
            changes+=("${section}.${key}=${value}")
        else
            preserved+=("${section}.${key}=${current}")
        fi
    }

    _set_always() {
        local section="$1" key="$2" value="$3"
        local old
        old=$(jq -r ".${section}.${key} // empty" "$CONFIG_PATH" 2>/dev/null)
        _jq_set "$section" "$key" "$value"
        if [ "$old" != "$value" ]; then
            changes+=("${section}.${key}=${value} (was ${old})")
        else
            preserved+=("${section}.${key}=${old}")
        fi
    }

    _set_if_missing admin token "$token"
    _set_if_missing agent work_dir "$HOME"
    _set_if_missing llm provider "openai"
    _set_if_missing llm model "gpt-4o-mini"
    _set_if_missing llm api_key ""
    _set_if_missing llm base_url ""

    if [ "$mode" = "server-client" ]; then
        _set_if_missing server host "127.0.0.1"
        _set_always server port "$port"
        _set_always web enable true
        _set_if_missing web host "127.0.0.1"
        _set_always web port "$port"
        _set_always cli server_url "ws://127.0.0.1:${port}"
        local admin_token
        admin_token=$(jq -r '.admin.token // empty' "$CONFIG_PATH")
        _set_always cli token "${admin_token:-$token}"
    else
        # standalone: the box should still be reachable in a browser right
        # after install — enable the web channel and pin its port.
        _set_if_missing web enable true
        _set_if_missing web host "0.0.0.0"
        _set_always web port "$PORT"
        local admin_token
        admin_token=$(jq -r '.admin.token // empty' "$CONFIG_PATH")
        _set_if_missing cli token "${admin_token:-$token}"
    fi

    for item in "${changes[@]+"${changes[@]}"}"; do
        [ -n "$item" ] && info "Config set: $item"
    done
    for item in "${preserved[@]+"${preserved[@]}"}"; do
        [ -n "$item" ] && warn "Config preserved: $item"
    done
}

write_config_python3() {
    local mode="$1" port="$2" token="$3"
    python3 - "$CONFIG_PATH" "$mode" "$port" "$token" "$HOME" <<'PY'
import json, os, sys
path, mode, port, token, home = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], sys.argv[5]
if os.path.exists(path):
    with open(path, 'r', encoding='utf-8') as f:
        try: cfg = json.load(f)
        except Exception: cfg = {}
else:
    cfg = {}
cfg.setdefault('server', {})
cfg.setdefault('web', {})
cfg.setdefault('cli', {})
cfg.setdefault('admin', {})
cfg.setdefault('agent', {})
cfg.setdefault('llm', {})
changes, preserved = [], []
def set_if_missing(s, k, v):
    if k not in cfg[s] or cfg[s][k] in (None, ''):
        cfg[s][k] = v; changes.append(f'{s}.{k}={v}')
    else:
        preserved.append(f'{s}.{k}={cfg[s][k]}')
def set_always(s, k, v):
    old = cfg[s].get(k); cfg[s][k] = v
    (changes if old != v else preserved).append(f'{s}.{k}={v}' + (f' (was {old})' if old != v else ''))
set_if_missing('admin', 'token', token)
set_if_missing('agent', 'work_dir', home)
set_if_missing('llm', 'provider', 'openai')
set_if_missing('llm', 'model', 'gpt-4o-mini')
set_if_missing('llm', 'api_key', '')
set_if_missing('llm', 'base_url', '')
if mode == 'server-client':
    set_if_missing('server', 'host', '127.0.0.1')
    set_always('server', 'port', port)
    set_always('web', 'enable', True)
    set_if_missing('web', 'host', '127.0.0.1')
    set_always('web', 'port', port)
    set_always('cli', 'server_url', f'ws://127.0.0.1:{port}')
    set_always('cli', 'token', cfg['admin'].get('token') or token)
else:
    # standalone: web reachable in a browser right after install.
    set_if_missing('web', 'enable', True)
    set_if_missing('web', 'host', '0.0.0.0')
    set_always('web', 'port', port)
    set_if_missing('cli', 'token', cfg['admin'].get('token') or token)
with open(path, 'w', encoding='utf-8') as f:
    json.dump(cfg, f, ensure_ascii=False, indent=2)
for c in changes: print(f'[INFO] Config set: {c}')
for p in preserved: print(f'[WARN] Config preserved: {p}', file=sys.stderr)
PY
}

# download_web_dist is the LEGACY inline web-dist download, used ONLY as the
# fallback when the installed binary predates the `setup` subcommand (e.g.
# curl'ing master install.sh while pinning an old VERSION). New releases do
# this via `xbot-cli setup` instead.
download_web_dist() {
    local version="$1" target_dir="$2"
    local dist_url="https://github.com/${REPO}/releases/download/${version}/xbot-web-dist.tar.gz"
    info "Downloading Web UI frontend..."
    mkdir -p "$target_dir"
    if curl -fSL "$(gh_url "$dist_url")" | tar xzf - -C "$target_dir" 2>/dev/null; then
        info "Web UI installed to ${target_dir} ✓"
    elif curl -fSL "$(gh_url "https://github.com/${FALLBACK_REPO}/releases/download/${version}/xbot-web-dist.tar.gz")" | tar xzf - -C "$target_dir" 2>/dev/null; then
        warn "Web UI downloaded from fallback repo ${FALLBACK_REPO}"
        info "Web UI installed to ${target_dir} ✓"
    else
        warn "Failed to download Web UI frontend. The server will run in API-only mode."
        warn "You can manually download it later from: ${dist_url}"
        warn "Extract to: ${target_dir}"
    fi
}

# Run the freshly-installed binary's `setup` subcommand: downloads the Web UI
# dist + built-in plugins (version-pinned to this release, checksum-verified)
# and activates channel plugins in config.json (channels.<name>.enabled=true).
# Runs in BOTH modes (standalone included — web/plugins are small and the user
# can flip to `xbot-cli serve` at any time). Replaces the old download_web_dist
# inline logic; one implementation (Go, cross-platform) shared with install.ps1.
# Soft-fail semantics: a non-zero exit warns with the remediation command but
# does NOT abort the install (old releases lack plugin tarballs → exit 3).
run_setup() {
    local version="$1"
    # Capability probe: the `setup` subcommand only exists in releases that
    # ship it. On an older binary, `setup ...` would be parsed as a PROMPT and
    # run the agent non-interactively (panic in the worst case — CI caught
    # exactly that with the v0.0.23 binary). Probe with `setup -h`: both old
    # and new binaries print help text and exit 0, but only the new one prints
    # the setup-specific usage line. Never invoke a subcommand blindly.
    if ! "${INSTALL_PATH}/${BINARY}" setup -h 2>/dev/null | grep -q "Usage: xbot-cli setup"; then
        warn "Installed binary does not support the setup subcommand (pre-setup release)."
        warn "Falling back to legacy Web UI download; built-in plugins are not available for this release."
        download_web_dist "$version" "${XBOT_HOME}/web/dist"
        return 0
    fi
    info "Setting up Web UI + built-in plugins (xbot-cli setup)..."
    if "${INSTALL_PATH}/${BINARY}" setup --tag "$version" --mirror "$GH_MIRROR"; then
        info "Web UI + built-in plugins installed"
    else
        local rc=$?
        warn "xbot-cli setup exited with code ${rc} (see messages above)."
        warn "The server will run without the Web UI / plugins until this is fixed."
        warn "Re-run later with: ${INSTALL_PATH}/${BINARY} setup"
    fi
}

# --- User-level systemd service (no sudo required) ---
write_systemd_user_unit() {
    local bin_path="$1" config_path="$2" unit_file="$3"
    local xbot_home work_dir
    xbot_home="$(cd "$XBOT_HOME" && pwd)"
    work_dir="$HOME"
    mkdir -p "$HOME/.config/systemd/user"
    cat > "$unit_file" <<EOF_UNIT
[Unit]
Description=xbot Agent Server (user)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=XBOT_HOME=${xbot_home}
Environment=PATH=${INSTALL_PATH}:/usr/local/bin:/usr/bin:/bin
WorkingDirectory=${work_dir}
ExecStart=${bin_path} serve --config ${config_path}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF_UNIT
}

install_systemd_user() {
    local bin_path="$1" config_path="$2"
    [ "$(uname -s)" = "Linux" ] || return 0
    info "Installing systemd --user service (no sudo required)..."
    write_systemd_user_unit "$bin_path" "$config_path" "$HOME/.config/systemd/user/${SERVICE_NAME}.service"
    info "systemd --user unit written: ${SERVICE_NAME}.service"
    if [ -z "${NONINTERACTIVE:-}" ]; then
        systemctl --user daemon-reload
        systemctl --user enable "$SERVICE_NAME"
        systemctl --user restart "$SERVICE_NAME"
        info "systemd --user service started: ${SERVICE_NAME}"
        info "  Logs: journalctl --user -u ${SERVICE_NAME} -f"
    else
        info "NONINTERACTIVE: skipped daemon-reload/enable/start"
    fi
    info "  Stop: systemctl --user stop ${SERVICE_NAME}"
}

install_launchd() {
    local bin_path="$1" config_path="$2"
    [ "$(uname -s)" = "Darwin" ] || return 0
    local plist="$HOME/Library/LaunchAgents/com.xbot.server.plist"
    mkdir -p "$HOME/Library/LaunchAgents" "$XBOT_HOME/logs"
    cat > "$plist" <<EOF_PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.xbot.server</string>
  <key>ProgramArguments</key>
  <array>
    <string>${bin_path}</string>
    <string>serve</string>
    <string>--config</string>
    <string>${config_path}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict>
    <key>SuccessfulExit</key><false/>
  </dict>
  <key>WorkingDirectory</key><string>${HOME}</string>
  <key>EnvironmentVariables</key><dict>
    <key>XBOT_HOME</key><string>${XBOT_HOME}</string>
  </dict>
  <key>StandardOutPath</key><string>${XBOT_HOME}/logs/xbot-server.log</string>
  <key>StandardErrorPath</key><string>${XBOT_HOME}/logs/xbot-server.err</string>
</dict></plist>
EOF_PLIST
    info "launchd plist written: ${plist}"
    if [ -z "${NONINTERACTIVE:-}" ]; then
        launchctl unload -w "$plist" >/dev/null 2>&1 || true
        launchctl load -w "$plist"
        info "launchd service loaded: com.xbot.server"
    else
        info "NONINTERACTIVE: skipped launchctl load"
    fi
    info "  Logs: ${XBOT_HOME}/logs/xbot-server.log"
    info "  Stop: launchctl unload -w ${plist}"
}

add_to_path() {
    case ":${PATH}:" in
        *":${INSTALL_PATH}:"*) return 0 ;;
    esac
    local profile=""
    if [ -n "${ZSH_VERSION:-}" ] || [ "$(basename "${SHELL:-}")" = "zsh" ]; then
        profile="$HOME/.zshrc"
    else
        profile="$HOME/.bashrc"
    fi
    if [ -f "$profile" ]; then
        if ! grep -qF "$INSTALL_PATH" "$profile" 2>/dev/null; then
            echo "" >> "$profile"
            echo "# Added by xbot installer" >> "$profile"
            echo "export PATH=\"${INSTALL_PATH}:\$PATH\"" >> "$profile"
            info "Added ${INSTALL_PATH} to PATH in ${profile}"
        fi
    fi
    export PATH="${INSTALL_PATH}:${PATH}"
}

# Ensure systemd --user lingering is enabled (so service runs at boot without login)
enable_linger() {
    [ "$(uname -s)" = "Linux" ] || return 0
    [ -z "${NONINTERACTIVE:-}" ] || return 0
    if command -v loginctl >/dev/null 2>&1; then
        if loginctl enable-linger "$(id -un)" 2>/dev/null; then
            info "Enabled lingering: server will start at boot"
        else
            warn "Could not enable linger (server starts on first login instead)"
            warn "  Run: loginctl enable-linger $(id -un)"
        fi
    fi
}

# install_binary_from_release <version>
# Downloads the release binary for <version>, verifies its checksum, stops any
# running instance and installs it to ${INSTALL_PATH}/${BINARY}.
# Extracted from main() so the installer can RETRY on a different channel when
# the resolved release turns out to predate `xbot-cli setup`.
install_binary_from_release() {
    local version="$1"
    local download_url="https://github.com/${REPO}/releases/download/${version}/xbot-cli-${PLATFORM}"

    # Try new repo first; fall back to old repo during the CjiW → ai-pivot move.
    if ! curl -fSL -o "${TMPDIR}/${BINARY}" "$(gh_url "$download_url")"; then
        local fallback="https://github.com/${FALLBACK_REPO}/releases/download/${version}/xbot-cli-${PLATFORM}"
        warn "Release not found on ${REPO}, trying fallback ${FALLBACK_REPO}..."
        download_url="$fallback"
        if ! curl -fSL -o "${TMPDIR}/${BINARY}" "$(gh_url "$download_url")"; then
            return 1
        fi
    fi

    if command -v shasum >/dev/null 2>&1; then
        info "Verifying checksum..."
        curl -fsSL "$(gh_url "https://github.com/${REPO}/releases/download/${version}/checksums.txt")" -o "${TMPDIR}/checksums.txt" 2>/dev/null \
            || curl -fsSL "$(gh_url "https://github.com/${FALLBACK_REPO}/releases/download/${version}/checksums.txt")" -o "${TMPDIR}/checksums.txt" 2>/dev/null \
            || warn "Checksum file not found, skipping verification."
        if [ -f "${TMPDIR}/checksums.txt" ]; then
            local expected actual
            expected=$(grep "xbot-cli-${PLATFORM}" "${TMPDIR}/checksums.txt" | awk '{print $1}')
            actual=$(shasum -a 256 "${TMPDIR}/${BINARY}" | awk '{print $1}')
            if [ -n "$expected" ] && [ "$expected" != "$actual" ]; then
                error "Checksum mismatch! Expected: ${expected}, Got: ${actual}"
            fi
            info "Checksum verified ✓"
        fi
    fi

    # Stop running xbot-cli before overwriting the binary
    if [ -x "${INSTALL_PATH}/${BINARY}" ]; then
        info "Checking for running xbot-cli..."
        if systemctl --user status "$SERVICE_NAME" >/dev/null 2>&1; then
            systemctl --user stop "$SERVICE_NAME" 2>/dev/null || true
        fi
        pkill -f "${INSTALL_PATH}/${BINARY}" 2>/dev/null || true
        for i in 1 2 3 4 5; do
            pgrep -f "${INSTALL_PATH}/${BINARY}" >/dev/null 2>&1 || break
            sleep 1
        done
    fi

    chmod +x "${TMPDIR}/${BINARY}"
    mkdir -p "$INSTALL_PATH"
    mv "${TMPDIR}/${BINARY}" "${INSTALL_PATH}/${BINARY}"
}

main() {
    # Parse --channel argument from command line
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --channel)
                CHANNEL="$2"
                shift 2
                ;;
            --channel=*)
                CHANNEL="${1#*=}"
                shift
                ;;
            *)
                shift
                ;;
        esac
    done

    echo ""
    echo "  ╔══════════════════════════════════════╗"
    echo "  ║         xbot-cli Installer           ║"
    echo "  ╚══════════════════════════════════════╝"
    echo ""

    require_cmd curl
    PLATFORM=$(detect_platform)

    # Ask for channel if not specified (interactive menu)
    ask_channel

    VERSION=$(resolve_version)
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/xbot-cli-${PLATFORM}"

    info "Platform:  ${PLATFORM}"
    info "Channel:   ${CHANNEL}"
    info "Version:   ${VERSION}"
    info "URL:       ${DOWNLOAD_URL}"
    info "Install:   ${INSTALL_PATH}/${BINARY}"
    info "Config:    ${CONFIG_PATH}"
    if [ -n "$GH_MIRROR" ]; then
        info "Mirror:    ${GH_MIRROR} (GitHub CDN proxy)"
    fi
    echo ""

    ask_mode
    TOKEN=$(random_token)
    PORT="${PORT:-$DEFAULT_PORT}"
    if [ -z "${PORT:-}" ] && [ "$MODE" = "server-client" ] && [ -z "${NONINTERACTIVE:-}" ] && [ -e /dev/tty ]; then
        printf "Server port (HTTP + WebSocket + Web UI) [${DEFAULT_PORT}]: "
        read -r input_port </dev/tty
        PORT="${input_port:-$DEFAULT_PORT}"
    fi

    info "Downloading..."
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT
    if [ "${INSTALL_LOCAL_BINARY:-}" = "1" ]; then
        # CI mode: a pre-built binary from THIS branch already sits at
        # ${INSTALL_PATH}/${BINARY} (caller-built via `go build`). Skips the
        # release download/checksum — install.sh's config/setup/service logic
        # is what's under test, and the `setup` subcommand only exists in
        # this branch (a downloaded old release binary lacks it).
        if [ ! -x "${INSTALL_PATH}/${BINARY}" ]; then
            error "INSTALL_LOCAL_BINARY=1 but ${INSTALL_PATH}/${BINARY} not found — build it first (go build -o ${INSTALL_PATH}/${BINARY} ./cmd/xbot-cli)"
        fi
        info "Using pre-built local binary at ${INSTALL_PATH}/${BINARY} (INSTALL_LOCAL_BINARY=1)"
    else
        if ! install_binary_from_release "$VERSION"; then
            error "Download failed from both repos. Check the version and platform."
        fi
    fi
    info "Binary installed to ${INSTALL_PATH}/${BINARY}"

    add_to_path

    backup_config
    write_config "$MODE" "$PORT" "$TOKEN"

    # ── Channel capability probe ────────────────────────────────────────────
    # Older stable releases ship a binary that PREDATES `xbot-cli setup`. On
    # those, run_setup() would fall back to a web-only download and the
    # built-in plugins would silently never appear — an agent following the
    # published one-liner would end up with an incomplete install.
    #
    # Probe `setup -h` (both old and new binaries exit 0, but only a binary
    # that HAS the subcommand prints its usage line — never invoke blindly) and,
    # when it is missing, retry once on nightly: nightly always carries the
    # newest build, so `setup` (and therefore the plugins) exists there.
    if [ "${INSTALL_LOCAL_BINARY:-}" != "1" ] && [ "$CHANNEL" != "nightly" ] \
        && ! "${INSTALL_PATH}/${BINARY}" setup -h 2>/dev/null | grep -q "Usage: xbot-cli setup"; then
        warn "Channel '${CHANNEL}' resolved to ${VERSION}, which predates 'xbot-cli setup'."
        warn "  Installing it would give you the Web UI WITHOUT the built-in plugins."
        warn "Retrying with the nightly channel (latest builds include 'setup')..."
        CHANNEL=nightly
        # nightly is a FIXED tag (overwritten on every master push) — no
        # resolve_version() call needed (and calling it would short-circuit:
        # VERSION is already set to the old stable tag, so it would echo the
        # stale value instead of resolving the new channel).
        if install_binary_from_release "nightly"; then
            VERSION="nightly"
            info "Switched to nightly — Web UI + built-in plugins available"
        else
            warn "nightly retry failed; continuing with ${VERSION} (plugins unavailable)."
        fi
    fi

    # Web UI + built-in plugins + channel activation config (both modes —
    # standalone users can flip to `xbot-cli serve` at any time; the binary
    # knows its own release version and downloads matching artifacts).
    run_setup "$VERSION"

    if [ "$MODE" = "server-client" ]; then
        case "$(uname -s)" in
            Linux)
                install_systemd_user "${INSTALL_PATH}/${BINARY}" "$CONFIG_PATH"
                enable_linger
                ;;
            Darwin)
                install_launchd "${INSTALL_PATH}/${BINARY}" "$CONFIG_PATH"
                ;;
        esac
    fi

    echo ""
    info "✅ xbot-cli ${VERSION} installed to ${INSTALL_PATH}/${BINARY}"
    info "Mode: ${MODE}"
    info "Config: ${CONFIG_PATH}"
    if [ "$MODE" = "server-client" ]; then
        info "Web UI: http://localhost:${PORT}"
        info "CLI will connect to the configured local server (see ${CONFIG_PATH})"
        case "$(uname -s)" in
            Linux)
                info "Service: systemd --user (${SERVICE_NAME})"
                info "  Logs:  journalctl --user -u ${SERVICE_NAME} -f"
                info "  Stop:  systemctl --user stop ${SERVICE_NAME}"
                info "  Start: systemctl --user start ${SERVICE_NAME}"
                ;;
            Darwin)
                info "Service: launchd (com.xbot.server)"
                info "  Logs:  ${XBOT_HOME}/logs/xbot-server.log"
                info "  Stop:  launchctl unload -w ~/Library/LaunchAgents/com.xbot.server.plist"
                ;;
        esac
    else
        info "Run '${BINARY}' to start."
        info "Web UI + built-in plugins were installed by 'xbot-cli setup' (see ${XBOT_HOME})."
        info "Want the local web server too? Run: ${BINARY} serve  (then open http://localhost:8082)"
    fi
    if ! command -v "$BINARY" >/dev/null 2>&1; then
        echo ""
        warn "Note: ${INSTALL_PATH} is not yet in your shell PATH."
        warn "  Run: source ~/.bashrc"
        warn "  Or restart your shell."
    fi
    echo ""
}

main "$@"
