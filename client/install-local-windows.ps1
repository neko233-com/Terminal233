[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$installRoot = 'C:\App\Terminal233'
$head = (& git -C $repo rev-parse HEAD).Trim()
$remote = (& git -C $repo rev-parse origin/main).Trim()
if ($head -ne $remote -or (& git -C $repo status --porcelain)) { throw 'Install requires a clean main matching origin/main.' }
if ((& git -C $repo branch --show-current).Trim() -ne 'main') { throw 'Install requires main.' }
$installedExe = Join-Path $installRoot 'Terminal233.exe'
$running = Get-CimInstance Win32_Process -Filter "Name='Terminal233.exe'" | Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $installedExe, [StringComparison]::OrdinalIgnoreCase) }
if ($running) { throw 'Terminal233 is running; close it before installing.' }
[System.Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal'
$env:GOMAXPROCS = '1'
$env:GOFLAGS = '-p=1'
Push-Location $repo
try {
    & task test
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    & task test:desktop:windows
    if ($LASTEXITCODE -ne 0) { throw 'Desktop test failed.' }
    & task package:windows:local
    if ($LASTEXITCODE -ne 0) { throw 'Package build failed.' }
} finally { Pop-Location }
$bundle = Join-Path $repo 'build\unsigned-local\windows-amd64'
$installer = Join-Path $bundle 'terminal233-setup-0.2.0-amd64.exe'
if (-not (Test-Path -LiteralPath $installer -PathType Leaf)) { throw 'Installer not found.' }
$process = Start-Process -FilePath $installer -ArgumentList @('/S', '/D=C:\App\Terminal233') -WindowStyle Hidden -PassThru -Wait
if ($process.ExitCode -ne 0) { throw "Installer failed: $($process.ExitCode)" }
if ((& $installedExe --version | Out-String).Trim() -ne 'Terminal233 0.2.0') { throw 'Installed version verification failed.' }
foreach ($name in @('Terminal233.exe','ghostty-vt.dll')) {
    if ((Get-FileHash -LiteralPath (Join-Path $bundle $name)).Hash -ne (Get-FileHash -LiteralPath (Join-Path $installRoot $name)).Hash) { throw "Installed file mismatch: $name" }
}
$shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'Terminal233.lnk'
if (-not (Test-Path -LiteralPath $shortcut -PathType Leaf)) { throw 'Start Menu shortcut missing.' }
$key = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\com.neko233.terminal233'
if ($key.DisplayVersion -ne '0.2.0' -or $key.InstallLocation -ne $installRoot) { throw 'Uninstall registration verification failed.' }
Set-Content -LiteralPath (Join-Path $installRoot 'LOCAL-VALIDATION-UNSIGNED.txt') -Value "Local validation only; unsigned; source commit $head" -Encoding UTF8
Write-Output "Installed unsigned local validation build: Terminal233 0.2.0 ($head)"
