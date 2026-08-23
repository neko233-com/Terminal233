[CmdletBinding()]
param(
    [string]$InstallRoot = 'C:\App\Terminal233'
)

$ErrorActionPreference = 'Stop'
$resolvedRoot = [System.IO.Path]::GetFullPath($InstallRoot)
$executable = Join-Path $resolvedRoot 'Terminal233.exe'
if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) { throw "Installed executable not found: $executable" }

$version = (& $executable --version | Out-String).Trim()
if ($version -notmatch '^Terminal233 \d+\.\d+\.\d+$') { throw "Unexpected installed version output: $version" }
$selfTest = (& $executable --self-test | Out-String).Trim() | ConvertFrom-Json
if ($selfTest.ok -ne $true -or $selfTest.runtime -ne 'pure-rust-puressh' -or $selfTest.persistence -ne 'memory_only') {
    throw 'Installed self-test did not report the expected pure-Rust memory-only runtime.'
}

$programs = [Environment]::GetFolderPath('Programs')
$shortcut = Join-Path $programs 'Terminal233\Terminal233.lnk'
if (-not (Test-Path -LiteralPath $shortcut -PathType Leaf)) { throw "Start Menu shortcut not found: $shortcut" }
$uninstallKey = Get-ItemProperty -LiteralPath 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Terminal233' -ErrorAction Stop
if ($uninstallKey.DisplayName -ne 'Terminal233' -or $uninstallKey.InstallLocation -ne $resolvedRoot -or $uninstallKey.DisplayVersion -notmatch '^\d+\.\d+\.\d+$') {
    throw 'Terminal233 uninstall registration is incomplete or points at the wrong directory.'
}

Write-Host "Terminal233 Windows install verification passed: $version"
Write-Host "Shortcut: $shortcut"
Write-Host "InstallRoot: $resolvedRoot"
