param([switch]$Quiet)
$ErrorActionPreference = 'Stop'
if ($Quiet) {
    [System.Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal'
    $env:GOMAXPROCS = '1'
    $env:GOFLAGS = '-p=1'
}
go -C $PSScriptRoot test -p 1 -tags desktope2e -run '^TestDesktopNativeWindow$' -count 1 .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
