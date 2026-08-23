[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ReleaseDirectory,
    [string]$Repository = 'neko233-com/Terminal233',
    [switch]$Draft,
    [switch]$Prerelease,
    [switch]$Clobber
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$resolvedReleaseDirectory = (Resolve-Path -LiteralPath $ReleaseDirectory).Path
$manifestPath = Join-Path $resolvedReleaseDirectory 'terminal233-update.json'
$notesPath = Join-Path $resolvedReleaseDirectory 'RELEASE_NOTES.md'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'terminal233-update.json is missing.' }
if (-not (Test-Path -LiteralPath $notesPath -PathType Leaf)) { throw 'RELEASE_NOTES.md is missing.' }
if (Test-Path -LiteralPath (Join-Path $repositoryRoot '.github\workflows') -PathType Container) { throw 'GitHub Actions publishing is prohibited; remove workflows first.' }

$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ($manifest.product -ne 'Terminal233' -or $manifest.schemaVersion -ne 1) { throw 'The release manifest is not Terminal233 schemaVersion 1.' }
$tag = [string]$manifest.tag
if ($tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { throw "Invalid release tag: $tag" }
$assets = @(Get-ChildItem -LiteralPath $resolvedReleaseDirectory -File | Where-Object { $_.Name -notin @('terminal233-update.json', 'release-manifest.json', 'RELEASE_NOTES.md') })
if ($assets.Count -lt 6) { throw 'The release directory needs four binary packages, SHA256SUMS.txt, and release notes.' }
$gh = Get-Command gh -ErrorAction SilentlyContinue
if ($null -eq $gh) { throw 'GitHub CLI gh was not found.' }

$branch = (& git -C $repositoryRoot branch --show-current).Trim()
if ($branch -ne 'main') { throw "Publishing must run on main; current branch is $branch." }
if ((& git -C $repositoryRoot status --porcelain)) { throw 'The worktree must be clean before publishing.' }

$createArguments = @('release', 'create', $tag, '--repo', $Repository, '--title', "Terminal233 $tag", '--notes-file', $notesPath, '--target', 'main')
if ($Draft) { $createArguments += '--draft' }
if ($Prerelease) { $createArguments += '--prerelease' }
& $gh.Source @createArguments
if ($LASTEXITCODE -ne 0) { throw "GitHub release $tag creation failed." }

$uploadArguments = @('release', 'upload', $tag, '--repo', $Repository)
if ($Clobber) { $uploadArguments += '--clobber' }
foreach ($asset in $assets) { $uploadArguments += $asset.FullName }
& $gh.Source @uploadArguments
if ($LASTEXITCODE -ne 0) { throw "Uploading assets to GitHub release $tag failed." }
Write-Host "Terminal233 $tag published to $Repository."
