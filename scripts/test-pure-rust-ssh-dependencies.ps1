[CmdletBinding()]
param(
    [string]$CargoExecutable = $env:TERMINAL233_RELEASE_CARGO
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$manifestPath = Join-Path $repositoryRoot 'terminal233\Cargo.toml'
$resolvedCargo = if ([string]::IsNullOrWhiteSpace($CargoExecutable)) {
    (Get-Command cargo -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
} else {
    (Resolve-Path -LiteralPath $CargoExecutable).Path
}

$tree = & $resolvedCargo tree --manifest-path $manifestPath --locked --target 'x86_64-pc-windows-msvc' --edges 'normal,build' --prefix none --package 'puressh'
if ($LASTEXITCODE -ne 0) {
    throw 'Unable to generate the puressh dependency tree.'
}

$packages = @(
    $tree |
        ForEach-Object {
            if ($_ -match '^([A-Za-z0-9_.-]+) v[0-9]') {
                $Matches[1].ToLowerInvariant()
            }
        } |
        Where-Object { $_ } |
        Sort-Object -Unique
)

$forbidden = @('ssh2', 'libssh2-sys', 'openssl', 'openssl-sys', 'openssl-src', 'ring', 'aws-lc-rs', 'aws-lc-sys', 'cc', 'cmake', 'bindgen')
$blocked = @($packages | Where-Object { $forbidden -contains $_ })
$nativeSys = @($packages | Where-Object { $_ -like '*-sys' })
if ($blocked.Count -gt 0 -or $nativeSys.Count -gt 0) {
    $found = @($blocked + $nativeSys | Sort-Object -Unique) -join ', '
    throw "Terminal233 SSH dependency chain is not pure Rust: $found"
}

$cargoToml = Get-Content -LiteralPath $manifestPath -Raw
if ($cargoToml -match '(?m)^\s*ssh2\s*=') {
    throw 'Terminal233 Cargo.toml must not reintroduce ssh2 or libssh2.'
}
if ($cargoToml -notmatch 'puressh\s*=\s*\{[^}]*version\s*=\s*"=0\.1\.3"') {
    throw 'Terminal233 must pin puressh to version 0.1.3.'
}
if ($cargoToml -match '(?m)^\s*ffi\s*=') {
    throw 'Vendored puressh must not declare an ffi feature.'
}

$metadataJson = & $resolvedCargo metadata --manifest-path $manifestPath --locked --format-version 1 --filter-platform 'x86_64-pc-windows-msvc'
if ($LASTEXITCODE -ne 0) {
    throw 'Unable to read Cargo metadata.'
}
$metadata = ($metadataJson -join [Environment]::NewLine) | ConvertFrom-Json
$puresshPackage = @($metadata.packages | Where-Object { $_.name -eq 'puressh' -and $_.version -eq '0.1.3' })
if ($puresshPackage.Count -ne 1) {
    throw "Expected exactly one puressh 0.1.3 package, found $($puresshPackage.Count)."
}
$puresshNode = @($metadata.resolve.nodes | Where-Object { $_.id -eq $puresshPackage[0].id })
if ($puresshNode.Count -ne 1) {
    throw 'Cargo resolve is missing the unique puressh node.'
}
$actualFeatures = @($puresshNode[0].features | Sort-Object -Unique)
$expectedFeatures = @('alloc', 'client', 'multichannel', 'std')
if (($actualFeatures -join ',') -ne ($expectedFeatures -join ',')) {
    throw "puressh feature set does not match policy. Expected: $($expectedFeatures -join ', '); actual: $($actualFeatures -join ', ')"
}
Write-Output "Pure-Rust SSH dependency verification passed ($($packages.Count) packages; features: $($actualFeatures -join ', '))."
