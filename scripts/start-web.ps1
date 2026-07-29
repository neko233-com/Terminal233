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
  throw "本地 MCP/API 端口 $backendPort 已被占用。请先释放该端口。"
}

while ($frontendPort -lt 5200 -and (Test-LocalPortInUse -Port $frontendPort)) {
  $frontendPort += 1
}
if ($frontendPort -ge 5200) {
  throw '5173-5199 均被占用，无法启动 Web macOS。'
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
  if (-not $backendReady) { throw '本地 ssh2js 后端启动失败。' }

  $viteEntry = Join-Path $projectRoot 'node_modules\vite\bin\vite.js'
  if (-not (Test-Path -LiteralPath $viteEntry)) { throw '缺少前端依赖，请先运行 npm install。' }
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
  if (-not $frontendReady) { throw 'Web macOS 前端启动失败。' }

  if (-not $NoBrowser) { Start-Process $webUrl }
  Write-Host "NekoSSH Web: $webUrl"
  Write-Host "NekoSSH MCP: $backendUrl/mcp"
  Write-Host "Authorization: Bearer $mcpToken"
  Write-Host "后端 PID $($server.Id)，前端 PID $($web.Id)。保持此终端打开；Ctrl+C 停止。"

  while ($true) {
    $server.Refresh()
    $web.Refresh()
    if ($server.HasExited) { throw '本地 ssh2js 后端意外退出。' }
    if ($web.HasExited) { throw 'Web macOS 前端意外退出。' }
    Start-Sleep -Seconds 1
  }
} finally {
  if ($web -and -not $web.HasExited) { Stop-Process -Id $web.Id -Force -ErrorAction SilentlyContinue }
  if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
}
