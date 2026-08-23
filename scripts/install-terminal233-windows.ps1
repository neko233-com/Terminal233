[CmdletBinding()]
param(
    [string]$PackageRoot = '',
    [string]$ExecutablePath = '',
    [string]$FrontendPath = '',
    [string]$InstallRoot = ''
)

$ErrorActionPreference = 'Stop'
$scriptRoot = (Resolve-Path $PSScriptRoot).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$sourceRoot = if (-not [string]::IsNullOrWhiteSpace($PackageRoot)) {
    (Resolve-Path -LiteralPath $PackageRoot).Path
} elseif (Test-Path -LiteralPath (Join-Path $scriptRoot 'Terminal233.exe') -PathType Leaf) {
    $scriptRoot
} else {
    $repositoryRoot
}
$resolvedExecutable = if ([string]::IsNullOrWhiteSpace($ExecutablePath)) {
    $packageExecutable = Join-Path $sourceRoot 'Terminal233.exe'
    if (Test-Path -LiteralPath $packageExecutable -PathType Leaf) {
        (Resolve-Path -LiteralPath $packageExecutable).Path
    } else {
        (Resolve-Path -LiteralPath (Join-Path $sourceRoot 'terminal233\target\release\terminal233.exe')).Path
    }
} else {
    (Resolve-Path -LiteralPath $ExecutablePath).Path
}
$resolvedFrontend = if ([string]::IsNullOrWhiteSpace($FrontendPath)) {
    (Resolve-Path -LiteralPath (Join-Path $sourceRoot 'dist')).Path
} else {
    (Resolve-Path -LiteralPath $FrontendPath).Path
}
$resolvedInstallRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    'C:\App\Terminal233'
} else {
    [IO.Path]::GetFullPath($InstallRoot)
}

if (-not (Test-Path -LiteralPath $resolvedExecutable -PathType Leaf)) {
    throw "Client executable was not found: $resolvedExecutable"
}
if (-not (Test-Path -LiteralPath $resolvedFrontend -PathType Container)) {
    throw "Frontend build directory was not found: $resolvedFrontend"
}
if ([string]::IsNullOrWhiteSpace($resolvedInstallRoot) -or $resolvedInstallRoot -eq [IO.Path]::GetPathRoot($resolvedInstallRoot)) {
    throw "Refusing to use a drive root as the install directory: $resolvedInstallRoot"
}

$installedFrontend = Join-Path $resolvedInstallRoot 'dist'
New-Item -ItemType Directory -Force -Path $resolvedInstallRoot, $installedFrontend | Out-Null
$installedExecutable = Join-Path $resolvedInstallRoot 'Terminal233.exe'
Copy-Item -LiteralPath $resolvedExecutable -Destination $installedExecutable -Force
Get-ChildItem -LiteralPath $resolvedFrontend -Force | Copy-Item -Destination $installedFrontend -Recurse -Force

$versionOutput = (& $installedExecutable --version | Out-String).Trim()
if ($versionOutput -notmatch '^Terminal233 (?<version>\d+\.\d+\.\d+)$') {
    throw "Invalid installed client version output: $versionOutput"
}
$installedVersion = $Matches.version
Set-Content -LiteralPath (Join-Path $resolvedInstallRoot 'Terminal233.version.txt') -Value $versionOutput -Encoding UTF8

$uninstallerSource = if (Test-Path -LiteralPath (Join-Path $scriptRoot 'uninstall-terminal233-windows.ps1') -PathType Leaf) {
    Join-Path $scriptRoot 'uninstall-terminal233-windows.ps1'
} else {
    Join-Path $repositoryRoot 'scripts\uninstall-terminal233-windows.ps1'
}
if (-not (Test-Path -LiteralPath $uninstallerSource -PathType Leaf)) {
    throw "Windows uninstaller script was not found: $uninstallerSource"
}
$uninstallerPath = Join-Path $resolvedInstallRoot 'uninstall-terminal233-windows.ps1'
Copy-Item -LiteralPath $uninstallerSource -Destination $uninstallerPath -Force

$programsFolder = [Environment]::GetFolderPath('Programs')
if ([string]::IsNullOrWhiteSpace($programsFolder)) {
    throw 'Windows Start Menu Programs folder could not be resolved.'
}
$startMenuFolder = Join-Path $programsFolder 'Terminal233'
New-Item -ItemType Directory -Force -Path $startMenuFolder | Out-Null
$shortcutPath = Join-Path $startMenuFolder 'Terminal233.lnk'
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $installedExecutable
$shortcut.WorkingDirectory = $resolvedInstallRoot
$shortcut.IconLocation = "$installedExecutable,0"
$shortcut.Description = 'Terminal233 · Rust SSH IDE'
$shortcut.Save()
[Runtime.InteropServices.Marshal]::FinalReleaseComObject($shortcut) | Out-Null
[Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) | Out-Null

$uninstallKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Terminal233'
New-Item -Path $uninstallKey -Force | Out-Null
$uninstallCommand = "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$uninstallerPath`""
New-ItemProperty -Path $uninstallKey -Name 'DisplayName' -Value 'Terminal233' -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'DisplayVersion' -Value $installedVersion -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'Publisher' -Value 'Terminal233 contributors' -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'InstallLocation' -Value $resolvedInstallRoot -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'DisplayIcon' -Value $installedExecutable -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'UninstallString' -Value $uninstallCommand -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'QuietUninstallString' -Value $uninstallCommand -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'NoModify' -Value 1 -PropertyType DWord -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name 'NoRepair' -Value 1 -PropertyType DWord -Force | Out-Null

Write-Host "Terminal233 was silently installed to $resolvedInstallRoot"
Write-Host "Installed version: $versionOutput"
Write-Host "Start Menu shortcut: $shortcutPath"
