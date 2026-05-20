<#
.SYNOPSIS
    xbot-cli installer for mainland China (CDN mirror mode)
.DESCRIPTION
    Automatically selects a reachable GitHub CDN mirror and proxies all
    downloads through it.  Zero configuration required.
.PARAMETER GhMirror
    Force a specific mirror host (e.g. "ghfast.top").
.PARAMETER MirrorList
    Space-separated list of mirrors to try (override defaults).
.EXAMPLE
    # One-liner via ghfast.top
    irm https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.ps1 | iex
.EXAMPLE
    # One-liner via gh-proxy.com
    irm https://gh-proxy.com/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.ps1 | iex
.EXAMPLE
    # From a cloned repo
    .\install-cn.ps1
.EXAMPLE
    # Force a specific mirror
    $env:GH_MIRROR="ghfast.top"; .\install-cn.ps1
#>

param(
    [string]$GhMirror = "",
    [string]$MirrorList = ""
)

$ErrorActionPreference = "Stop"

# Default mirror candidates — ordered by reliability in mainland China
$DefaultMirrors = @("ghfast.top", "gh-proxy.com", "ghps.cc")

function Write-Info  { param([string]$Msg) Write-Host "[INFO] $Msg" -ForegroundColor Green }
function Write-Warn  { param([string]$Msg) Write-Host "[WARN] $Msg" -ForegroundColor Yellow }
function Write-Err   { param([string]$Msg) Write-Host "[ERROR] $Msg" -ForegroundColor Red; throw $Msg }

# ---------------------------------------------------------------------------
# Detect the best reachable mirror (3s timeout per candidate)
# ---------------------------------------------------------------------------
function Select-Mirror {
    param([string[]]$Candidates)
    foreach ($m in $Candidates) {
        try {
            $null = Invoke-WebRequest -Uri "https://$m/https://github.com" `
                -TimeoutSec 5 -UseBasicParsing -ErrorAction Stop
            return $m
        } catch {}
    }
    return ""
}

# ---------------------------------------------------------------------------
# Locate the real install.ps1 (local repo or download via mirror)
# ---------------------------------------------------------------------------
function Find-InstallScript {
    param([string]$SelectedMirror)

    # 1. Check if install.ps1 exists alongside this script (cloned repo)
    $scriptDir = Split-Path -Parent $MyInvocation.PSCommandPath
    if (-not $scriptDir) { $scriptDir = $PSScriptRoot }
    if ($scriptDir) {
        $localInstall = Join-Path $scriptDir "install.ps1"
        if (Test-Path $localInstall) {
            Write-Info "Using local install.ps1 from repository"
            return $localInstall
        }
    }

    # 2. Download install.ps1 through the selected mirror
    $tmpFile = Join-Path ([System.IO.Path]::GetTempPath()) "xbot-install.ps1"
    $urls = @(
        "https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1",
        "https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.ps1"
    )

    foreach ($rawUrl in $urls) {
        $proxiedUrl = if ($SelectedMirror) { "https://${SelectedMirror}/${rawUrl}" } else { $rawUrl }
        Write-Info "Trying to download install.ps1 from $proxiedUrl..."
        try {
            Invoke-WebRequest -Uri $proxiedUrl -OutFile $tmpFile -TimeoutSec 30 -UseBasicParsing
            return $tmpFile
        } catch {}
    }

    Write-Err "Failed to download install.ps1. Check your network or set -GhMirror manually."
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "  ===============================================" -ForegroundColor Cyan
Write-Host "     xbot-cli Installer (China Mirror Mode)" -ForegroundColor Cyan
Write-Host "  ===============================================" -ForegroundColor Cyan
Write-Host ""

# Step 1: Select mirror (unless user forced one via param or env var)
if (-not $GhMirror) { $GhMirror = $env:GH_MIRROR }
if (-not $GhMirror) {
    Write-Info "Auto-detecting best CDN mirror..."
    $mirrors = if ($MirrorList) { $MirrorList -split "\s+" } else { $DefaultMirrors }
    $GhMirror = Select-Mirror -Candidates $mirrors
}

if ($GhMirror) {
    Write-Info "Using mirror: $GhMirror"
} else {
    Write-Warn "No CDN mirror reachable -- will try direct GitHub."
    Write-Warn "If download fails, set mirror manually:"
    Write-Warn '  $env:GH_MIRROR="ghfast.top"; .\install-cn.ps1'
}
Write-Host ""

# Step 2: Find the real install.ps1
$installScript = Find-InstallScript -SelectedMirror $GhMirror

# Step 3: Set GH_MIRROR env var and run install.ps1
$env:GH_MIRROR = $GhMirror
Write-Info "Launching installer..."
Write-Host ""

# Pass through any remaining args (Mode, Channel, etc.)
& $installScript @args
