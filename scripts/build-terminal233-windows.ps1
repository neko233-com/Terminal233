[CmdletBinding()]
param(
    [ValidateSet('x86_64-pc-windows-msvc', 'aarch64-pc-windows-msvc')]
    [string]$Target = 'x86_64-pc-windows-msvc'
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$manifestPath = Join-Path $repositoryRoot 'terminal233\Cargo.toml'

if ($Target -eq 'x86_64-pc-windows-msvc') {
    & cargo build --manifest-path $manifestPath --release --locked
    if ($LASTEXITCODE -ne 0) {
        throw "x86_64 Cargo build failed with exit code $LASTEXITCODE."
    }
    exit 0
}

$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
if (-not (Test-Path -LiteralPath $vswhere -PathType Leaf)) {
    throw 'Visual Studio vswhere.exe was not found; install Visual C++ Build Tools.'
}
$installationPath = (& $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.ARM64 -property installationPath | Out-String).Trim()
if ([string]::IsNullOrWhiteSpace($installationPath)) {
    throw 'Visual Studio ARM64 C++ tools are not installed: Microsoft.VisualStudio.Component.VC.Tools.ARM64'
}
$vsDevCmd = Join-Path $installationPath 'Common7\Tools\VsDevCmd.bat'
if (-not (Test-Path -LiteralPath $vsDevCmd -PathType Leaf)) {
    throw "VsDevCmd.bat was not found: $vsDevCmd"
}

$command = 'call "' + $vsDevCmd + '" -arch=arm64 -host_arch=x64 && cargo build --manifest-path "' + $manifestPath + '" --target aarch64-pc-windows-msvc --release --locked'
& cmd.exe /d /c $command
if ($LASTEXITCODE -ne 0) {
    throw "ARM64 Cargo build failed with exit code $LASTEXITCODE."
}
