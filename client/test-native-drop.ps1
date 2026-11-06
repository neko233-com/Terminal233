param([switch]$Quiet)
$ErrorActionPreference = 'Stop'
if ($Quiet) {
    [Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal'
    $env:GOMAXPROCS = '1'
    $env:GOFLAGS = '-p=1'
}
$env:GOWORK = 'off'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
# MyGo's native OLE test hook is internal. A disposable nested module tests
# the unchanged client source through the native drop target without exposing
# production hooks or changing the pinned dependency.
$testRoot = [IO.Path]::GetFullPath((Join-Path $repo 'build\native-drop-test'))
$module = [IO.Path]::GetFullPath((Join-Path $testRoot ([guid]::NewGuid().ToString('N'))))
if (!$module.StartsWith($testRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Test module path is outside the intended build directory.'
}
New-Item -ItemType Directory -Path $module -Force | Out-Null
try {
    Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.go' -File |
        Where-Object { $_.Name -notlike '*_test.go' } |
        Copy-Item -Destination $module
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'go.mod'),(Join-Path $PSScriptRoot 'go.sum') -Destination $module
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'testdata\native-drop\native_drop_test.go') -Destination $module
    $modPath = Join-Path $module 'go.mod'
    $mod = Get-Content -LiteralPath $modPath -Raw
    $mod = $mod.Replace('module github.com/neko233-com/nekossh/client', 'module github.com/egoist/mygo/nekossh_native_test')
    Set-Content -LiteralPath $modPath -Value $mod -Encoding utf8NoBOM
    $mygoDir = (& go -C $PSScriptRoot list -m -f '{{.Dir}}' github.com/egoist/mygo | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or !$mygoDir) { throw 'Could not locate the pinned MyGo module.' }
    $mygoDir = $mygoDir.Replace('\','/')
    & go -C $module mod edit "-replace=github.com/egoist/mygo=$mygoDir"
    if ($LASTEXITCODE -ne 0) { throw 'Could not configure the native test module.' }
    & go -C $module test -p 1 -run '^TestNativeOLE(BatchDrop|PaneDrop)$' -count 1 .
    if ($LASTEXITCODE -ne 0) { throw 'Native OLE batch drop test failed.' }
} finally {
    if (Test-Path -LiteralPath $module) { Remove-Item -LiteralPath $module -Recurse -Force }
}
