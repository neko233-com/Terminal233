package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

const maxWorkspaceSessions = 12

func terminalEnded(term *terminal.Terminal) bool {
	if term == nil {
		return false
	}
	select {
	case <-term.Done():
		return true
	default:
		return false
	}
}

func (d *desktop) fileSessionUnavailable() bool { return d.term == nil || terminalEnded(d.term) }

func sessionDisplayLabel(s *workspaceSession) string {
	if terminalEnded(s.term) {
		if s.shell != nil {
			return s.label + " · 已断开"
		}
		return s.label + " · 已退出"
	}
	return s.label
}

// Each tab owns its terminal and remote file selection. Credentials stay in
// memory and are never part of the persisted connection list.
type workspaceSession struct {
	id                                                       int
	label                                                    string
	term                                                     *terminal.Terminal
	shell                                                    *remoteShell
	active                                                   connectionSettings
	fileDir, fileLocal, fileStatus, selectedFile             string
	fileFilter, fileSort                                     string
	fileSortDescending, fileHideDotfiles, fileFollowTerminal bool
	lastTerminalDir                                          string
	files                                                    []remoteFile
	refreshPending                                           bool
	cloudSources                                             map[string]string
	forwards                                                 []*localForward
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
		s.fileFilter, s.fileSort = d.fileFilter, d.fileSort
		s.fileSortDescending, s.fileHideDotfiles, s.fileFollowTerminal = d.fileSortDescending, d.fileHideDotfiles, d.fileFollowTerminal
		s.lastTerminalDir = d.lastTerminalDir
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
		d.forwardStatus = ""
		d.term, d.shell, d.active = s.term, s.shell, s.active
		d.fileDir, d.fileLocal, d.fileStatus, d.selectedFile = s.fileDir, s.fileLocal, s.fileStatus, s.selectedFile
		d.fileFilter, d.fileSort = s.fileFilter, s.fileSort
		d.fileSortDescending, d.fileHideDotfiles, d.fileFollowTerminal = s.fileSortDescending, s.fileHideDotfiles, s.fileFollowTerminal
		d.lastTerminalDir = s.lastTerminalDir
		d.fileNewDirectory, d.fileRename = "", ""
		d.files, d.confirmDelete = s.files, ""
		d.workspace = "terminal"
		d.status = "当前会话：" + s.label
		if s.refreshPending && !d.fileSessionUnavailable() && !d.busy && d.win != nil {
			s.refreshPending = false
			d.refreshFilesAfter(d.fileStatus)
			return
		}
		if d.showFilePanel && !d.fileSessionUnavailable() && d.shell == nil && d.files == nil && d.fileStatus == "" && d.win != nil {
			d.refreshFiles()
		}
		return
	}
}

func (d *desktop) addSession(term *terminal.Terminal, shell *remoteShell, active connectionSettings, label string) {
	d.saveSessionState()
	if term != nil {
		term.SetFont(terminal.Font{Size: float32(d.currentTerminalFontSize())})
	}
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
		for _, forward := range s.forwards {
			forward.Close()
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
		d.fileFilter, d.fileSort = "", ""
		d.fileSortDescending, d.fileHideDotfiles, d.fileFollowTerminal = false, false, false
		d.lastTerminalDir = ""
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
		for _, forward := range s.forwards {
			forward.Close()
		}
		if s.term != nil {
			_ = s.term.Close()
		}
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
		d.splitFocusedSession = 0
		terminal.View(c, d.term).Fill()
		return
	}
	stacked := width < 1100 || (d.showFilePanel && width < 1450)
	focusedID := 0
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
			view := terminal.View(c, s.term).Label("终端：" + s.label).Grow(1).FillWidth()
			if view.Focused() {
				focusedID = s.id
			}
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
	// A terminal takes keyboard focus on pointer input. Follow focus changes
	// so the file panel and close action refer to the terminal being used.
	if focusedID != 0 && focusedID != d.currentSession && focusedID != d.splitFocusedSession && d.canSwitchSession() {
		d.activateSession(focusedID)
		c.Invalidate()
	}
	if !d.canSwitchSession() && focusedID != d.currentSession {
		d.splitFocusedSession = 0
	} else {
		d.splitFocusedSession = focusedID
	}
}
