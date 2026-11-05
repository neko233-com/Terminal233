package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo/plugins/terminal"
)

const maxWorkspaceSessions = 12

// Each tab owns its terminal and remote file selection. Credentials stay in
// memory and are never part of the persisted connection list.
type workspaceSession struct {
	id                                           int
	label                                        string
	term                                         *terminal.Terminal
	shell                                        *remoteShell
	active                                       connectionSettings
	fileDir, fileLocal, fileStatus, selectedFile string
	fileFilter                                   string
	files                                        []remoteFile
	refreshPending                               bool
}

func (d *desktop) canSwitchSession() bool {
	return !d.busy || d.cancelTransfer != nil
}

func (d *desktop) saveSessionState() {
	for _, s := range d.sessions {
		if s.id != d.currentSession {
			continue
		}
		s.fileDir, s.fileLocal, s.fileStatus, s.selectedFile = d.fileDir, d.fileLocal, d.fileStatus, d.selectedFile
		s.fileFilter = d.fileFilter
		s.files = d.files
		return
	}
}

func (d *desktop) activateSession(id int) {
	if !d.canSwitchSession() {
		return
	}
	for _, s := range d.sessions {
		if s.id != id {
			continue
		}
		d.saveSessionState()
		d.currentSession = id
		d.term, d.shell, d.active = s.term, s.shell, s.active
		d.fileDir, d.fileLocal, d.fileStatus, d.selectedFile = s.fileDir, s.fileLocal, s.fileStatus, s.selectedFile
		d.fileFilter = s.fileFilter
		d.fileNewDirectory = ""
		d.files, d.confirmDelete = s.files, ""
		d.workspace = "terminal"
		d.status = "当前会话：" + s.label
		if s.refreshPending && d.term != nil && !d.busy && d.win != nil {
			s.refreshPending = false
			d.refreshFiles()
			return
		}
		if d.showFilePanel && d.term != nil && d.shell == nil && d.files == nil && d.fileStatus == "" && d.win != nil {
			d.refreshFiles()
		}
		return
	}
}

func (d *desktop) addSession(term *terminal.Terminal, shell *remoteShell, active connectionSettings, label string) {
	d.saveSessionState()
	d.nextSession++
	s := &workspaceSession{id: d.nextSession, label: label, term: term, shell: shell, active: active}
	if shell != nil {
		s.fileDir = "."
	} else {
		s.fileDir, _ = os.UserHomeDir()
		if s.fileDir == "" {
			s.fileDir = "."
		}
	}
	d.sessions = append(d.sessions, s)
	d.currentSession = 0
	d.activateSession(s.id)
}

func (d *desktop) closeCurrentSession() {
	d.closeSession(d.currentSession)
}

func (d *desktop) closeSession(id int) {
	if d.busy {
		return
	}
	for i, s := range d.sessions {
		if s.id != id {
			continue
		}
		_ = s.term.Close()
		d.sessions = append(d.sessions[:i], d.sessions[i+1:]...)
		if id != d.currentSession {
			d.status = "已关闭会话：" + s.label
			return
		}
		d.currentSession = 0
		d.term, d.shell, d.active = nil, nil, connectionSettings{}
		d.files, d.fileDir, d.fileLocal, d.fileStatus, d.selectedFile, d.confirmDelete = nil, "", "", "", "", ""
		d.fileFilter = ""
		if len(d.sessions) == 0 {
			d.workspace, d.status = "connections", "已断开"
			return
		}
		if i >= len(d.sessions) {
			i = len(d.sessions) - 1
		}
		d.activateSession(d.sessions[i].id)
		d.status = fmt.Sprintf("已关闭会话；当前：%s", d.sessions[i].label)
		return
	}
}

func (d *desktop) closeAllSessions() {
	if d.cancelTransfer != nil {
		d.cancelTransfer()
	}
	for _, s := range d.sessions {
		_ = s.term.Close()
	}
	d.sessions = nil
	d.term, d.shell, d.active = nil, nil, connectionSettings{}
	d.currentSession = 0
}
