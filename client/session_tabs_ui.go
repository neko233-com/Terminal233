package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) sessionTabs(c *ui.Context) {
	width, _ := c.Size()
	revealKey := fmt.Sprintf("%s/%d/%d", d.workspace, d.currentSession, d.confirmTerminalGroupClose)
	reveal := revealKey != d.lastTabRevealKey || width != d.lastTabRevealWidth
	d.lastTabRevealKey, d.lastTabRevealWidth = revealKey, width
	closeSessionID, closeGroupID, disbandGroupID := 0, 0, 0
	var moveFrom, moveTo sessionDragID
	var splitFrom, splitTo sessionDragID
	var hostTarget int
	var localHost profileID
	var cloudHost cloudGroupDragID
	// MyGo accepts one drop type per element; inspect the value after accepting it.
	handleDrop := func(tab ui.Element, target int) {
		value, ok := ui.Drop[any](tab)
		if !ok {
			return
		}
		switch dragged := value.(type) {
		case sessionDragID:
			if tabDropCenter(tab) && d.canSplitTabDrop(int(dragged), target) {
				splitFrom, splitTo = dragged, sessionDragID(target)
			} else {
				moveFrom, moveTo = dragged, sessionDragID(target)
			}
		case profileID:
			localHost, hostTarget = dragged, target
		case cloudGroupDragID:
			cloudHost, hostTarget = dragged, target
		}
	}
	tabs := d.terminalTopTabs()
	for index, entry := range tabs {
		tabInfo := entry
		index := index
		active := d.workspace == "terminal" && d.terminalGroupFor(d.currentSession) == tabInfo.group
		if tabInfo.group == nil {
			s := tabInfo.sessions[0]
			active = s.id == d.currentSession && ((s.fileOnly && d.workspace == "files") || (!s.fileOnly && d.workspace == "terminal"))
		}
		container := ui.Row(c.Key(fmt.Sprintf("session-tab-surface-%d", tabInfo.representative))).Height(36).Shrink(0).Padding(2).Gap(0).Radius(8).AlignItems(ui.Center).Background(workspaceTabSurface(c, active))
		container.Children(func() {
			if g := tabInfo.group; g != nil {
				tab := workspaceTabButton(c, fmt.Sprintf("group-tab-%d", g.id), g.name, splitIcon, active).Label("工作区：" + g.name).Tooltip(g.name + " · 拖入主机或终端加入分屏；拖到边缘调整顺序").Disabled(!d.canSwitchSession()).Drag(sessionDragID(tabInfo.representative))
				tab.ContextMenu(func(menu *ui.Menu) {
					if menu.Item("向左移动").Disabled(index == 0).Chosen() {
						moveFrom, moveTo = sessionDragID(tabInfo.representative), sessionDragID(tabs[index-1].representative)
					}
					if menu.Item("向右移动").Disabled(index == len(tabs)-1).Chosen() {
						moveFrom, moveTo = sessionDragID(tabInfo.representative), sessionDragID(tabs[index+1].representative)
					}
					if menu.Item("重命名工作区…").Disabled(d.busy).Chosen() {
						g.renameDraft, g.renameStatus, g.renameOpen = g.name, "", true
					}
					if menu.Item("解散工作区").Disabled(d.busy).Chosen() {
						disbandGroupID = g.id
					}
					if menu.Item("关闭工作区…").Disabled(d.busy).Chosen() {
						closeGroupID = g.id
					}
				})
				ui.PopoverBase(c, tab, &g.renameOpen, func(panel ui.Element) {
					panel.AttachTo(tab, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
						ui.Column(c).Width(270).Padding(10).Gap(6).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
							ui.TextInput(c.Key(fmt.Sprintf("group-rename-input-%d", g.id)), &g.renameDraft).Label("工作区新名称").FillWidth().OnSubmit(func() { d.renameTerminalGroup(g) })
							if g.renameStatus != "" {
								ui.Text(c, g.renameStatus).FontSize(12).Wrap()
							}
							ui.Row(c).Gap(6).Children(func() {
								if ui.Button(c.Key(fmt.Sprintf("group-rename-save-%d", g.id)), "重命名").Disabled(d.busy).Clicked() {
									d.renameTerminalGroup(g)
								}
								if ui.Button(c.Key(fmt.Sprintf("group-rename-cancel-%d", g.id)), "取消").Clicked() {
									g.renameOpen = false
								}
							})
						})
					})
				})
				if tab.Clicked() {
					d.confirmTerminalGroupClose = 0
					d.selectSession(g.focused)
					if d.terminalGroupFor(d.currentSession) == g {
						d.workspace = "terminal"
					}
				}
				handleDrop(tab, tabInfo.representative)
				if d.workspaceGroupDirty(g) {
					if ui.Button(c.Key(fmt.Sprintf("group-save-%d", g.id)), "●").Label("保存工作区：" + g.name).Tooltip("保存工作区").Width(24).Height(28).Padding(0).BorderWidth(0).Background(ui.Transparent).TextColor(c.Theme().TextMuted).Disabled(d.busy).Clicked() {
						d.selectSession(g.focused)
						d.saveActiveWorkspaceGroup(g)
					}
				}
				closeLabel := "关闭工作区：" + g.name
				var close ui.Element
				if d.confirmTerminalGroupClose == g.id {
					closeLabel = "确认关闭工作区：" + g.name
					close = ui.Button(c.Key(fmt.Sprintf("group-close-%d", g.id)), "确认").Label(closeLabel).Height(28).Padding(4, 8).BorderWidth(0).Disabled(d.busy)
				} else {
					close = workspaceTabClose(c, fmt.Sprintf("group-close-%d", g.id), closeLabel, d.busy)
				}
				if close.Clicked() {
					closeGroupID = g.id
				}
				if d.confirmTerminalGroupClose == g.id {
					if ui.Button(c.Key(fmt.Sprintf("group-close-cancel-%d", g.id)), "取消").Label("取消关闭工作区："+g.name).Height(28).Padding(4, 8).BorderWidth(0).Background(ui.Transparent).Clicked() {
						d.confirmTerminalGroupClose = 0
					}
				}
				return
			}
			s := tabInfo.sessions[0]
			icon := terminalIcon
			if s.fileOnly {
				icon = fileIcon
			}
			tab := workspaceTabButton(c, fmt.Sprintf("session-%d", s.id), sessionDisplayLabel(s), icon, active).Label("会话标签：" + sessionDisplayLabel(s)).Tooltip(sessionDisplayLabel(s) + " · 拖入主机或终端创建分屏；拖到边缘调整顺序").Disabled(!d.canSwitchSession()).Drag(sessionDragID(s.id))
			tab.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("向左移动").Disabled(index == 0).Chosen() {
					moveFrom, moveTo = sessionDragID(s.id), sessionDragID(tabs[index-1].representative)
				}
				if menu.Item("向右移动").Disabled(index == len(tabs)-1).Chosen() {
					moveFrom, moveTo = sessionDragID(s.id), sessionDragID(tabs[index+1].representative)
				}
				if menu.Item("关闭会话").Disabled(d.busy).Chosen() {
					closeSessionID = s.id
				}
			})
			if tab.Clicked() {
				d.confirmTerminalGroupClose = 0
				d.selectSession(s.id)
			}
			handleDrop(tab, s.id)
			if workspaceTabClose(c, fmt.Sprintf("session-close-%d", s.id), "关闭会话："+s.label, d.busy).Clicked() {
				closeSessionID = s.id
			}
		})
		confirming := tabInfo.group != nil && d.confirmTerminalGroupClose == tabInfo.group.id
		if reveal && (confirming || (d.confirmTerminalGroupClose == 0 && active)) {
			container.ScrollIntoView()
		}
	}
	if d.newTabOpen {
		container := ui.Row(c.Key("new-tab-surface")).Height(36).Shrink(0).Padding(2).Gap(0).Radius(8).AlignItems(ui.Center).Background(workspaceTabSurface(c, d.workspace == "new-tab"))
		container.Children(func() {
			if workspaceTabButton(c, "new-tab-session", "新标签页", nil, d.workspace == "new-tab").Label("切换到新标签页").Clicked() {
				d.confirmTerminalGroupClose = 0
				d.workspace = "new-tab"
			}
			if workspaceTabClose(c, "new-tab-close", "关闭新标签页", false).Clicked() {
				d.closeNewTab()
			}
		})
		if d.workspace == "new-tab" && reveal && d.confirmTerminalGroupClose == 0 {
			container.ScrollIntoView()
		}
	}
	if hostTarget != 0 {
		d.startWorkspaceHostDrop(hostTarget, localHost, cloudHost)
		c.Invalidate()
	}
	if splitFrom != 0 {
		d.splitTabs(int(splitFrom), int(splitTo))
		c.Invalidate()
	}
	if moveFrom != 0 {
		d.moveSession(int(moveFrom), int(moveTo))
		c.Invalidate()
	}
	if disbandGroupID != 0 {
		d.disbandTerminalGroup(disbandGroupID)
		c.Invalidate()
	}
	if closeGroupID != 0 {
		stay := d.workspace == "new-tab"
		d.closeTerminalGroup(closeGroupID)
		if stay {
			d.workspace = "new-tab"
		}
		c.Invalidate()
	}
	if closeSessionID != 0 {
		stay := d.workspace == "new-tab"
		d.closeSession(closeSessionID)
		if stay {
			d.workspace = "new-tab"
		}
	}
}

// A drop on the middle of a terminal tab creates a split workspace; its
// edges retain tab reordering. This avoids adding extra visible controls.
func tabDropCenter(tab ui.Element) bool {
	x, _, over := tab.PointerPosition()
	width := tab.Bounds().W
	return over && width > 0 && x >= width*0.2 && x <= width*0.8
}

func (d *desktop) canSplitTabDrop(from, to int) bool {
	if d.busy || from == to || d.terminalGroupFor(from) != nil {
		return false
	}
	var source, target *workspaceSession
	for _, s := range d.sessions {
		if s.id == from {
			source = s
		}
		if s.id == to {
			target = s
		}
	}
	return source != nil && target != nil && source.term != nil && target.term != nil && !terminalEnded(source.term) && !terminalEnded(target.term)
}

func (d *desktop) splitTabs(from, to int) {
	if !d.canSplitTabDrop(from, to) {
		return
	}
	if g := d.terminalGroupFor(to); g != nil {
		if len(g.members) >= maxWorkspaceSessions {
			d.status = "工作区终端数量已达上限"
			return
		}
		if err := d.addTerminalGroupMember(g, from); err != nil {
			d.status = err.Error()
			return
		}
		g.grid = true
		d.workspace = "terminal"
		d.pendingTerminalFocus = from
		return
	}
	name := "工作区"
	used := func(candidate string) bool {
		for _, g := range d.terminalGroups {
			if strings.EqualFold(g.name, candidate) {
				return true
			}
		}
		for _, w := range d.workspaceTemplates {
			if strings.EqualFold(w.Name, candidate) {
				return true
			}
		}
		for _, w := range d.cloudWorkspaceSnapshot.Items {
			if strings.EqualFold(w.Name, candidate) {
				return true
			}
		}
		return false
	}
	for n := 2; used(name); n++ {
		name = fmt.Sprintf("工作区 %d", n)
	}
	if err := d.createTerminalGroup(name, []int{to, from}, to, true); err != nil {
		d.status = err.Error()
		return
	}
	d.selectSession(to)
	d.workspace = "terminal"
	d.pendingTerminalFocus = to
}
