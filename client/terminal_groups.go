package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type terminalGroup struct {
	id                        int
	name                      string
	members                   []int
	focused                   int
	grid                      bool
	broadcast                 bool
	broadcastMembers          map[int]bool
	tabNames                  map[int]string
	memberRenameID            int
	memberRenameOpen          bool
	memberRenameDraft         string
	memberRenameStatus        string
	showAdd                   bool
	renameOpen                bool
	renameDraft, renameStatus string
	rowPerc                   [maxWorkspaceSessions]float32
	columnPerc                [maxWorkspaceSessions]float32
	savedLocal                *savedWorkspace
	savedCloud                *cloudWorkspace
	savedCloudURL, savedUser  string
	savedSignature            [32]byte
	pendingCloud              *pendingWorkspaceSync
}

type terminalGroupMemberDrag int

type terminalTopTab struct {
	representative int
	group          *terminalGroup
	sessions       []*workspaceSession
}

func (d *desktop) terminalGroupFor(id int) *terminalGroup {
	for _, g := range d.terminalGroups {
		for _, member := range g.members {
			if member == id {
				return g
			}
		}
	}
	return nil
}
func (d *desktop) terminalGroupSessions(g *terminalGroup) []*workspaceSession {
	var result []*workspaceSession
	if g == nil {
		return result
	}
	for _, id := range g.members {
		for _, s := range d.sessions {
			if s.id == id {
				result = append(result, s)
				break
			}
		}
	}
	return result
}

func sessionTerminalDirectory(s *workspaceSession) string {
	if s == nil || s.term == nil || s.serial != nil {
		return ""
	}
	dir, err := terminalDirectory(s.term.Dir(), s.shell == nil)
	if err != nil || !validWorkspaceText(dir, 4096) {
		return ""
	}
	return dir
}
func (d *desktop) createTerminalGroup(name string, ids []int, focus int, grid bool) error {
	name = strings.TrimSpace(name)
	if name == "" || !validWorkspaceText(name, 120) || len(ids) == 0 || len(ids) > maxWorkspaceSessions {
		return errors.New("请输入有效工作区名称和会话")
	}
	seen := map[int]bool{}
	foundFocus := false
	for _, id := range ids {
		if id == 0 || seen[id] || d.terminalGroupFor(id) != nil {
			return errors.New("会话已属于工作区或不存在")
		}
		seen[id] = true
		found := false
		for _, s := range d.sessions {
			if s.id == id && s.term != nil {
				found = true
				break
			}
		}
		if !found {
			return errors.New("会话已不存在")
		}
		if id == focus {
			foundFocus = true
		}
	}
	if !foundFocus {
		return errors.New("当前会话不在工作区中")
	}
	d.nextTerminalGroupID++
	d.terminalGroups = append(d.terminalGroups, &terminalGroup{id: d.nextTerminalGroupID, name: name, members: append([]int(nil), ids...), focused: focus, grid: grid})
	d.splitSession, d.splitFirstSession, d.splitFocusedSession = 0, 0, 0
	return nil
}
func (d *desktop) createGroupFromCurrentSessions() {
	if d.busy || d.terminalGroupFor(d.currentSession) != nil {
		return
	}
	ids := []int{}
	focus := 0
	for _, s := range d.sessions {
		if d.terminalGroupFor(s.id) == nil && s.term != nil && !terminalEnded(s.term) && (!d.showTerminalGroupSelection || d.terminalGroupSelected[s.id]) {
			ids = append(ids, s.id)
			if s.id == d.currentSession {
				focus = s.id
			}
		}
	}
	if len(ids) < 2 {
		d.workspaceTemplatesStatus = "至少需要两个未分组的活动会话"
		return
	}
	if focus == 0 {
		focus = ids[0]
	}
	if err := d.createTerminalGroup(d.workspaceTemplateName, ids, focus, false); err != nil {
		d.workspaceTemplatesStatus = err.Error()
		return
	}
	d.selectSession(focus)
	d.newTabOpen = false
	d.workspace = "terminal"
	d.pendingTerminalFocus = d.currentSession
	d.showTerminalGroupSelection = false
	d.terminalGroupSelected = nil
	d.status = "已创建工作区：" + strings.TrimSpace(d.workspaceTemplateName)
	d.workspaceTemplateName = ""
	d.workspaceTemplatesStatus = ""
}
func (d *desktop) renameTerminalGroup(group *terminalGroup) {
	if d.busy || group == nil {
		return
	}
	name := strings.TrimSpace(group.renameDraft)
	if name == "" || !validWorkspaceText(name, 120) {
		group.renameStatus = "请输入有效的工作区名称"
		return
	}
	group.name, group.renameOpen, group.renameStatus = name, false, ""
}

func (d *desktop) disbandTerminalGroup(id int) {
	active := d.terminalGroupFor(d.currentSession)
	for i, g := range d.terminalGroups {
		if g.id == id {
			d.terminalGroups = append(d.terminalGroups[:i], d.terminalGroups[i+1:]...)
			break
		}
	}
	if d.confirmTerminalGroupClose == id {
		d.confirmTerminalGroupClose = 0
	}
	if active != nil && active.id == id {
		d.splitSession, d.splitFirstSession, d.splitFocusedSession = 0, 0, 0
		d.pendingTerminalFocus = d.currentSession
	}
}
func (d *desktop) closeTerminalGroup(id int) {
	if d.busy {
		return
	}
	for _, g := range d.terminalGroups {
		if g.id != id {
			continue
		}
		if d.confirmTerminalGroupClose != id {
			d.confirmTerminalGroupClose = id
			return
		}
		ids := append([]int(nil), g.members...)
		d.disbandTerminalGroup(id)
		for _, member := range ids {
			d.closeSession(member)
		}
		return
	}
}

// detachGroupSession returns a sibling to focus after the active member closes.
func (d *desktop) detachGroupSession(id int) int {
	g := d.terminalGroupFor(id)
	if g == nil {
		return 0
	}
	g.broadcast, g.broadcastMembers = false, nil
	delete(g.tabNames, id)
	if g.memberRenameID == id {
		g.memberRenameID, g.memberRenameOpen = 0, false
	}
	index := 0
	for i, member := range g.members {
		if member == id {
			index = i
			break
		}
	}
	g.members = append(g.members[:index], g.members[index+1:]...)
	if len(g.members) == 0 {
		d.disbandTerminalGroup(g.id)
		return 0
	}
	if len(g.members) < 2 {
		g.grid = false
	}
	if index >= len(g.members) {
		index = len(g.members) - 1
	}
	if g.focused == id {
		g.focused = g.members[index]
	}
	return g.focused
}
func moveTerminalGroupMember(group *terminalGroup, from, to int) {
	if group == nil {
		return
	}
	a, b := -1, -1
	for i, id := range group.members {
		if id == from {
			a = i
		}
		if id == to {
			b = i
		}
	}
	if a < 0 || b < 0 || a == b {
		return
	}
	id := group.members[a]
	if a < b {
		copy(group.members[a:b], group.members[a+1:b+1])
	} else {
		copy(group.members[b+1:a+1], group.members[b:a])
	}
	group.members[b] = id
}

func (d *desktop) addTerminalGroupMember(group *terminalGroup, id int) error {
	if d.busy || group == nil || d.terminalGroupFor(id) != nil {
		return errors.New("会话已属于工作区")
	}
	registered := false
	for _, current := range d.terminalGroups {
		if current == group {
			registered = true
			break
		}
	}
	if !registered {
		return errors.New("工作区已关闭")
	}
	found := false
	for _, session := range d.sessions {
		if session.id == id && session.term != nil && !terminalEnded(session.term) {
			found = true
			break
		}
	}
	if !found {
		return errors.New("会话已结束或不存在")
	}
	group.broadcast, group.broadcastMembers = false, nil
	group.members = append(group.members, id)
	group.focused = id
	d.selectSession(id)
	return nil
}

func (d *desktop) groupAddButton(c *ui.Context, group *terminalGroup) {
	button := terminalActionButton(c, fmt.Sprintf("group-add-%d", group.id), "添加工作区终端", plusIcon, group.showAdd, d.busy)
	if button.Clicked() {
		group.showAdd = !group.showAdd
	}
	ui.PopoverBase(c, button, &group.showAdd, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(270).Padding(8).Gap(4).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
				if ui.Button(c.Key(fmt.Sprintf("group-add-local-%d", group.id)), "新建本机终端").FillWidth().Justify(ui.Start).Disabled(d.busy || d.terminalLimitReached()).Clicked() {
					before := d.nextSession
					d.openLocalTerminal()
					if d.nextSession != before {
						if err := d.addTerminalGroupMember(group, d.currentSession); err != nil {
							d.status = err.Error()
						}
						group.showAdd = false
					}
				}
				for _, entry := range d.sessions {
					session := entry
					if d.terminalGroupFor(session.id) != nil {
						continue
					}
					if ui.Button(c.Key(fmt.Sprintf("group-add-session-%d", session.id)), sessionDisplayLabel(session)).Label("添加终端：" + sessionDisplayLabel(session)).FillWidth().Justify(ui.Start).Disabled(d.busy || session.term == nil || terminalEnded(session.term)).Clicked() {
						if err := d.addTerminalGroupMember(group, session.id); err != nil {
							d.status = err.Error()
						}
						group.showAdd = false
					}
				}
			})
		})
	})
}

func (d *desktop) terminalTopTabs() []terminalTopTab {
	var result []terminalTopTab
	seen := map[int]bool{}
	for _, s := range d.sessions {
		g := d.terminalGroupFor(s.id)
		if g == nil {
			result = append(result, terminalTopTab{representative: s.id, sessions: []*workspaceSession{s}})
			continue
		}
		if seen[g.id] {
			continue
		}
		seen[g.id] = true
		members := d.terminalGroupSessions(g)
		if len(members) > 0 {
			result = append(result, terminalTopTab{representative: members[0].id, group: g, sessions: members})
		}
	}
	return result
}
func (d *desktop) moveTerminalTopTab(from, to int) {
	tabs := d.terminalTopTabs()
	a, b := -1, -1
	for i, tab := range tabs {
		for _, s := range tab.sessions {
			if s.id == from {
				a = i
			}
			if s.id == to {
				b = i
			}
		}
	}
	if a < 0 || b < 0 || a == b {
		return
	}
	item := tabs[a]
	if a < b {
		copy(tabs[a:b], tabs[a+1:b+1])
	} else {
		copy(tabs[b+1:a+1], tabs[b:a])
	}
	tabs[b] = item
	reordered := make([]*workspaceSession, 0, len(d.sessions))
	for _, tab := range tabs {
		reordered = append(reordered, tab.sessions...)
	}
	if len(reordered) == len(d.sessions) {
		d.sessions = reordered
	}
}

func (d *desktop) terminalGroupView(c *ui.Context, g *terminalGroup, width float32) {
	sessions := d.terminalGroupSessions(g)
	if len(sessions) == 0 {
		return
	}
	focusID, explicitFocusID, closeID, detachID, moveFrom, moveTo := 0, 0, 0, 0, 0, 0
	memberMenu := func(menu *ui.Menu, id int) {
		index := -1
		for i, member := range g.members {
			if member == id {
				index = i
				break
			}
		}
		if menu.Item("向前移动").Disabled(d.busy || index <= 0).Chosen() {
			moveFrom, moveTo = id, g.members[index-1]
		}
		if menu.Item("向后移动").Disabled(d.busy || index < 0 || index >= len(g.members)-1).Chosen() {
			moveFrom, moveTo = id, g.members[index+1]
		}
		if menu.Item("重命名终端…").Disabled(d.busy).Chosen() {
			for _, s := range sessions {
				if s.id == id {
					g.memberRenameID, g.memberRenameOpen = id, true
					g.memberRenameDraft, g.memberRenameStatus = groupSessionName(g, s), ""
					break
				}
			}
		}
		if menu.Item("移出工作区").Disabled(d.busy).Chosen() {
			detachID = id
		}
	}
	pane := func(s *workspaceSession, header bool) {
		ui.Column(c).Fill().Children(func() {
			if header {
				ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
					label := "切换到终端：" + groupSessionDisplayLabel(g, s)
					button := ui.Button(c.Key(fmt.Sprintf("group-focus-%d", s.id)), groupSessionDisplayLabel(g, s)).Label(label).BorderWidth(0).Background(c.Theme().Background).Padding(4, 8).FontSize(12).Grow(1).Justify(ui.Start).Disabled(!d.canSwitchSession()).Drag(terminalGroupMemberDrag(s.id))
					if s.id == d.currentSession {
						button = button.Selected(true).Background(c.Theme().SurfacePressed)
					}
					button.ContextMenu(func(menu *ui.Menu) { memberMenu(menu, s.id) })
					d.groupMemberRenamePopover(c, g, s, button)
					if dragged, ok := ui.Drop[terminalGroupMemberDrag](button); ok && !d.busy {
						moveFrom, moveTo = int(dragged), s.id
					}
					if button.Clicked() {
						explicitFocusID = s.id
						d.selectSession(s.id)
					}
					if dir := sessionTerminalDirectory(s); dir != "" {
						ui.Text(c, dir).FontSize(11).TextColor(c.Theme().TextMuted).MaxWidth(160).SingleLine().Tooltip(dir)
					}
					if command := sessionRunningCommand(s); command != "" {
						ui.Text(c, command).Label("运行中的命令：" + command).FontSize(11).TextColor(c.Theme().TextMuted).MaxWidth(160).SingleLine().Tooltip(command)
					}
					d.broadcastMemberButton(c, g, s)
					if ui.Button(c.Key(fmt.Sprintf("group-close-member-%d", s.id)), "×").Label("关闭终端：" + groupSessionName(g, s)).BorderWidth(0).Background(c.Theme().Surface).Width(30).Disabled(d.busy).Clicked() {
						closeID = s.id
					}
				})
			}
			view := terminal.View(c, s.term).Label("终端：" + groupSessionName(g, s)).Grow(1).FillWidth()
			d.terminalCompletionView(c, s, view)
			if d.pendingTerminalFocus == s.id {
				view.Focus()
				d.pendingTerminalFocus = 0
				focusID = s.id
			}
			if view.Focused() {
				focusID = s.id
			}
		})
	}
	if !g.grid || len(sessions) == 1 {
		current := sessions[0]
		for _, s := range sessions {
			if s.id == d.currentSession {
				current = s
				break
			}
		}
		railWidth := float32(190)
		if width < 800 {
			railWidth = 140
		}
		ui.Row(c).Fill().Children(func() {
			ui.Scroll(c.Key(fmt.Sprintf("group-rail-%d", g.id))).Label("工作区会话").Width(railWidth).FillHeight().BorderWidth(0, 1, 0, 0).BorderColor(c.Theme().Border).Children(func() {
				for _, entry := range sessions {
					s := entry
					ui.Row(c).FillWidth().Padding(2, 4).AlignItems(ui.Center).Children(func() {
						button := ui.Button(c.Key(fmt.Sprintf("group-focus-%d", s.id)), "").Label("切换到终端："+groupSessionDisplayLabel(g, s)).BorderWidth(0).Background(c.Theme().Background).Radius(5).FontSize(12).Padding(7, 8).Grow(1).Justify(ui.Start).Drag(terminalGroupMemberDrag(s.id))
						button.Children(func() {
							ui.Column(c).Gap(2).Children(func() {
								ui.Text(c, groupSessionDisplayLabel(g, s)).FontSize(12).SingleLine()
								if dir := sessionTerminalDirectory(s); dir != "" {
									ui.Text(c, dir).FontSize(10).TextColor(c.Theme().TextMuted).MaxWidth(110).SingleLine().Tooltip(dir)
								}
								if command := sessionRunningCommand(s); command != "" {
									ui.Text(c, command).Label("运行中的命令：" + command).FontSize(10).TextColor(c.Theme().TextMuted).MaxWidth(110).SingleLine().Tooltip(command)
								}
							})
						})
						if s.id == d.currentSession {
							button = button.Selected(true).Background(c.Theme().SurfacePressed)
						}
						button.ContextMenu(func(menu *ui.Menu) { memberMenu(menu, s.id) })
						d.groupMemberRenamePopover(c, g, s, button)
						if dragged, ok := ui.Drop[terminalGroupMemberDrag](button); ok && !d.busy {
							moveFrom, moveTo = int(dragged), s.id
						}
						if button.Disabled(!d.canSwitchSession()).Clicked() {
							explicitFocusID = s.id
							d.selectSession(s.id)
						}
						d.broadcastMemberButton(c, g, s)
						if ui.Button(c.Key(fmt.Sprintf("group-close-member-%d", s.id)), "×").Label("关闭终端：" + groupSessionName(g, s)).BorderWidth(0).Background(c.Theme().Background).Radius(5).Width(30).Disabled(d.busy).Clicked() {
							closeID = s.id
						}
					})
				}
			})
			ui.Column(c).Grow(1).FillHeight().Children(func() { pane(current, false) })
		})
	} else {
		columns := 2
		if len(sessions) > 4 {
			columns = 3
		}
		if len(sessions) > 9 {
			columns = 4
		}
		rows := (len(sessions) + columns - 1) / columns
		var renderRow func(int)
		renderRow = func(row int) {
			start := row * columns
			end := min(start+columns, len(sessions))
			var renderColumn func(int)
			renderColumn = func(i int) {
				if i == end-1 {
					pane(sessions[i], true)
					return
				}
				k := row*4 + (i - start)
				if g.columnPerc[k] == 0 {
					g.columnPerc[k] = 100 / float32(end-i)
				}
				index := i
				ui.SplitPercent(c.Key(fmt.Sprintf("group-column-%d-%d", g.id, k)), &g.columnPerc[k], func() { pane(sessions[index], true) }, func() { renderColumn(index + 1) }).Fill()
			}
			renderColumn(start)
		}
		var renderRows func(int)
		renderRows = func(row int) {
			if row == rows-1 {
				renderRow(row)
				return
			}
			if g.rowPerc[row] == 0 {
				g.rowPerc[row] = 100 / float32(rows-row)
			}
			index := row
			ui.SplitVerticalPercent(c.Key(fmt.Sprintf("group-row-%d-%d", g.id, row)), &g.rowPerc[row], func() { renderRow(index) }, func() { renderRows(index + 1) }).Fill()
		}
		_, height := c.Size()
		gridHeight := max(float32(rows*180), height-180)
		ui.Scroll(c.Key(fmt.Sprintf("group-grid-%d", g.id))).Label("工作区分屏").Fill().Children(func() { ui.Column(c).Height(gridHeight).FillWidth().Children(func() { renderRows(0) }) })
	}
	if moveFrom != 0 {
		moveTerminalGroupMember(g, moveFrom, moveTo)
		c.Invalidate()
	}
	if detachID != 0 {
		d.detachGroupSession(detachID)
		d.splitSession, d.splitFirstSession, d.splitFocusedSession = 0, 0, 0
		if d.currentSession == detachID {
			d.pendingTerminalFocus = detachID
		}
		d.status = "已移出工作区，终端仍保持打开"
		c.Invalidate()
		return
	}
	if closeID != 0 {
		d.closeSession(closeID)
		c.Invalidate()
		return
	}
	if explicitFocusID != 0 {
		focusID = explicitFocusID
	}
	if focusID != 0 && focusID != d.currentSession && focusID != d.splitFocusedSession && d.canSwitchSession() {
		d.activateSession(focusID)
		c.Invalidate()
	}
	if !d.canSwitchSession() && focusID != d.currentSession {
		d.splitFocusedSession = 0
	} else {
		d.splitFocusedSession = focusID
	}
}
