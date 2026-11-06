package main

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/plugins/terminal"
	"golang.org/x/crypto/ssh"
)

func openSSHSession(settings connectionSettings, options terminal.Options) (*remoteShell, *terminal.Terminal, error) {
	return openSSHSessionAt(settings, options, "")
}

func openSSHSessionAt(settings connectionSettings, options terminal.Options, dir string) (*remoteShell, *terminal.Terminal, error) {
	return openSSHSessionAtThrough(settings, options, dir, nil)
}

func openSSHSessionAtThrough(settings connectionSettings, options terminal.Options, dir string, via *ssh.Client) (*remoteShell, *terminal.Terminal, error) {
	return openSSHSessionAtThroughContext(context.Background(), settings, options, dir, via)
}

func openSSHSessionAtThroughContext(ctx context.Context, settings connectionSettings, options terminal.Options, dir string, via *ssh.Client) (*remoteShell, *terminal.Terminal, error) {
	shell, err := connectShellAtThroughContext(ctx, settings, dir, via)
	if err != nil {
		return nil, nil, err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = shell.Close() })
	defer stopCancellation()
	options.Conn = shell
	term, err := terminal.New(options)
	if err != nil {
		_ = shell.Close()
		return nil, nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = term.Close()
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
	var owner *workspaceSession
	for _, session := range d.sessions {
		if session.id == id && session.term == oldTerm {
			owner = session
			break
		}
	}
	if owner == nil {
		return
	}
	via, err := d.sessionJumpClient(owner)
	if err != nil {
		d.status = err.Error()
		return
	}
	if settings.Fingerprint == "" {
		d.status = "该主机的信任已撤销，请在主机详情中重新连接并核对指纹。"
		return
	}
	options := d.terminalOptions()
	d.busy = true
	d.status = "正在重新连接并验证主机指纹…"
	go func() {
		shell, term, err := openSSHSessionAtThrough(settings, options, "", via)
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
				if group := d.terminalGroupFor(s.id); group != nil {
					group.broadcast, group.broadcastMembers = false, nil
				}
				d.bindTerminalBroadcast(s)
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
