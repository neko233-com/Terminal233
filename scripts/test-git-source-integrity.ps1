[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$modulePath = Join-Path $PSScriptRoot 'git-source-integrity.psm1'
Import-Module -Name $modulePath -Force -ErrorAction Stop
$moduleSource = [IO.File]::ReadAllText($modulePath)
if ($moduleSource -match "(?m)-ArgumentList\s+@\('status'") {
    throw 'Source integrity must not invoke git status because local attributes may execute filters.'
}
if (-not $moduleSource.Contains("`$EnvironmentVariables['GIT_NO_LAZY_FETCH'] = '1'")) {
    throw 'Source integrity must disable partial-clone lazy fetches.'
}
$safeDirectoryBindings = [regex]::Matches(
    $moduleSource,
    [regex]::Escape('"safe.directory=$RepositoryRoot"')
)
if ($safeDirectoryBindings.Count -ne 3) {
    throw 'Every source-integrity Git process must bind safe.directory to the exact repository root.'
}

$gitExecutable = (Get-Command git.exe -CommandType Application -ErrorAction Stop |
    Select-Object -First 1).Source
$temporaryParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$testRoot = Join-Path $temporaryParent (
    "nekossh-source-integrity-test-$PID-$([guid]::NewGuid().ToString('N'))"
)
$repository = Join-Path $testRoot 'worktree'
$origin = Join-Path $testRoot 'origin.git'
$snapshotRoot = Join-Path $testRoot 'snapshot'
$utf8 = [Text.UTF8Encoding]::new($false)

function Invoke-TestGit {
    param(
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string[]]$ArgumentList
    )

    $previousErrorPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $gitExecutable -C $WorkingDirectory @ArgumentList 2>&1
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorPreference
    }
    if ($exitCode -ne 0) {
        throw "Test Git command failed: git $($ArgumentList -join ' ')`n$($output -join "`n")"
    }
    return ($output -join "`n").Trim()
}

function Assert-Rejected {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$MessagePattern,
        [Parameter(Mandatory)][string]$FailureMessage
    )

    $rejected = $false
    try {
        & $Action
    }
    catch {
        $rejected = $_.Exception.Message -like $MessagePattern
    }
    if (-not $rejected) {
        throw $FailureMessage
    }
}

function Restore-TestWorktree {
    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('restore', '--source=HEAD', '--staged', '--worktree', '--', 'file.txt') |
        Out-Null
}

function Remove-TestRoot {
    if (-not (Test-Path -LiteralPath $testRoot)) {
        return
    }
    $resolved = [IO.Path]::GetFullPath($testRoot)
    $parent = $temporaryParent.TrimEnd('\', '/')
    if (-not $resolved.StartsWith("$parent$([IO.Path]::DirectorySeparatorChar)", [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove test data outside the temporary directory: $resolved"
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}

[void][IO.Directory]::CreateDirectory($testRoot)
try {
    [void][IO.Directory]::CreateDirectory($origin)
    [void][IO.Directory]::CreateDirectory($repository)
    Invoke-TestGit -WorkingDirectory $origin -ArgumentList @('init', '--bare') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('init', '-b', 'main') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('config', 'user.name', 'NekoSSH security test') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('config', 'user.email', 'security-test@invalid.local') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('config', 'core.autocrlf', 'false') | Out-Null
    [IO.File]::WriteAllText(
        (Join-Path $repository '.gitattributes'),
        "*.txt text eol=lf filter=nekossh-security-test`n",
        $utf8
    )
    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "committed-one`n", $utf8)
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('add', '--all') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('commit', '-m', 'initial') | Out-Null
    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "committed-two`n", $utf8)
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('add', '--all') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('commit', '-m', 'second') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('remote', 'add', 'origin', $origin) | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('push', '-u', 'origin', 'main') | Out-Null

    $identity = Assert-NekoSshGitSourceIntegrity `
        -RepositoryRoot $repository `
        -GitExecutable $gitExecutable
    if (
        $identity.Branch -cne 'main' -or
        $identity.Commit -cnotmatch '^[0-9a-f]{40}$' -or
        $identity.Tree -cnotmatch '^[0-9a-f]{40}$' -or
        $identity.SnapshotSha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $identity.DigestFormat -cne 'git-raw-blobs-sha256-v1' -or
        $identity.TrackedFileCount -ne 2
    ) {
        throw 'Clean source identity is incomplete or noncanonical.'
    }

    $injectedEnvironment = @(
        'GIT_CONFIG_COUNT',
        'GIT_CONFIG_KEY_0',
        'GIT_CONFIG_VALUE_0',
        'GIT_CONFIG_KEY_1',
        'GIT_CONFIG_VALUE_1'
    )
    $savedEnvironment = @{}
    foreach ($name in $injectedEnvironment) {
        $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable(
            $name,
            [EnvironmentVariableTarget]::Process
        )
    }
    try {
        $env:GIT_CONFIG_COUNT = '2'
        $env:GIT_CONFIG_KEY_0 = 'filter.nekossh-security-test.clean'
        $env:GIT_CONFIG_VALUE_0 = 'exit 42'
        $env:GIT_CONFIG_KEY_1 = 'filter.nekossh-security-test.required'
        $env:GIT_CONFIG_VALUE_1 = 'true'
        [IO.File]::WriteAllText(
            (Join-Path $repository 'file.txt'),
            "injected-filter-probe`n",
            $utf8
        )
        $injectionWasActive = $false
        try {
            Invoke-TestGit `
                -WorkingDirectory $repository `
                -ArgumentList @('hash-object', '--', 'file.txt') |
                Out-Null
        }
        catch {
            $injectionWasActive = $true
        }
        if (-not $injectionWasActive) {
            throw 'GIT_CONFIG_COUNT negative-test injection did not affect an unguarded Git process.'
        }
        [IO.File]::WriteAllText(
            (Join-Path $repository 'file.txt'),
            "committed-two`n",
            $utf8
        )
        $injectionIdentity = Assert-NekoSshGitSourceIntegrity `
            -RepositoryRoot $repository `
            -GitExecutable $gitExecutable
        if ($injectionIdentity.Commit -cne $identity.Commit) {
            throw 'Cleared GIT_CONFIG_COUNT injection changed the source identity.'
        }
    }
    finally {
        foreach ($name in $injectedEnvironment) {
            if ($null -eq $savedEnvironment[$name]) {
                Remove-Item "Env:$name" -ErrorAction SilentlyContinue
            } else {
                Set-Item "Env:$name" -Value $savedEnvironment[$name]
            }
        }
    }

    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('update-index', '--skip-worktree', 'file.txt') | Out-Null
    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "hidden-skip-worktree`n", $utf8)
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*forbidden Git index state*' `
        -FailureMessage 'Source gate did not reject skip-worktree.'
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('update-index', '--no-skip-worktree', 'file.txt') | Out-Null
    Restore-TestWorktree

    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('update-index', '--assume-unchanged', 'file.txt') | Out-Null
    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "hidden-assume-unchanged`n", $utf8)
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*forbidden Git index state*' `
        -FailureMessage 'Source gate did not reject assume-unchanged.'
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('update-index', '--no-assume-unchanged', 'file.txt') | Out-Null
    Restore-TestWorktree

    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "staged-change`n", $utf8)
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('add', 'file.txt') | Out-Null
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*index tree*does not exactly match*' `
        -FailureMessage 'Source gate did not reject a staged index change.'
    Restore-TestWorktree

    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "unstaged-change`n", $utf8)
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*Git worktree tracked file bytes*do not exactly match*' `
        -FailureMessage 'Source gate did not reject an unstaged worktree change.'
    Restore-TestWorktree

    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "committed-two`r`n", $utf8)
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*Git worktree tracked file bytes*do not exactly match*' `
        -FailureMessage 'Source gate allowed CRLF bytes hidden by text normalization.'
    Restore-TestWorktree

    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @(
            'config',
            'filter.nekossh-security-test.clean',
            'git show HEAD:file.txt'
        ) |
        Out-Null
    [IO.File]::WriteAllText(
        (Join-Path $repository 'file.txt'),
        "malicious-filter-hidden-by-clean`n",
        $utf8
    )
    $filteredObject = Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('hash-object', '--path=file.txt', '--', 'file.txt')
    $committedFileObject = Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('rev-parse', 'HEAD:file.txt')
    if ($filteredObject -cne $committedFileObject) {
        throw 'Malicious clean-filter fixture did not map modified bytes to the committed blob.'
    }
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*Git worktree tracked file bytes*do not exactly match*' `
        -FailureMessage 'Source gate allowed bytes hidden by a malicious clean filter.'
    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('config', '--unset', 'filter.nekossh-security-test.clean') |
        Out-Null
    Restore-TestWorktree

    $untrackedPath = Join-Path $repository 'untracked.txt'
    [IO.File]::WriteAllText($untrackedPath, "untracked`n", $utf8)
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*clean Git worktree*' `
        -FailureMessage 'Source gate did not reject an untracked file.'
    Remove-Item -LiteralPath $untrackedPath -Force

    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('switch', '-c', 'feature') | Out-Null
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*require refs/heads/main*' `
        -FailureMessage 'Source gate did not reject a non-main branch.'
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('switch', 'main') | Out-Null
    Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('branch', '-D', 'feature') | Out-Null

    $headCommit = Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('rev-parse', 'HEAD')
    $parentCommit = Invoke-TestGit -WorkingDirectory $repository -ArgumentList @('rev-parse', 'HEAD^')
    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('update-ref', 'refs/remotes/origin/main', $parentCommit) |
        Out-Null
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*must exactly match origin/main*' `
        -FailureMessage 'Source gate did not reject an origin/main mismatch.'
    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('update-ref', 'refs/remotes/origin/main', $headCommit) |
        Out-Null

    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @(
            'config',
            'filter.nekossh-security-test.clean',
            'cat'
        ) |
        Out-Null
    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @(
            'config',
            'filter.nekossh-security-test.smudge',
            'git show HEAD^:file.txt'
        ) |
        Out-Null
    $snapshot = New-NekoSshGitSourceSnapshot `
        -RepositoryRoot $repository `
        -DestinationRoot $snapshotRoot `
        -TrustedParent $testRoot `
        -Identity $identity `
        -GitExecutable $gitExecutable `
        -ReadOnlyTrackedFiles
    if ($snapshot.PSObject.Properties.Name -cnotcontains 'Path') {
        $snapshotTypes = @($snapshot | ForEach-Object { $_.GetType().FullName }) -join ', '
        throw "Snapshot creation emitted unexpected output objects: $snapshotTypes"
    }
    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('config', '--unset', 'filter.nekossh-security-test.smudge') |
        Out-Null
    Invoke-TestGit `
        -WorkingDirectory $repository `
        -ArgumentList @('config', '--unset', 'filter.nekossh-security-test.clean') |
        Out-Null
    if (Test-Path -LiteralPath (Join-Path $snapshotRoot '.git')) {
        throw 'Exported source snapshot must not contain Git administrative data.'
    }
    if ([IO.File]::ReadAllText((Join-Path $snapshotRoot 'file.txt')) -cne "committed-two`n") {
        throw 'Exported source snapshot does not contain committed HEAD bytes.'
    }
    [void](Assert-NekoSshGitSourceSnapshotIntegrity `
        -RepositoryRoot $repository `
        -Snapshot $snapshot `
        -GitExecutable $gitExecutable)

    [IO.File]::WriteAllText((Join-Path $repository 'file.txt'), "changed-after-snapshot`n", $utf8)
    if ([IO.File]::ReadAllText((Join-Path $snapshotRoot 'file.txt')) -cne "committed-two`n") {
        throw 'A source snapshot changed with the mutable worktree.'
    }
    [void](Assert-NekoSshGitSourceSnapshotIntegrity `
        -RepositoryRoot $repository `
        -Snapshot $snapshot `
        -GitExecutable $gitExecutable)
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceIntegrity `
                -RepositoryRoot $repository `
                -GitExecutable $gitExecutable `
                -Expected $identity |
                Out-Null
        } `
        -MessagePattern '*Git worktree tracked file bytes*do not exactly match*' `
        -FailureMessage 'Post-build source gate did not reject a source change after snapshot creation.'
    Restore-TestWorktree

    $snapshotFile = Get-Item -LiteralPath (Join-Path $snapshotRoot 'file.txt') -Force
    $snapshotFile.IsReadOnly = $false
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceSnapshotIntegrity `
                -RepositoryRoot $repository `
                -Snapshot $snapshot `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*lost its read-only protection*' `
        -FailureMessage 'Snapshot gate did not reject removed read-only protection.'
    $snapshotFile.IsReadOnly = $true

    $snapshotFile.IsReadOnly = $false
    [IO.File]::WriteAllText($snapshotFile.FullName, "tampered-snapshot`n", $utf8)
    $snapshotFile.IsReadOnly = $true
    Assert-Rejected `
        -Action {
            Assert-NekoSshGitSourceSnapshotIntegrity `
                -RepositoryRoot $repository `
                -Snapshot $snapshot `
                -GitExecutable $gitExecutable |
                Out-Null
        } `
        -MessagePattern '*Tracked source snapshot tracked file bytes*do not exactly match*' `
        -FailureMessage 'Snapshot gate did not reject changed tracked bytes.'

    $finalIdentity = Assert-NekoSshGitSourceIntegrity `
        -RepositoryRoot $repository `
        -GitExecutable $gitExecutable `
        -Expected $identity
    if ($finalIdentity.SnapshotSha256 -cne $identity.SnapshotSha256) {
        throw 'Stable source inventory digest changed without a Git source change.'
    }

    [pscustomobject]@{
        CleanMain = 'passed'
        HeadMatchesOriginMain = 'passed'
        GitConfigInjectionCleared = 'passed'
        SkipWorktreeRejected = 'passed'
        AssumeUnchangedRejected = 'passed'
        StagedChangeRejected = 'passed'
        WorktreeChangeRejected = 'passed'
        CrlfNormalizationRejected = 'passed'
        MaliciousCleanFilterRejected = 'passed'
        MaliciousSmudgeFilterBypassed = 'passed'
        UntrackedFileRejected = 'passed'
        NonMainBranchRejected = 'passed'
        OriginMismatchRejected = 'passed'
        IsolatedSnapshot = 'passed'
        SnapshotReadOnly = 'passed'
        SnapshotTamperRejected = 'passed'
        PostBuildRecheck = 'passed'
        SnapshotSha256 = $identity.SnapshotSha256
        Tree = $identity.Tree
    } | ConvertTo-Json -Compress
}
finally {
    Remove-TestRoot
}
