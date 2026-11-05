package main

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/egoist/mygo/plugins/terminal"
)

// openLocalTerminal starts a local interactive shell, without an SSH client or server.
func (d *desktop) openLocalTerminal() {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions {
		return
	}
	options := terminal.Options{NoClipboardWrite: true, ImageLimit: -1, Env: []string{"TERM=xterm-256color", "COLORTERM=truecolor"}}
	if runtime.GOOS == "windows" {
		shell, err := exec.LookPath("pwsh.exe")
		if err != nil {
			d.status = "未找到 PowerShell 7（pwsh），请先安装并加入 PATH。"
			return
		}
		options.Command = []string{shell, "-NoLogo", "-NoExit", "-Command", "if (Get-Module -ListAvailable PSReadLine) { Import-Module PSReadLine }"}
	}
	t, err := terminal.New(options)
	if err != nil {
		d.status = fmt.Sprintf("本机终端启动失败：%v", err)
		return
	}
	d.addSession(t, nil, connectionSettings{}, fmt.Sprintf("本机终端 %d", d.nextSession+1))
	d.status = "本机终端已打开；命令颜色由本机 shell 提供。"
}
