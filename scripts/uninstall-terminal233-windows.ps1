[CmdletBinding()]
param(
    [string]$InstallRoot = $PSScriptRoot
)

$ErrorActionPreference = 'Stop'
$resolvedInstallRoot = (Resolve-Path -LiteralPath $InstallRoot).Path
$installedExecutable = Join-Path $resolvedInstallRoot 'Terminal233.exe'
$startMenuShortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'Terminal233\Terminal233.lnk'
$startMenuFolder = Split-Path -Parent $startMenuShortcut
$uninstallKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Terminal233'

if (Test-Path -LiteralPath $startMenuShortcut -PathType Leaf) {
    Remove-Item -LiteralPath $startMenuShortcut -Force
}
if (Test-Path -LiteralPath $startMenuFolder -PathType Container) {
    $remaining = @(Get-ChildItem -LiteralPath $startMenuFolder -Force)
    if ($remaining.Count -eq 0) {
        Remove-Item -LiteralPath $startMenuFolder -Force
    }
}
if (Test-Path -LiteralPath $uninstallKey) {
    Remove-Item -LiteralPath $uninstallKey -Recurse -Force
}

if (Test-Path -LiteralPath $installedExecutable -PathType Leaf) {
    Remove-Item -LiteralPath $resolvedInstallRoot -Recurse -Force
}

Write-Host "Terminal233 was uninstalled from $resolvedInstallRoot"
