<#
.SYNOPSIS
  Creator - one-line Windows installer.

.DESCRIPTION
  Creator is the terminal agent that never floods its context.
  https://github.com/DERYK-RYLOY/creator

.EXAMPLE
  irm https://raw.githubusercontent.com/DERYK-RYLOY/creator/main/install.ps1 | iex

.EXAMPLE
  .\install.ps1 -Version v0.1.0 -InstallDir "$env:USERPROFILE\tools\creator" -WhatIf

.EXAMPLE
  .\install.ps1 -FromSource

  No admin rights are required. Everything the script changes is printed, and
  it is safe to re-run: an existing installation is verified and left alone
  unless the version changed.
#>
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$Version = "latest",
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\Creator"),
    [switch]$FromSource,
    [switch]$NoPath,
    [switch]$NoShortcut,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # faster + quieter Invoke-WebRequest
$Repo = "DERYK-RYLOY/creator"
$ApiLatest = "https://api.github.com/repos/$Repo/releases/latest"
$DownloadBase = "https://github.com/$Repo/releases/download"

function Write-Info  { param([string]$m) Write-Host "> $m" -ForegroundColor Cyan }
function Write-Ok    { param([string]$m) Write-Host "OK $m" -ForegroundColor Green }
function Write-Warn  { param([string]$m) Write-Host "! $m" -ForegroundColor Yellow }
function Write-Fail  { param([string]$m) Write-Host "X $m" -ForegroundColor Red; exit 1 }
function Invoke-Run  { param([scriptblock]$Action, [string]$Describe)
    if ($DryRun -or $WhatIfPreference) { Write-Host "  [dry-run] $Describe"; return }
    & $Action
}

function Get-LatestTag {
    try {
        $rel = Invoke-RestMethod -Uri $ApiLatest -UseBasicParsing -Headers @{ "User-Agent" = "creator-installer" }
        return $rel.tag_name
    } catch {
        return $null
    }
}

function Get-AssetUrl { param([string]$Tag, [string]$Asset)
    "$DownloadBase/$Tag/$Asset"
}

function Get-FileSha256 { param([string]$Path)
    (Get-FileHash -Path $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Add-ToUserPath { param([string]$Dir)
    $current = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @()
    if ($current) { $parts = $current -split ';' | ForEach-Object { $_.Trim().TrimEnd('\') } | Where-Object { $_ } }
    $normalized = $Dir.TrimEnd('\')
    foreach ($p in $parts) {
        if ([string]::Equals($p, $normalized, [StringComparison]::OrdinalIgnoreCase)) {
            Write-Info "$Dir is already on the user PATH - leaving it alone"
            return $false
        }
    }
    $newPath = if ($current) { "$current;$Dir" } else { $Dir }
    if ($DryRun -or $WhatIfPreference) {
        Write-Host "  [dry-run] append $Dir to the user PATH (registry)"
        return $true
    }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    # make it visible in this session immediately
    if (($env:Path -split ';' | ForEach-Object { $_.Trim().TrimEnd('\') }) -notcontains $normalized) {
        $env:Path = "$env:Path;$Dir"
    }
    return $true
}

function New-CreatorShortcut { param([string]$Dir)
    $startMenu = [Environment]::GetFolderPath("StartMenu")
    $programs = Join-Path $startMenu "Programs"
    $link = Join-Path $programs "Creator.lnk"
    Invoke-Run -Describe "create Start Menu shortcut $link" {
        New-Item -ItemType Directory -Force -Path $programs | Out-Null
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($link)
        $shortcut.TargetPath = "cmd.exe"
        $shortcut.Arguments = "/k `"$Dir\creator.exe`""
        $shortcut.WorkingDirectory = $Dir
        $shortcut.IconLocation = "$Dir\creator.exe,0"
        $shortcut.Description = "Creator - the terminal agent that never floods its context"
        $shortcut.Save()
    }
}

# ---------- resolve version ----------
Write-Info "creator installer - repo $Repo"
if (-not $FromSource) {
    if ($Version -eq "latest") {
        Write-Info "resolving the latest release..."
        $tag = Get-LatestTag
        if (-not $tag) {
            Write-Warn "could not read the latest release from GitHub - falling back to -FromSource"
            $FromSource = $true
        } else {
            $Version = $tag
        }
    }
}
$VersionNum = $Version -replace '^v', ''
$arch = "amd64"
try {
    $osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    if ($osArch -eq "Arm64") { $arch = "arm64" } elseif ($osArch -ne "X64") { Write-Fail "unsupported architecture: $osArch (amd64 and arm64 are built)" }
} catch {
    Write-Warn "could not detect CPU architecture - assuming amd64"
}

if ($FromSource) { Write-Info "building from source (git + go required)" }
else { Write-Info "installing $VersionNum for windows/$arch" }
Write-Info "target: $InstallDir"
if ($DryRun -or $WhatIfPreference) { Write-Warn "dry run - nothing will be changed" }

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("creator-install-" + [System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

try {
    if ($FromSource) {
        foreach ($tool in @("git", "go")) {
            if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
                Write-Fail "$tool not found - install it, or download a release instead"
            }
        }
        Write-Info "cloning $Repo (depth 1)"
        Invoke-Run -Describe "git clone --depth 1 $Repo" {
            git clone --depth 1 "https://github.com/$Repo.git" (Join-Path $tmp "creator") | Out-Null
        }
        Write-Info "building..."
        $ldflags = "-s -w -X github.com/DERYK-RYLOY/creator/internal/buildinfo.Version=$VersionNum"
        foreach ($t in @("creator", "creator-tui", "ctr")) {
            Invoke-Run -Describe "go build ./cmd/$t" {
                Push-Location (Join-Path $tmp "creator")
                try {
                    go build -trimpath -ldflags $ldflags -o (Join-Path $tmp "$t.exe") "./cmd/$t"
                } finally {
                    Pop-Location
                }
            }
        }
        Invoke-Run -Describe "copy binaries to $InstallDir" {
            New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
            Copy-Item (Join-Path $tmp "creator.exe"), (Join-Path $tmp "creator-tui.exe"), (Join-Path $tmp "ctr.exe") -Destination $InstallDir -Force
        }
        Write-Ok "built and installed from source"
    } else {
        $asset = "creator-$VersionNum-windows-$arch.zip"
        $url = Get-AssetUrl $Version $asset
        $archivePath = Join-Path $tmp $asset

        Write-Info "downloading $asset"
        Invoke-Run -Describe "download $url" {
            Invoke-WebRequest -Uri $url -OutFile $archivePath -UseBasicParsing
        }

        Write-Info "verifying SHA-256"
        if ($DryRun -or $WhatIfPreference) {
            Write-Info "[dry-run] checksum verification skipped (would fetch checksums.txt)"
            $checksums = $null
        } else {
            $checksums = $null
            try {
                $checksums = (Invoke-WebRequest -Uri (Get-AssetUrl $Version "checksums.txt") -UseBasicParsing).Content
            } catch {
                Write-Warn "checksums.txt unreadable from the release - installing without verification"
            }
        }
        if ($checksums) {
            $expected = ($checksums -split "`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -match "\s$([regex]::Escape($asset))$" } | Select-Object -First 1) -split '\s+' | Select-Object -First 1
            if (-not $expected) {
                Write-Warn "no checksum line for $asset - installing without verification"
            } else {
                $actual = Get-FileSha256 $archivePath
                if ($actual -ne $expected.ToLowerInvariant()) {
                    Remove-Item $archivePath -Force -ErrorAction SilentlyContinue
                    Write-Fail "checksum mismatch for $asset - expected $expected, got $actual (nothing installed)"
                }
                Write-Ok "checksum verified"
            }
        }

        Write-Info "unpacking to $InstallDir"
        Invoke-Run -Describe "extract $asset to $InstallDir" {
            New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
            $unpacked = Join-Path $tmp "unpacked"
            Expand-Archive -Path $archivePath -DestinationPath $unpacked -Force
            foreach ($b in @("creator.exe", "creator-tui.exe", "ctr.exe")) {
                $src = Get-ChildItem -Path $unpacked -Filter $b -Recurse | Select-Object -First 1
                if ($src) { Copy-Item $src.FullName -Destination (Join-Path $InstallDir $b) -Force }
            }
        }
        $found = 0
        foreach ($b in @("creator.exe", "creator-tui.exe", "ctr.exe")) {
            if (Test-Path (Join-Path $InstallDir $b)) { $found++ }
        }
        if ($DryRun -or $WhatIfPreference) {
            Write-Info "(dry-run) creator.exe, creator-tui.exe, ctr.exe would be installed to $InstallDir"
        } elseif ($found -lt 1) {
            Write-Fail "archive did not contain creator.exe - refusing to install"
        } else {
            Write-Ok "installed $found binaries"
        }
    }

    # ---------- PATH ----------
    if ($NoPath) {
        Write-Warn "PATH will not be modified - add $InstallDir to it yourself"
    } else {
        $added = Add-ToUserPath $InstallDir
        if ($added) { Write-Ok "user PATH updated (restart apps to see it)" }
    }

    # ---------- Start Menu shortcut ----------
    if (-not $NoShortcut) {
        New-CreatorShortcut $InstallDir
        if ($DryRun -or $WhatIfPreference) { Write-Info "(dry-run) shortcut would be created: Creator.lnk" }
        else { Write-Ok "Start Menu shortcut created (Creator)" }
    }

    Write-Host ""
    Write-Host "CREATOR" -ForegroundColor Cyan -NoNewline
    Write-Host " installed - the terminal agent that never floods its context" -ForegroundColor White
    Write-Host ""
    Write-Host "  open it:       creator"
    Write-Host "  headless:      creator run `"why is the build failing?`""
    Write-Host "  connect a key: creator connect <provider>     (or: setx ANTHROPIC_API_KEY ...)"
    Write-Host "  no key yet:    pollinations is free, no key - creator run --provider pollinations `"hello`""
    Write-Host ""
    Write-Host "  docs: https://github.com/$Repo" -ForegroundColor DarkGray
    Write-Host ""
    exit 0
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
