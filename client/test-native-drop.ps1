param([switch]$Quiet)
$ErrorActionPreference = 'Stop'
if ($Quiet) {
    [Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal'
    $env:GOMAXPROCS = '1'
    $env:GOFLAGS = '-p=1'
}
$env:GOWORK = 'off'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
# The MyGo Windows OLE test hook is internal to its module. A disposable
# module nested under the same import namespace can exercise the unmodified
# client source and the real native drop target without exposing test hooks
# or changing production dependencies.
$module = Join-Path $repo ('build\native-drop-test\' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $module -Force | Out-Null
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
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& go -C $module test -p 1 -run '^TestNativeOLEBatchDrop$' -count 1 .
exit $LASTEXITCODE
