package main

import (
	"context"
	"fmt"
	"time"
)

func (d *desktop) currentSFTPSession() *workspaceSession {
	for _, s := range d.sessions {
		if s.id == d.currentSession && s.fileOnly && s.shell == d.shell {
			return s
		}
	}
	return nil
}

func (d *desktop) setSFTPStatus(s *workspaceSession, message string) {
	s.fileStatus = message
	if d.currentSession == s.id && d.shell == s.shell {
		d.fileStatus = message
	}
	if d.secondaryPane.sessionID == s.id && d.secondaryPane.shell == s.shell {
		d.secondaryPane.status = message
	}
}

func (d *desktop) watchSFTPTransport(s *workspaceSession, shell *remoteShell) {
	if d.win == nil || shell == nil || shell.done == nil {
		return
	}
	go func() {
		<-shell.done
		d.win.Update(func() {
			for _, current := range d.sessions {
				if current != s || current.shell != shell {
					continue
				}
				d.setSFTPStatus(current, "SFTP 已断开，请重新连接。")
				return
			}
		})
	}()
}

func (d *desktop) reconnectSFTP(s *workspaceSession) {
	if d.busy || d.win == nil || s == nil || !s.fileOnly || s.fileAvailable() {
		return
	}
	if s.active.Fingerprint == "" {
		d.setSFTPStatus(s, "主机信任已撤销，请重新确认指纹。")
		return
	}
	settings, old := s.active, s.shell
	d.busy = true
	d.setSFTPStatus(s, "正在重新连接 SFTP…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		shell, err := openSFTPTransport(ctx, settings)
		cancel()
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.setSFTPStatus(s, "SFTP 重新连接失败："+err.Error())
				return
			}
			valid := false
			for _, current := range d.sessions {
				if current == s && current.shell == old && current.active == settings {
					valid = true
					break
				}
			}
			if !valid {
				_ = shell.Close()
				return
			}
			s.shell, s.files, s.selectedFile, s.fileStatus = shell, nil, "", ""
			s.filePaneSelection, s.fileChecked = paneSelection{}, nil
			if d.currentSession == s.id && d.shell == old {
				d.shell, d.files, d.selectedFile, d.fileStatus = shell, nil, "", ""
				d.filePaneSelection, d.fileChecked = paneSelection{}, nil
			}
			if d.secondaryPane.sessionID == s.id && d.secondaryPane.shell == old {
				p := &d.secondaryPane
				p.shell, p.files, p.selected, p.status = shell, nil, "", ""
				p.paneSelection = paneSelection{}
				p.generation++
				p.loading, p.loadingDirectory = false, ""
				d.requestSecondaryPane(p.directory, -1)
			}
			d.watchSFTPTransport(s, shell)
			if d.currentSession == s.id {
				d.refreshFiles()
			}
		})
	}()
}

// Switching to a terminal creates a separate SSH session, preserving the file
// connection and rechecking the host key before credentials are sent.
func (d *desktop) openTerminalFromSFTP(s *workspaceSession) {
	if d.busy || d.win == nil || s == nil || !s.fileOnly || !s.fileAvailable() || d.terminalLimitReached() {
		return
	}
	if s.active.Fingerprint == "" {
		d.setSFTPStatus(s, "请先重新确认主机指纹。")
		return
	}
	settings, originalShell, originalSession := s.active, s.shell, d.currentSession
	options := d.terminalOptions()
	d.busy = true
	d.setSFTPStatus(s, "正在打开终端…")
	go func() {
		shell, term, err := openSSHSession(settings, options)
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.setSFTPStatus(s, fmt.Sprintf("打开终端失败：%v", err))
				return
			}
			valid := false
			for _, current := range d.sessions {
				if current == s && current.shell == originalShell && current.active == settings && current.fileAvailable() {
					valid = true
					break
				}
			}
			if !valid || d.workspace != "files" || d.currentSession != originalSession {
				_ = term.Close()
				if valid {
					d.setSFTPStatus(s, "已取消打开终端。")
				}
				return
			}
			d.setSFTPStatus(s, "")
			d.addSession(term, shell, settings, s.label)
			for _, current := range d.sessions {
				if current.id == d.currentSession {
					current.sourceProfileID = s.sourceProfileID
					current.cloudOriginURL, current.cloudOriginUser, current.cloudOriginTeam, current.cloudOriginID = s.cloudOriginURL, s.cloudOriginUser, s.cloudOriginTeam, s.cloudOriginID
					current.cloudSources, current.cloudVersions = cloneCloudSources(s.cloudSources), cloneCloudSources(s.cloudVersions)
					break
				}
			}
			d.workspace, d.status, d.pendingTerminalFocus = "terminal", "已打开终端", d.currentSession
		})
	}()
}
