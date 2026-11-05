package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
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
		if id == d.splitSession {
			d.splitSession = d.currentSession
		} else if d.splitSession != 0 && d.splitFirstSession == d.currentSession {
			d.splitFirstSession = id
		}
		d.currentSession = id
		d.term, d.shell, d.active = s.term, s.shell, s.active
		d.fileDir, d.fileLocal, d.fileStatus, d.selectedFile = s.fileDir, s.fileLocal, s.fileStatus, s.selectedFile
		d.fileFilter = s.fileFilter
		d.fileNewDirectory, d.fileRename = "", ""
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
		if s.term != nil {
			_ = s.term.Close()
		}
		if d.splitSession == id {
			d.splitSession, d.splitFirstSession = 0, 0
		}
		d.sessions = append(d.sessions[:i], d.sessions[i+1:]...)
		if id != d.currentSession {
			d.status = "已关闭会话：" + s.label
			return
		}
		d.currentSession = 0
		d.splitSession, d.splitFirstSession = 0, 0
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
	if d.updateCancel != nil {
		d.updateCancel()
	}
	if d.cancelTransfer != nil {
		d.cancelTransfer()
	}
	for _, s := range d.sessions {
		_ = s.term.Close()
	}
	d.sessions = nil
	d.term, d.shell, d.active = nil, nil, connectionSettings{}
	d.currentSession, d.splitSession, d.splitFirstSession = 0, 0, 0
}

// toggleSplit displays a second, already open session without duplicating its terminal.
func (d *desktop) toggleSplit() {
	if !d.canSwitchSession() {
		return
	}
	if d.splitSession != 0 {
		d.splitSession, d.splitFirstSession = 0, 0
		return
	}
	for _, s := range d.sessions {
		if s.id != d.currentSession && s.term != nil {
			d.splitSession, d.splitFirstSession = s.id, d.currentSession
			return
		}
	}
}

func (d *desktop) terminalWorkspace(c *ui.Context, width float32) {
	var second *workspaceSession
	for _, s := range d.sessions {
		if s.id == d.splitSession && s.id != d.currentSession && s.term != nil {
			second = s
			break
		}
	}
	if second == nil {
		terminal.View(c, d.term).Fill()
		return
	}
	stacked := width < 1100 || (d.showFilePanel && width < 1450)
	pane := func(s *workspaceSession, current bool) {
		container := ui.Column(c).Grow(1)
		if stacked {
			container.FillWidth().HeightPercent(50)
		} else {
			container.FillHeight().WidthPercent(50)
		}
		container.Children(func() {
			label := "切换到：" + s.label
			if current {
				label = "当前会话：" + s.label
			}
			if ui.Button(c.Key(fmt.Sprintf("split-focus-%d", s.id)), label).Disabled(current || !d.canSwitchSession()).Clicked() {
				d.activateSession(s.id)
			}
			terminal.View(c, s.term).Grow(1).FillWidth()
		})
	}
	var current *workspaceSession
	for _, s := range d.sessions {
		if s.id == d.currentSession {
			current = s
			break
		}
	}
	if current == nil || current.term == nil {
		terminal.View(c, d.term).Fill()
		return
	}
	panes := func() {
		if second.id == d.splitFirstSession {
			pane(second, false)
			pane(current, true)
		} else {
			pane(current, true)
			pane(second, false)
		}
	}
	if stacked {
		ui.Column(c).Fill().Children(panes)
	} else {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(panes)
	}
}
