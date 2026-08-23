[CmdletBinding()]
param([switch]$NoBrowser)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$backendPortValue = if ([string]::IsNullOrWhiteSpace($env:TERMINAL233_BACKEND_PORT)) { '7331' } else { $env:TERMINAL233_BACKEND_PORT }
$frontendPortValue = if ([string]::IsNullOrWhiteSpace($env:TERMINAL233_BROWSER_FRONTEND_PORT)) { '1420' } else { $env:TERMINAL233_BROWSER_FRONTEND_PORT }
$backendPort = [int]$backendPortValue
$frontendPort = [int]$frontendPortValue
$env:TERMINAL233_BACKEND_PORT = "$backendPort"
$env:TERMINAL233_BROWSER_FRONTEND_PORT = "$frontendPort"
$env:NEKOSSH_BACKEND_PORT = "$backendPort"
$env:NEKOSSH_FRONTEND_PORT = "$frontendPort"

$server = $null
$vite = $null
try {
    $server = Start-Process -FilePath 'node' -ArgumentList 'server/ssh2js-server.mjs' -WorkingDirectory $projectRoot -PassThru -WindowStyle Hidden
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        try { Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$backendPort/healthz" -TimeoutSec 1 | Out-Null; break }
        catch {
            if ($server.HasExited) { throw 'Terminal233 browser SSH backend exited during startup.' }
            Start-Sleep -Milliseconds 250
        }
        if ($attempt -eq 39) { throw 'Terminal233 browser SSH backend did not become ready.' }
    }
    $viteEntry = Join-Path $projectRoot 'node_modules\vite\bin\vite.js'
    $vite = Start-Process -FilePath 'node' -ArgumentList @($viteEntry, '--mode', 'browser') -WorkingDirectory $projectRoot -PassThru
    $url = "http://127.0.0.1:$frontendPort/"
    Write-Host "Terminal233 browser IDE: $url"
    if (-not $NoBrowser) { Start-Process $url | Out-Null }
    Wait-Process -Id $vite.Id
} finally {
    if ($vite -and -not $vite.HasExited) { Stop-Process -Id $vite.Id -Force }
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force }
}
