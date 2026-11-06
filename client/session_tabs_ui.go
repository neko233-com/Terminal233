package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) sessionTabs(c *ui.Context) {
	closeSessionID, closeGroupID, disbandGroupID := 0, 0, 0
	var moveFrom, moveTo sessionDragID
	tabs := d.terminalTopTabs()
	for index, entry := range tabs {
		tabInfo := entry
		index := index
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			if g := tabInfo.group; g != nil {
				active := d.workspace == "terminal" && d.terminalGroupFor(d.currentSession) == g
				tab := workspaceButton(c, fmt.Sprintf("group-tab-%d", g.id), g.name, active).Label("工作区：" + g.name).Disabled(!d.canSwitchSession()).Drag(sessionDragID(tabInfo.representative))
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
				if dragged, ok := ui.Drop[sessionDragID](tab); ok {
					moveFrom, moveTo = dragged, sessionDragID(tabInfo.representative)
				}
				closeLabel, closeText := "关闭工作区："+g.name, "×"
				if d.confirmTerminalGroupClose == g.id {
					closeLabel, closeText = "确认关闭工作区："+g.name, "确认"
				}
				if ui.Button(c.Key(fmt.Sprintf("group-close-%d", g.id)), closeText).Label(closeLabel).Disabled(d.busy).Clicked() {
					closeGroupID = g.id
				}
				if d.confirmTerminalGroupClose == g.id {
					if ui.Button(c.Key(fmt.Sprintf("group-close-cancel-%d", g.id)), "取消").Label("取消关闭工作区：" + g.name).Clicked() {
						d.confirmTerminalGroupClose = 0
					}
				}
				return
			}
			s := tabInfo.sessions[0]
			active := s.id == d.currentSession && ((s.fileOnly && d.workspace == "files") || (!s.fileOnly && d.workspace == "terminal"))
			tab := workspaceButton(c, fmt.Sprintf("session-%d", s.id), sessionDisplayLabel(s), active).Disabled(!d.canSwitchSession()).Drag(sessionDragID(s.id))
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
			if dragged, ok := ui.Drop[sessionDragID](tab); ok {
				moveFrom, moveTo = dragged, sessionDragID(s.id)
			}
			if ui.Button(c.Key(fmt.Sprintf("session-close-%d", s.id)), "×").Label("关闭会话：" + s.label).Disabled(d.busy).Clicked() {
				closeSessionID = s.id
			}
		})
	}
	if d.newTabOpen {
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			if workspaceButton(c, "new-tab-session", "新标签页", d.workspace == "new-tab").Label("切换到新标签页").Clicked() {
				d.confirmTerminalGroupClose = 0
				d.workspace = "new-tab"
			}
			if ui.Button(c.Key("new-tab-close"), "×").Label("关闭新标签页").Clicked() {
				d.closeNewTab()
			}
		})
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
