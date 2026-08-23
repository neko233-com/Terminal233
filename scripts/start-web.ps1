[CmdletBinding()]
param([switch]$NoBrowser)

$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$backendPort = 7331
$frontendPort = 5173

function Test-LocalPortInUse {
  param([int]$Port)
  return $null -ne (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
}

if (Test-LocalPortInUse -Port $backendPort) {
  throw "Local MCP/API port $backendPort is already in use."
}

while ($frontendPort -lt 5200 -and (Test-LocalPortInUse -Port $frontendPort)) {
  $frontendPort += 1
}
if ($frontendPort -ge 5200) {
  throw 'Ports 5173-5199 are all in use; Web macOS cannot start.'
}

$tokenBytes = New-Object byte[] 32
$tokenGenerator = [System.Security.Cryptography.RandomNumberGenerator]::Create()
try {
  $tokenGenerator.GetBytes($tokenBytes)
} finally {
  $tokenGenerator.Dispose()
}
$mcpToken = [Convert]::ToBase64String($tokenBytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
$webUrl = "http://127.0.0.1:$frontendPort/"
$backendUrl = "http://127.0.0.1:$backendPort"

# The server inherits this one-process capability.  It is neither saved to a Vault nor written to
# this project.  Keep this console open if you need to copy the MCP configuration below.
$env:NEKOSSH_MCP_TOKEN = $mcpToken
$env:NEKOSSH_BACKEND_PORT = "$backendPort"
$env:NEKOSSH_FRONTEND_PORT = "$frontendPort"
$server = $null
$web = $null

try {
  $server = Start-Process -FilePath 'node' -ArgumentList 'server/ssh2js-server.mjs' -WorkingDirectory $projectRoot -PassThru -WindowStyle Hidden
  $backendReady = $false
  for ($attempt = 0; $attempt -lt 40; $attempt++) {
    try {
      Invoke-WebRequest -UseBasicParsing "$backendUrl/healthz" -TimeoutSec 1 | Out-Null
      $backendReady = $true
      break
    } catch {
      if ($server.HasExited) { break }
      Start-Sleep -Milliseconds 250
    }
  }
  if (-not $backendReady) { throw 'The local ssh2js backend did not start.' }

  $viteEntry = Join-Path $projectRoot 'node_modules\vite\bin\vite.js'
  if (-not (Test-Path -LiteralPath $viteEntry)) { throw 'Vite is missing; run npm install first.' }
  $web = Start-Process -FilePath 'node' -ArgumentList $viteEntry, '--mode', 'web' -WorkingDirectory $projectRoot -PassThru -WindowStyle Hidden
  $frontendReady = $false
  for ($attempt = 0; $attempt -lt 80; $attempt++) {
    try {
      Invoke-WebRequest -UseBasicParsing $webUrl -TimeoutSec 1 | Out-Null
      $frontendReady = $true
      break
    } catch {
      if ($web.HasExited) { break }
      Start-Sleep -Milliseconds 250
    }
  }
  if (-not $frontendReady) { throw 'The Web macOS frontend did not start.' }

  if (-not $NoBrowser) { Start-Process $webUrl }
  Write-Host "Terminal233 Web: $webUrl"
  Write-Host "Terminal233 MCP: $backendUrl/mcp"
  Write-Host "Authorization: Bearer $mcpToken"
  Write-Host "Backend PID $($server.Id), frontend PID $($web.Id). Keep this terminal open; press Ctrl+C to stop."

  while ($true) {
    $server.Refresh()
    $web.Refresh()
    if ($server.HasExited) { throw 'The local ssh2js backend exited unexpectedly.' }
    if ($web.HasExited) { throw 'The Web macOS frontend exited unexpectedly.' }
    Start-Sleep -Seconds 1
  }
} finally {
  if ($web -and -not $web.HasExited) { Stop-Process -Id $web.Id -Force -ErrorAction SilentlyContinue }
  if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
}
