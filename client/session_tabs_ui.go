package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) sessionTabs(c *ui.Context) {
	closeSessionID := 0
	var moveSessionFrom, moveSessionTo sessionDragID
	for index, session := range d.sessions {
		s := session
		index := index
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			tab := workspaceButton(c, fmt.Sprintf("session-%d", s.id), sessionDisplayLabel(s), d.workspace == "terminal" && s.id == d.currentSession).Disabled(!d.canSwitchSession()).Drag(sessionDragID(s.id))
			tab.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("向左移动").Disabled(index == 0).Chosen() {
					moveSessionFrom, moveSessionTo = sessionDragID(s.id), sessionDragID(d.neighborSession(s.id, -1))
				}
				if menu.Item("向右移动").Disabled(index == len(d.sessions)-1).Chosen() {
					moveSessionFrom, moveSessionTo = sessionDragID(s.id), sessionDragID(d.neighborSession(s.id, 1))
				}
				if menu.Item("关闭会话").Disabled(d.busy).Chosen() {
					closeSessionID = s.id
				}
			})
			if tab.Clicked() {
				d.selectSession(s.id)
				if d.currentSession == s.id {
					d.workspace = "terminal"
				}
			}
			if dragged, ok := ui.Drop[sessionDragID](tab); ok {
				moveSessionFrom, moveSessionTo = dragged, sessionDragID(s.id)
			}
			if ui.Button(c.Key(fmt.Sprintf("session-close-%d", s.id)), "×").Label("关闭会话：" + s.label).Disabled(d.busy).Clicked() {
				closeSessionID = s.id
			}
		})
	}
	if d.newTabOpen {
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			if workspaceButton(c, "new-tab-session", "新标签页", d.workspace == "new-tab").Label("切换到新标签页").Clicked() {
				d.workspace = "new-tab"
			}
			if ui.Button(c.Key("new-tab-close"), "×").Label("关闭新标签页").Clicked() {
				d.closeNewTab()
			}
		})
	}
	if moveSessionFrom != 0 {
		d.moveSession(int(moveSessionFrom), int(moveSessionTo))
		c.Invalidate()
	}
	if closeSessionID != 0 {
		stayOnNewTab := d.workspace == "new-tab"
		d.closeSession(closeSessionID)
		if stayOnNewTab {
			d.workspace = "new-tab"
		}
	}
}
