# SPDX-License-Identifier: GPL-3.0-only
#
# package-release.ps1 — Assembles the portable release distribution.
#
# Usage (from studio/BeamNGModStudio):
#   node build/collect-notices.mjs
#   powershell -File build/windows/package-release.ps1
#
# Prerequisites:
#   - Production binary built at bin\beamngmodstudio.exe
#   - Root LICENSE (GPL-3.0) at ..\..\LICENSE
#   dist\third-party\ populated by build/collect-notices.mjs
#
# Output:
#   dist\BeamWorldsModStudio-windows-amd64.zip
#   dist\SHA256SUMS

param(
    [string]$BinDir = "bin",
    [string]$DistDir = "dist",
    [string]$AppName = "beamngmodstudio"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$binary       = Join-Path $BinDir "$AppName.exe"
$rootLicense  = Join-Path (Join-Path ".." "..") "LICENSE"
$noticesDir   = Join-Path $DistDir "third-party"

# Validate all required inputs exist
$missing = @()
if (!(Test-Path $binary))       { $missing += "Binary: $binary" }
if (!(Test-Path $rootLicense))  { $missing += "GPL-3.0 LICENSE: $rootLicense" }
foreach ($notice in @("NOTICES.txt", "ai-runtime\LICENSE-MIT", "ai-runtime\THIRD-PARTY-NOTICES.txt")) {
    if (!(Test-Path (Join-Path $noticesDir $notice))) { $missing += "Third-party notice: $notice (run node build/collect-notices.mjs first)" }
}
if ($missing.Count -gt 0) {
    Write-Error ("Missing required files:`n  " + ($missing -join "`n  "))
    exit 1
}

$distName = "BeamWorldsModStudio-windows-amd64"
$stageDir = Join-Path $DistDir $distName
$zipPath  = Join-Path $DistDir "$distName.zip"

# Clean previous staging
if (Test-Path $stageDir) { Remove-Item -Recurse -Force $stageDir }
if (Test-Path $zipPath)  { Remove-Item -Force $zipPath }

# Stage files
New-Item -ItemType Directory -Force -Path $stageDir | Out-Null

Copy-Item $binary (Join-Path $stageDir "$AppName.exe")
Copy-Item $rootLicense (Join-Path $stageDir "LICENSE")

# Copy the entire third-party directory (NOTICES.txt + ai-runtime/)
Copy-Item $noticesDir (Join-Path $stageDir "third-party") -Recurse

# Create ZIP
Compress-Archive -Path "$stageDir\*" -DestinationPath $zipPath -Force
$zipSize = (Get-Item $zipPath).Length
Write-Host "Portable ZIP: $zipPath ($([math]::Round($zipSize / 1MB, 1)) MB)"

# Generate SHA256SUMS for all release files in dist/
Push-Location $DistDir
$releaseFiles = Get-ChildItem -File | Where-Object {
    $_.Name -ne "SHA256SUMS" -and $_.Name -ne $distName
}
$lines = foreach ($f in $releaseFiles) {
    $hash = (Get-FileHash $f.FullName -Algorithm SHA256).Hash.ToLower()
    "$hash  $($f.Name)"
}
[System.IO.File]::WriteAllLines((Join-Path (Get-Location) "SHA256SUMS"), [string[]]$lines, (New-Object System.Text.UTF8Encoding($false)))
Write-Host "`nSHA256SUMS:"
Get-Content "SHA256SUMS"
Pop-Location

# Clean staging directory (ZIP is self-contained)
Remove-Item -Recurse -Force $stageDir

Write-Host "`nRelease artifacts in $DistDir/:"
Get-ChildItem $DistDir -File | ForEach-Object { Write-Host "  $($_.Name)" }
