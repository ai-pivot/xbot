<#
.SYNOPSIS
    xbot-cli installer for mainland China (CDN mirror mode)
.DESCRIPTION
    Proxies all GitHub downloads through a CDN mirror.
    Default mirror: ghfast.top (verified working in mainland China).
.PARAMETER GhMirror
    Force a specific mirror host (e.g. "ghfast.top").
.EXAMPLE
    # One-liner via ghfast.top (default)
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
    [string]$GhMirror = ""
)

$ErrorActionPreference = "Stop"

# Default mirror candidates — ordered by reliability in mainland China
$DefaultMirrors = @("ghfast.top", "gh-proxy.com", "ghps.cc")

# GitHub ref (branch/tag) to download install.ps1 from.
# Defaults to master; can be overridden for testing: $env:GITHUB_REF="my-branch"
$GitHubRef = if ($env:GITHUB_REF) { $env:GITHUB_REF } else { "master" }

function Write-Info  { param([string]$Msg) Write-Host "[INFO] $Msg" -ForegroundColor Green }
function Write-Warn  { param([string]$Msg) Write-Host "[WARN] $Msg" -ForegroundColor Yellow }
function Write-Err   { param([string]$Msg) Write-Host "[ERROR] $Msg" -ForegroundColor Red; throw $Msg }

# ---------------------------------------------------------------------------
# Download install.ps1 through a mirror, with content validation.
# Returns the local file path on success, or $null on failure.
# ---------------------------------------------------------------------------
function Try-Download {
    param(
        [string]$Mirror,    # e.g. "ghfast.top" or "" (direct)
        [string]$RawUrl,    # e.g. "https://raw.githubusercontent.com/..."
        [string]$Dest       # local file path to write to
    )

    $url = if ($Mirror) { "https://${Mirror}/${RawUrl}" } else { $RawUrl }

    Write-Info "Trying to download from $url..."
    try {
        Invoke-WebRequest -Uri $url -OutFile $Dest -TimeoutSec 30 -UseBasicParsing
        # Verify the download is a real PowerShell script (not an error page)
        $firstLine = Get-Content $Dest -TotalCount 1 -ErrorAction SilentlyContinue
        if ($firstLine -and $firstLine.TrimStart().StartsWith("<#")) {
            return $Dest
        }
        Write-Warn "Downloaded file is not a valid PowerShell script, trying next..."
        return $null
    } catch {
        return $null
    }
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "  ===============================================" -ForegroundColor Cyan
Write-Host "     xbot-cli Installer (China Mirror Mode)" -ForegroundColor Cyan
Write-Host "  ===============================================" -ForegroundColor Cyan
Write-Host ""

# Step 1: Determine mirror
if (-not $GhMirror) { $GhMirror = $env:GH_MIRROR }
if (-not $GhMirror) {
    $GhMirror = "ghfast.top"
    Write-Info "Using default mirror: $GhMirror"
} else {
    Write-Info "Using mirror: $GhMirror"
}
Write-Host ""

# Step 2: Check for local install.ps1 (cloned repo)
$scriptDir = Split-Path -Parent $MyInvocation.PSCommandPath
if (-not $scriptDir) { $scriptDir = $PSScriptRoot }
$installScript = $null

if ($scriptDir) {
    $localInstall = Join-Path $scriptDir "install.ps1"
    if (Test-Path $localInstall) {
        Write-Info "Using local install.ps1 from repository"
        $installScript = $localInstall
    }
}

# Step 3: Download install.ps1 — try all mirrors with fallback
if (-not $installScript) {
    $tmpFile = Join-Path ([System.IO.Path]::GetTempPath()) "xbot-install.ps1"
    $urls = @(
        "https://raw.githubusercontent.com/ai-pivot/xbot/${GitHubRef}/scripts/install.ps1",
        "https://raw.githubusercontent.com/CjiW/xbot/${GitHubRef}/scripts/install.ps1"
    )

    # Build mirror list: selected mirror first, then defaults, then direct
    $mirrorsToTry = @($GhMirror)
    foreach ($m in $DefaultMirrors) {
        if ($m -ne $GhMirror) { $mirrorsToTry += $m }
    }
    $mirrorsToTry += ""  # direct (no mirror)

    foreach ($m in $mirrorsToTry) {
        foreach ($rawUrl in $urls) {
            $result = Try-Download -Mirror $m -RawUrl $rawUrl -Dest $tmpFile
            if ($result) {
                $GhMirror = $m  # update to the one that actually worked
                $installScript = $result
                break
            }
        }
        if ($installScript) { break }
    }

    if (-not $installScript) {
        Write-Err "Failed to download install.ps1. Check your network or set -GhMirror manually."
    }
}

# Step 4: Set GH_MIRROR env var and run install.ps1
$env:GH_MIRROR = $GhMirror
Write-Info "Launching installer..."
Write-Host ""

# Pass through any remaining args (Mode, Channel, etc.)
& $installScript @args
