[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$installRoot = 'C:\App\nekossh'
$version = (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'mygo.json') -Raw | ConvertFrom-Json).version
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'Invalid application version.' }
$head = (& git -C $repo rev-parse HEAD).Trim()
$remote = (& git -C $repo rev-parse origin/main).Trim()
if ($head -ne $remote -or (& git -C $repo status --porcelain)) { throw 'Install requires a clean main matching origin/main.' }
if ((& git -C $repo branch --show-current).Trim() -ne 'main') { throw 'Install requires main.' }
$installedExe = Join-Path $installRoot 'Terminal233.exe'
$legacyRoot = 'C:\App\Terminal233'
$legacyExe = Join-Path $legacyRoot 'Terminal233.exe'
$running = Get-CimInstance Win32_Process -Filter "Name='Terminal233.exe'" | Where-Object { $_.ExecutablePath -and ([string]::Equals($_.ExecutablePath, $installedExe, [StringComparison]::OrdinalIgnoreCase) -or [string]::Equals($_.ExecutablePath, $legacyExe, [StringComparison]::OrdinalIgnoreCase)) }
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
$bundle = Join-Path $repo 'build\release-windows\windows-amd64'
$builtExe = Join-Path $bundle 'Terminal233.exe'
if ((& $builtExe --verify-broadcast | Out-String).Trim() -ne 'broadcast-input: available' -or $LASTEXITCODE -ne 0) { throw 'Packaged broadcast support verification failed.' }
$installer = Join-Path $bundle "terminal233-setup-$version-amd64.exe"
if (-not (Test-Path -LiteralPath $installer -PathType Leaf)) { throw 'Installer not found.' }
$process = Start-Process -FilePath $installer -ArgumentList @('/S', '/D=C:\App\nekossh') -WindowStyle Hidden -PassThru -Wait
if ($process.ExitCode -ne 0) { throw "Installer failed: $($process.ExitCode)" }
if ((& $installedExe --version | Out-String).Trim() -ne "Terminal233 $version") { throw 'Installed version verification failed.' }
if ((& $installedExe --verify-broadcast | Out-String).Trim() -ne 'broadcast-input: available' -or $LASTEXITCODE -ne 0) { throw 'Installed broadcast support verification failed.' }
foreach ($name in @('Terminal233.exe','ghostty-vt.dll')) {
    if ((Get-FileHash -LiteralPath (Join-Path $bundle $name)).Hash -ne (Get-FileHash -LiteralPath (Join-Path $installRoot $name)).Hash) { throw "Installed file mismatch: $name" }
}
$shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'Terminal233.lnk'
if (-not (Test-Path -LiteralPath $shortcut -PathType Leaf)) { throw 'Start Menu shortcut missing.' }
$shell = New-Object -ComObject WScript.Shell
$link = $shell.CreateShortcut($shortcut)
try { if (-not [string]::Equals($link.TargetPath, $installedExe, [StringComparison]::OrdinalIgnoreCase)) { throw 'Start Menu shortcut target mismatch.' } }
finally { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($link); [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) }
$key = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\com.neko233.terminal233'
if ($key.DisplayVersion -ne $version -or $key.InstallLocation -ne $installRoot) { throw 'Uninstall registration verification failed.' }
Set-Content -LiteralPath (Join-Path $installRoot 'UNSIGNED-BUILD.txt') -Value "Unsigned Windows build; source commit $head" -Encoding UTF8
# Remove only the known previous local validation installation, never arbitrary files.
if (Test-Path -LiteralPath $legacyRoot -PathType Container) {
    $legacyDir = Get-Item -LiteralPath $legacyRoot -Force
    $marker = Join-Path $legacyRoot 'LOCAL-VALIDATION-UNSIGNED.txt'
    $expected = @('ghostty-vt.dll','LOCAL-VALIDATION-UNSIGNED.txt','Terminal233.exe','Uninstall.exe')
    $items = @(Get-ChildItem -LiteralPath $legacyRoot -Force)
    $safe = -not ($legacyDir.Attributes -band [IO.FileAttributes]::ReparsePoint) -and (Test-Path -LiteralPath $marker -PathType Leaf) -and ((Get-Content -LiteralPath $marker -Raw) -match '^Local validation only; unsigned; source commit [0-9a-f]{40}\s*$') -and $items.Count -eq $expected.Count
    foreach ($item in $items) { if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $item.Name -notin $expected) { $safe = $false } }
    if ($safe) {
        foreach ($name in $expected) { Remove-Item -LiteralPath (Join-Path $legacyRoot $name) }
        Remove-Item -LiteralPath $legacyRoot
    } else { Write-Warning "Previous directory was preserved because it contains unrecognized data: $legacyRoot" }
}
Write-Output "Installed unsigned build: Terminal233 $version ($head)"
