[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$OutputPath,
    [Parameter(Mandatory)][string]$NpmCliPath,
    [string]$PowerShellPath = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName,
    [string]$GitPath = (Get-Command git.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source,
    [string]$GhPath = (Get-Command gh.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source,
    [string]$NodePath = (Get-Command node.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source,
    [Parameter(Mandatory)][string]$RustcPath,
    [Parameter(Mandatory)][string]$CargoPath,
    [string]$CargoAuditPath = (Get-Command cargo-audit.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source,
    [Parameter(Mandatory)][string]$PerlPath,
    [Parameter(Mandatory)][string]$NasmPath,
    [string]$VsWherePath = (Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'),
    [Parameter(Mandatory)][string]$SignToolPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$modulePath = Join-Path $PSScriptRoot 'release-toolchain-lock.psm1'
Import-Module -Name $modulePath -Force -ErrorAction Stop

$resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Assert-NekoSshToolchainLockOutsideRepository `
    -LockPath $resolvedOutput `
    -RepositoryRoot $repositoryRoot
if (Test-Path -LiteralPath $resolvedOutput) {
    throw "Refusing to overwrite an existing toolchain lock: $resolvedOutput"
}
$parent = Split-Path -Parent $resolvedOutput
if (-not (Test-Path -LiteralPath $parent -PathType Container)) {
    throw "Toolchain lock parent directory does not exist: $parent"
}
$toolPaths = [ordered]@{
    powershell = $PowerShellPath
    git = $GitPath
    gh = $GhPath
    node = $NodePath
    npm = $NpmCliPath
    rustc = $RustcPath
    cargo = $CargoPath
    cargoAudit = $CargoAuditPath
    perl = $PerlPath
    nasm = $NasmPath
    vswhere = $VsWherePath
    signtool = $SignToolPath
}
$document = New-NekoSshReleaseToolchainLockDocument -ToolPaths $toolPaths
$json = $document | ConvertTo-Json -Depth 8
$utf8 = [Text.UTF8Encoding]::new($false, $true)
[IO.File]::WriteAllText($resolvedOutput, "$json`n", $utf8)
$file = Get-Item -LiteralPath $resolvedOutput -Force
$sha256 = (Get-FileHash -LiteralPath $resolvedOutput -Algorithm SHA256).Hash.ToLowerInvariant()
[pscustomobject]@{
    path = $resolvedOutput
    bytes = [long]$file.Length
    expectedSha256 = $sha256
}
