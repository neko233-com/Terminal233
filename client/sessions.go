package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

const maxWorkspaceSessions = 16
const maxFileSessions = 16
const legacyWorkspaceSessions = 12

// File-only SFTP transports do not consume terminal slots.
func (d *desktop) sessionCount(fileOnly bool) int {
	count := 0
	for _, s := range d.sessions {
		if (s != nil && s.fileOnly) == fileOnly {
			count++
		}
	}
	return count
}
func (d *desktop) terminalLimitReached() bool { return d.sessionCount(false) >= maxWorkspaceSessions }
func (d *desktop) fileLimitReached() bool     { return d.sessionCount(true) >= maxFileSessions }
func (d *desktop) connectionLimitReached() bool {
	if d.fileReturnAfterConnect {
		return d.fileLimitReached()
	}
	return d.terminalLimitReached()
}

type sessionDragID int

func (d *desktop) neighborSession(id, delta int) int {
	tabs := d.terminalTopTabs()
	for i, tab := range tabs {
		for _, s := range tab.sessions {
			if s.id == id && i+delta >= 0 && i+delta < len(tabs) {
				return tabs[i+delta].representative
			}
		}
	}
	return 0
}

// Reorder whole top-level tabs, keeping grouped sessions together.
func (d *desktop) moveSession(from, to int) { d.moveTerminalTopTab(from, to) }

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

func transportEnded(shell *remoteShell) bool {
	if shell == nil {
		return true
	}
	select {
	case <-shell.done:
		return true
	default:
		return false
	}
}

func (s *workspaceSession) fileAvailable() bool {
	if s == nil {
		return false
	}
	if s.fileOnly {
		return s.shell != nil && !transportEnded(s.shell)
	}
	return s.term != nil && !terminalEnded(s.term)
}

func (d *desktop) fileSessionUnavailable() bool {
	if d.term != nil {
		return terminalEnded(d.term)
	}
	for _, s := range d.sessions {
		if s.id == d.currentSession && s.fileOnly && s.shell == d.shell {
			return !s.fileAvailable()
		}
	}
	return true
}

func sessionDisplayLabel(s *workspaceSession) string {
	if s.fileOnly {
		if transportEnded(s.shell) {
			return "SFTP · " + s.label + " · 已断开"
		}
		return "SFTP · " + s.label
	}
	if terminalEnded(s.term) {
		if s.shell != nil {
			return s.label + " · 已断开"
		}
		return s.label + " · 已退出"
	}
	return s.label
}

// Each tab owns its terminal and remote file selection. Session credentials
// stay in memory; saved host credentials are protected separately.
type workspaceSession struct {
	id                                                              int
	label                                                           string
	fileOnly                                                        bool
	term                                                            *terminal.Terminal
	shell                                                           *remoteShell
	active                                                          connectionSettings
	fileDir, fileLocal, fileStatus, selectedFile                    string
	fileFilter, fileSort                                            string
	fileSortDescending, fileHideDotfiles, fileFollowTerminal        bool
	lastTerminalDir                                                 string
	files                                                           []remoteFile
	fileHistory                                                     fileHistory
	filePaneSelection                                               paneSelection
	fileBatchErrors                                                 []string
	fileBatchDetails                                                bool
	fileChecked                                                     map[string]bool
	fileCheckedDir, fileBatchTarget                                 string
	terminalSearchOpen                                              bool
	terminalSearchText                                              string
	refreshPending                                                  bool
	cloudOriginURL, cloudOriginUser, cloudOriginTeam, cloudOriginID string
	sourceProfileID                                                 string
	cloudSources                                                    map[string]string
	cloudVersions                                                   map[string]string
	forwards                                                        []*localForward
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
		s.fileHistory = d.fileHistory
		s.filePaneSelection = d.filePaneSelection
		s.fileBatchErrors, s.fileBatchDetails = d.fileBatchErrors, d.fileBatchDetails
		s.fileChecked, s.fileCheckedDir, s.fileBatchTarget = d.fileChecked, d.fileCheckedDir, d.fileBatchTarget
		s.terminalSearchOpen, s.terminalSearchText = d.terminalSearchOpen, d.terminalSearchText
		return
	}
}

func (d *desktop) activateSession(id int) {
	d.activateSessionWithRefresh(id, true)
}

func (d *desktop) activateSessionWithRefresh(id int, refresh bool) {
	if !d.canSwitchSession() {
		return
	}
	for _, s := range d.sessions {
		if s.id != id {
			continue
		}
		if d.currentSession != id {
			d.stopMonitor()
		}
		d.saveSessionState()
		oldGroup, nextGroup := d.terminalGroupFor(d.currentSession), d.terminalGroupFor(id)
		if oldGroup != nil || nextGroup != nil {
			d.splitSession, d.splitFirstSession, d.splitFocusedSession = 0, 0, 0
			if nextGroup != nil {
				nextGroup.focused = id
			}
		} else if id == d.splitSession {
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
		d.fileHistory = s.fileHistory
		d.filePaneSelection = s.filePaneSelection
		d.fileBatchErrors, d.fileBatchDetails = s.fileBatchErrors, s.fileBatchDetails
		d.fileChecked, d.fileCheckedDir, d.fileBatchTarget = s.fileChecked, s.fileCheckedDir, s.fileBatchTarget
		d.terminalSearchOpen, d.terminalSearchText = s.terminalSearchOpen, s.terminalSearchText
		d.workspace = "terminal"
		if s.fileOnly {
			d.workspace = "files"
		}
		d.status = "当前会话：" + s.label
		if refresh && s.refreshPending && !d.fileSessionUnavailable() && !d.busy && d.win != nil {
			s.refreshPending = false
			d.refreshFilesAfter(d.fileStatus)
			return
		}
		if refresh && d.showFilePanel && !d.fileSessionUnavailable() && d.shell == nil && d.files == nil && d.fileStatus == "" && d.win != nil {
			d.refreshFiles()
		}
		return
	}
}

func (d *desktop) addSession(term *terminal.Terminal, shell *remoteShell, active connectionSettings, label string) {
	d.saveSessionState()
	if term != nil {
		term.SetFont(terminal.Font{Size: float32(d.currentTerminalFontSize())})
		term.SetTheme(d.terminalLightTheme, d.terminalDarkTheme)
	}
	d.nextSession++
	s := &workspaceSession{id: d.nextSession, label: label, term: term, shell: shell, active: active, fileHideDotfiles: true, fileOnly: term == nil && shell != nil}
	d.bindTerminalBroadcast(s)
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
		wasFiles := d.workspace == "files"
		nextGroupMember := d.detachGroupSession(id)
		if id == d.monitorSession {
			d.stopMonitor()
		}
		for _, forward := range s.forwards {
			forward.Close()
		}
		if s.term != nil {
			_ = s.term.Close()
		} else if s.fileOnly && s.shell != nil {
			_ = s.shell.Close()
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
		d.fileHistory = fileHistory{}
		d.filePaneSelection = paneSelection{}
		d.fileBatchErrors, d.fileBatchDetails = nil, false
		d.fileChecked, d.fileCheckedDir, d.fileBatchTarget = nil, "", ""
		d.terminalSearchOpen, d.terminalSearchText = false, ""
		d.fileFilter, d.fileSort = "", ""
		d.fileSortDescending, d.fileHideDotfiles, d.fileFollowTerminal = false, false, false
		d.lastTerminalDir = ""
		if len(d.sessions) == 0 {
			d.workspace, d.status = "connections", "已断开"
			if wasFiles {
				d.workspace = "files"
			}
			return
		}
		if nextGroupMember != 0 {
			d.activateSession(nextGroupMember)
			if wasFiles {
				d.workspace = "files"
			}
			d.status = "已关闭会话；已切换到工作区中的其他终端"
			return
		}
		if i >= len(d.sessions) {
			i = len(d.sessions) - 1
		}
		d.activateSession(d.sessions[i].id)
		if wasFiles {
			d.workspace = "files"
		}
		d.status = fmt.Sprintf("已关闭会话；当前：%s", d.sessions[i].label)
		return
	}
}

func (d *desktop) closeAllSessions() {
	for id := range d.savedForwardRuns {
		d.stopSavedForward(id)
	}
	d.stopMonitor()
	if d.archiveCancel != nil {
		d.archiveCancel()
	}
	if d.cloudBatchCancel != nil {
		d.cloudBatchCancel()
	}
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
		} else if s.fileOnly && s.shell != nil {
			_ = s.shell.Close()
		}
	}
	d.sessions = nil
	d.terminalGroups = nil
	d.confirmTerminalGroupClose = 0
	d.term, d.shell, d.active = nil, nil, connectionSettings{}
	d.currentSession, d.splitSession, d.splitFirstSession, d.pendingTerminalFocus = 0, 0, 0, 0
}

// toggleSplit displays a second, already open session without duplicating its terminal.
func (d *desktop) toggleSplit() {
	if !d.canSwitchSession() {
		return
	}
	if group := d.terminalGroupFor(d.currentSession); group != nil {
		if len(group.members) > 1 {
			group.grid = !group.grid
			d.pendingTerminalFocus = d.currentSession
		}
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
	if group := d.terminalGroupFor(d.currentSession); group != nil {
		d.terminalGroupView(c, group, width)
		return
	}
	var second *workspaceSession
	for _, s := range d.sessions {
		if s.id == d.splitSession && s.id != d.currentSession && s.term != nil {
			second = s
			break
		}
	}
	if second == nil {
		d.splitFocusedSession = 0
		view := terminal.View(c, d.term).Fill()
		if d.pendingTerminalFocus == d.currentSession && d.currentSession != 0 {
			view.Focus()
			d.pendingTerminalFocus = 0
		}
		return
	}
	stacked := d.splitLayout == "stacked" || (d.splitLayout == "" && (width < 1100 || (d.showFilePanel && width < 1450)))
	// Two vertically stacked terminals become too short in a shallow window.
	// Keep both usable side by side until there is enough vertical space.
	_, height := c.Size()
	if height < 650 && d.splitLayout == "" {
		stacked = false
	}
	focusedID := 0
	pane := func(s *workspaceSession, current bool) {
		container := ui.Column(c).Fill()
		container.Children(func() {
			label := "切换到：" + s.label
			if current {
				label = "当前会话：" + s.label
			}
			button := ui.Button(c.Key(fmt.Sprintf("split-focus-%d", s.id)), label).BorderWidth(0).Padding(4, 10).FontSize(12).Disabled(current || !d.canSwitchSession())
			if current {
				button = button.Background(c.Theme().SurfacePressed)
			}
			if button.Clicked() {
				d.selectSession(s.id)
			}
			view := terminal.View(c, s.term).Label("终端：" + s.label).Grow(1).FillWidth()
			if d.pendingTerminalFocus == s.id {
				view.Focus()
				d.pendingTerminalFocus = 0
				focusedID = s.id
			}
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
	first, last := current, second
	if second.id == d.splitFirstSession {
		first, last = second, current
	}
	firstPane := func() { pane(first, first.id == current.id) }
	lastPane := func() { pane(last, last.id == current.id) }
	if d.splitHorizontalPercent == 0 {
		d.splitHorizontalPercent = 50
	}
	if d.splitVerticalPercent == 0 {
		d.splitVerticalPercent = 50
	}
	if stacked {
		ui.SplitVerticalPercent(c.Key("terminal-split-vertical"), &d.splitVerticalPercent, firstPane, lastPane).Fill()
	} else {
		ui.SplitPercent(c.Key("terminal-split-horizontal"), &d.splitHorizontalPercent, firstPane, lastPane).Fill()
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

func splitLayoutLabel(layout string) string {
	switch layout {
	case "side":
		return "左右分屏"
	case "stacked":
		return "上下分屏"
	default:
		return "自动布局"
	}
}

func splitLayoutID(label string) string {
	switch label {
	case "左右分屏":
		return "side"
	case "上下分屏":
		return "stacked"
	default:
		return ""
	}
}

func (d *desktop) changeSplitLayout(layout string) {
	if layout != "" && layout != "side" && layout != "stacked" {
		return
	}
	d.splitLayout = layout
	if d.preferencesPath == "" {
		return
	}
	if err := updatePreferences(d.preferencesPath, func(p *workspacePreferences) error { p.SplitLayout = layout; return nil }); err != nil {
		d.preferencesStatus = "界面设置未保存：" + err.Error()
	} else {
		d.preferencesStatus = ""
	}
}

// Explicit tab and pane selection also directs subsequent typing to that session.
func (d *desktop) selectSession(id int) {
	d.activateSession(id)
	if d.currentSession == id && d.term != nil {
		d.pendingTerminalFocus = id
	}
}

func (d *desktop) selectAdjacentSession(delta int) {
	if !d.canSwitchSession() {
		return
	}
	tabs := d.terminalTopTabs()
	if len(tabs) < 2 {
		return
	}
	for index, tab := range tabs {
		for _, session := range tab.sessions {
			if session.id != d.currentSession {
				continue
			}
			next := tabs[(index+delta+len(tabs))%len(tabs)]
			id := next.representative
			if next.group != nil {
				id = next.group.focused
			}
			d.selectSession(id)
			return
		}
	}
}
