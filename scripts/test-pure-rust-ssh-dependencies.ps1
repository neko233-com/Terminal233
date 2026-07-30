[CmdletBinding()]
param(
    [string]$CargoExecutable = $env:NEKOSSH_RELEASE_CARGO
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$manifestPath = Join-Path $repositoryRoot 'src-tauri\Cargo.toml'
$resolvedCargo = if ([string]::IsNullOrWhiteSpace($CargoExecutable)) {
    (Get-Command cargo -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
} else {
    (Resolve-Path -LiteralPath $CargoExecutable).Path
}

$tree = & $resolvedCargo tree `
    --manifest-path $manifestPath `
    --locked `
    --target 'x86_64-pc-windows-msvc' `
    --edges 'normal,build' `
    --prefix none `
    --package 'puressh'
if ($LASTEXITCODE -ne 0) {
    throw '无法生成 puressh 依赖树。'
}

$packages = @(
    $tree |
        ForEach-Object {
            if ($_ -match '^(?<name>[A-Za-z0-9_.-]+) v[0-9]') {
                $Matches.name.ToLowerInvariant()
            }
        } |
        Where-Object { $_ } |
        Sort-Object -Unique
)

$forbidden = @(
    'ssh2',
    'libssh2-sys',
    'openssl',
    'openssl-sys',
    'openssl-src',
    'ring',
    'aws-lc-rs',
    'aws-lc-sys',
    'cc',
    'cmake',
    'bindgen'
)
$blocked = @($packages | Where-Object { $forbidden -contains $_ })
$nativeSys = @($packages | Where-Object { $_ -like '*-sys' })
if ($blocked.Count -gt 0 -or $nativeSys.Count -gt 0) {
    $found = @($blocked + $nativeSys | Sort-Object -Unique) -join ', '
    throw "Tauri SSH 依赖链不是纯 Rust，发现禁止依赖：$found"
}

$cargoToml = Get-Content -LiteralPath $manifestPath -Raw
if ($cargoToml -match '(?m)^\s*ssh2\s*=') {
    throw 'Tauri Cargo.toml 禁止重新引入 ssh2/libssh2。'
}
if ($cargoToml -notmatch 'puressh\s*=\s*\{[^}]*version\s*=\s*"=0\.1\.3"') {
    throw 'Tauri 必须精确锁定经过验证的 puressh 0.1.3。'
}
if ($cargoToml -match '(?m)^\s*ffi\s*=') {
    throw 'vendored puressh 禁止声明或保留 C ABI/ffi feature。'
}

$metadataJson = & $resolvedCargo metadata `
    --manifest-path $manifestPath `
    --locked `
    --format-version 1 `
    --filter-platform 'x86_64-pc-windows-msvc'
if ($LASTEXITCODE -ne 0) {
    throw '无法读取 Cargo metadata。'
}
$metadata = ($metadataJson -join [Environment]::NewLine) | ConvertFrom-Json
$puresshPackage = @(
    $metadata.packages |
        Where-Object { $_.name -eq 'puressh' -and $_.version -eq '0.1.3' }
)
if ($puresshPackage.Count -ne 1) {
    throw "必须且只能解析到一个 puressh 0.1.3，实际数量：$($puresshPackage.Count)"
}
$puresshNode = @($metadata.resolve.nodes | Where-Object { $_.id -eq $puresshPackage[0].id })
if ($puresshNode.Count -ne 1) {
    throw 'Cargo resolve 中缺少唯一的 puressh 节点。'
}
$actualFeatures = @($puresshNode[0].features | Sort-Object -Unique)
$expectedFeatures = @('alloc', 'client', 'multichannel', 'std')
if (($actualFeatures -join ',') -ne ($expectedFeatures -join ',')) {
    throw "puressh feature 集不符合纯 Rust门禁。期望：$($expectedFeatures -join ', ')；实际：$($actualFeatures -join ', ')"
}
Write-Output "Pure-Rust SSH dependency verification passed ($($packages.Count) packages; features: $($actualFeatures -join ', '))."
