package main

import (
	"fmt"
	"github.com/egoist/mygo/plugins/terminal"
)

func openSSHSession(settings connectionSettings, options terminal.Options) (*remoteShell, *terminal.Terminal, error) {
	shell, err := connectShell(settings)
	if err != nil {
		return nil, nil, err
	}
	options.Conn = shell
	term, err := terminal.New(options)
	if err != nil {
		_ = shell.Close()
		return nil, nil, err
	}
	return shell, term, nil
}

func (d *desktop) reconnectCurrentSession() {
	if d.busy || d.shell == nil || !terminalEnded(d.term) {
		return
	}
	id, settings, oldTerm := d.currentSession, d.active, d.term
	options := d.terminalOptions()
	d.busy = true
	d.status = "正在重新连接并验证主机指纹…"
	go func() {
		shell, term, err := openSSHSession(settings, options)
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.status = fmt.Sprintf("重新连接失败：%v", err)
				return
			}
			for _, s := range d.sessions {
				if s.id != id || s.term != oldTerm {
					continue
				}
				for _, forward := range s.forwards {
					forward.Close()
				}
				s.forwards = nil
				_ = oldTerm.Close()
				term.SetFont(terminal.Font{Size: float32(d.currentTerminalFontSize())})
				term.SetTheme(d.terminalLightTheme, d.terminalDarkTheme)
				// A new terminal has no old scrollback; discard its old search.
				s.terminalSearchOpen, s.terminalSearchText = false, ""
				s.term, s.shell, s.files, s.refreshPending = term, shell, nil, false
				s.selectedFile, s.fileStatus = "", ""
				s.lastTerminalDir = ""
				if d.currentSession == id {
					d.terminalSearchOpen, d.terminalSearchText = false, ""
					d.term, d.shell = term, shell
					d.files, d.selectedFile, d.confirmDelete, d.lastTerminalDir = nil, "", "", ""
					d.fileRename, d.fileNewDirectory = "", ""
					d.status = "已重新连接"
					d.refreshFiles()
				}
				return
			}
			// The owning window/session disappeared before completion.
			_ = term.Close()
		})
	}()
}
