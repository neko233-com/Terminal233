param([switch]$Quiet)
$ErrorActionPreference = 'Stop'
if ($Quiet) {
    [System.Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal'
    $env:GOMAXPROCS = '1'
    $env:GOFLAGS = '-p=1'
}
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Push-Location $repo
try { go run client/tools/mygooverlay/main.go go -C client test -p 1 -tags desktope2e -run '^TestDesktop(NativeWindow|CloudWorkflows|RemotePanes|SFTPOnly)$' -count 1 . }
finally { Pop-Location }
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
