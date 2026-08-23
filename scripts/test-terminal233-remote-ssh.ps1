[CmdletBinding()]
param(
    [string]$RemoteHost = '43.119.1.108',
    [ValidateRange(1, 65535)][int]$Port = 22,
    [string]$Username = 'root'
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if ([string]::IsNullOrWhiteSpace($env:TERMINAL233_REMOTE_SSH_PASSWORD)) {
    throw 'Set TERMINAL233_REMOTE_SSH_PASSWORD in the current process before running the remote acceptance test.'
}

$env:TERMINAL233_REMOTE_SSH_INTEROP = '1'
$env:TERMINAL233_REMOTE_SSH_HOST = $RemoteHost
$env:TERMINAL233_REMOTE_SSH_PORT = [string]$Port
$env:TERMINAL233_REMOTE_SSH_USERNAME = $Username

try {
    Push-Location $repositoryRoot
    $previousErrorAction = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $testOutput = @(
            & cargo test --manifest-path terminal233/Cargo.toml --locked --all-features ssh::tests::native_remote_ssh_interoperability -- --exact --nocapture 2>&1
        )
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    $exitCode = $LASTEXITCODE
    $testOutput | Write-Host
    if ($exitCode -ne 0) {
        throw "Remote SSH acceptance failed with exit code $exitCode."
    }
    if (($testOutput -join "`n") -notmatch 'test ssh::tests::native_remote_ssh_interoperability \.\.\. ok') {
        throw 'Remote SSH acceptance did not execute the exact native test.'
    }
} finally {
    Pop-Location
}

Write-Host 'Terminal233 remote SSH acceptance passed without persisting credentials.'
