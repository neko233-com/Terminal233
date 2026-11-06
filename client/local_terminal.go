package main

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/egoist/mygo/plugins/terminal"
)

// Install after PowerShell profiles so their prompt remains in use. The OSC 7
// sequence reports only filesystem locations and is written outside the
// PowerShell output pipeline; LASTEXITCODE is restored before the user's prompt.
const powerShellIntegration = `
if (Get-Module -ListAvailable PSReadLine) { Import-Module PSReadLine }
$global:NekoSSHOriginalPrompt_233 = (Get-Item Function:\prompt).ScriptBlock
function global:prompt {
    $nekoSSHPreviousSuccess = $?
    $nekoSSHExitCode = $global:LASTEXITCODE
    try {
        $nekoSSHLocation = $ExecutionContext.SessionState.Path.CurrentLocation
        if ($nekoSSHLocation.Provider.Name -eq 'FileSystem') {
            $nekoSSHUri = [System.Uri]::new($nekoSSHLocation.ProviderPath)
            if ($nekoSSHUri.IsFile -and -not $nekoSSHUri.IsUnc) {
                [Console]::Write(([char]27) + ']7;' + $nekoSSHUri.AbsoluteUri + ([char]7))
            }
        }
    } catch { }
    $global:LASTEXITCODE = $nekoSSHExitCode
    if ($nekoSSHPreviousSuccess) { $null = $true } else { Write-Error 'NekoSSH prompt status' -ErrorAction Ignore }
    & $global:NekoSSHOriginalPrompt_233
}
`

// newLocalTerminal starts a local interactive shell without an SSH client.
func newLocalTerminal(options terminal.Options) (*terminal.Terminal, error) {
	options.Env = []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	if runtime.GOOS == "windows" {
		shell, err := exec.LookPath("pwsh.exe")
		if err != nil {
			return nil, fmt.Errorf("未找到 PowerShell 7（pwsh），请先安装并加入 PATH：%w", err)
		}
		options.Command = []string{shell, "-NoLogo", "-NoExit", "-Command", powerShellIntegration}
	}
	return terminal.New(options)
}

func (d *desktop) openLocalTerminal() {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions {
		return
	}
	t, err := newLocalTerminal(d.terminalOptions())
	if err != nil {
		d.status = fmt.Sprintf("本机终端启动失败：%v", err)
		return
	}
	d.addSession(t, nil, connectionSettings{}, fmt.Sprintf("本机终端 %d", d.nextSession+1))
	d.status = "本机终端已打开；命令颜色由本机 shell 提供。"
}
