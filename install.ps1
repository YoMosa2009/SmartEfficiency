#Requires -Version 5.1
<#
.SYNOPSIS
  Installs SmartEfficiency (the Go version) and registers it to run automatically at logon.

.DESCRIPTION
  Downloads the latest release for this machine's architecture from GitHub,
  installs it to %LOCALAPPDATA%\SmartEfficiencyGo, and registers the
  daemon+tray as Task Scheduler tasks (elevating once, via a UAC prompt, if
  not already running as Administrator - registering scheduled tasks
  requires it).

  Deliberately installs to a folder named "SmartEfficiencyGo", not
  "SmartEfficiency" - the latter may already be in use by the original
  PowerShell version of this project on the same machine, and the two must
  never share a config directory.

.EXAMPLE
  irm https://raw.githubusercontent.com/YoMosa2009/SmartEfficiency/main/install.ps1 | iex
#>

$ErrorActionPreference = 'Stop'

$Repo = 'YoMosa2009/SmartEfficiency'
$InstallDir = Join-Path $env:LOCALAPPDATA 'SmartEfficiencyGo'

function Get-Arch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'ARM64' { return 'arm64' }
        default { return 'amd64' }
    }
}

function Test-Elevated {
    $id = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $p = New-Object System.Security.Principal.WindowsPrincipal($id)
    return $p.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
}

Write-Host "SmartEfficiency installer" -ForegroundColor Cyan
$arch = Get-Arch
Write-Host "  Architecture: windows-$arch"
Write-Host "  Install dir:  $InstallDir"

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

Write-Host "Fetching latest release info..."
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ 'User-Agent' = 'SmartEfficiency-installer' }
Write-Host "  Latest version: $($release.tag_name)"

$binaries = @('smarteffd', 'smarteff-tray', 'smarteff-update')
foreach ($bin in $binaries) {
    $assetName = "$bin-windows-$arch.exe"
    $asset = $release.assets | Where-Object { $_.name -eq $assetName }
    if (-not $asset) {
        throw "No release asset named '$assetName' found in the latest release. (Available: $($release.assets.name -join ', '))"
    }
    $dest = Join-Path $InstallDir "$bin.exe"
    Write-Host "  Downloading $assetName ..."
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $dest -UseBasicParsing
}

Write-Host "Registering autostart (Task Scheduler)..." -ForegroundColor Cyan
$daemonExe = Join-Path $InstallDir 'smarteffd.exe'

if (Test-Elevated) {
    & $daemonExe -install
    if ($LASTEXITCODE -ne 0) { throw "Autostart registration failed - see output above." }
} else {
    Write-Host "  Registering a scheduled task requires Administrator - a UAC prompt will appear." -ForegroundColor Yellow
    $proc = Start-Process -FilePath $daemonExe -ArgumentList '-install' -Verb RunAs -Wait -PassThru
    if ($proc.ExitCode -ne 0) { throw "Autostart registration failed (exit code $($proc.ExitCode))." }
}

Write-Host ""
Write-Host "Installed SmartEfficiency $($release.tag_name)." -ForegroundColor Green
Write-Host "The daemon and tray icon are running now and will start automatically at every logon."
Write-Host "Click the tray icon's 'Open Dashboard' item for live status, or run:"
Write-Host "  & `"$InstallDir\smarteffd.exe`" -uninstall"
Write-Host "to remove the autostart registration later."
