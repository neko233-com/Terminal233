package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// A workspace alias never changes the original session or host profile.
func groupSessionName(g *terminalGroup, s *workspaceSession) string {
	if name := g.tabNames[s.id]; name != "" {
		return name
	}
	return s.label
}
func groupSessionDisplayLabel(g *terminalGroup, s *workspaceSession) string {
	copy := *s
	copy.label = groupSessionName(g, s)
	return sessionDisplayLabel(&copy)
}
func (d *desktop) renameGroupMember(g *terminalGroup, s *workspaceSession) {
	if d.busy || g == nil || s == nil || g.memberRenameID != s.id || d.terminalGroupFor(s.id) != g {
		return
	}
	name := strings.TrimSpace(g.memberRenameDraft)
	if name == "" || !validWorkspaceText(name, 120) {
		g.memberRenameStatus = "请输入有效名称，最多 120 字节"
		return
	}
	if name == s.label {
		delete(g.tabNames, s.id)
	} else {
		if g.tabNames == nil {
			g.tabNames = make(map[int]string)
		}
		g.tabNames[s.id] = name
	}
	g.memberRenameID, g.memberRenameOpen, g.memberRenameStatus = 0, false, ""
}
func (d *desktop) groupMemberRenamePopover(c *ui.Context, g *terminalGroup, s *workspaceSession, anchor ui.Element) {
	if g.memberRenameID != s.id {
		return
	}
	ui.PopoverBase(c, anchor, &g.memberRenameOpen, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(270).Padding(10).Gap(6).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
				ui.TextInput(c.Key(fmt.Sprintf("group-member-name-%d", s.id)), &g.memberRenameDraft).Label("终端新名称").FillWidth().OnSubmit(func() { d.renameGroupMember(g, s) })
				if g.memberRenameStatus != "" {
					ui.Text(c, g.memberRenameStatus).FontSize(12).Wrap()
				}
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c.Key(fmt.Sprintf("group-member-rename-save-%d", s.id)), "重命名").Label("保存终端名称").Disabled(d.busy).Clicked() {
						d.renameGroupMember(g, s)
					}
					if ui.Button(c.Key(fmt.Sprintf("group-member-rename-cancel-%d", s.id)), "取消").Label("取消重命名终端").Clicked() {
						g.memberRenameID, g.memberRenameOpen = 0, false
					}
				})
			})
		})
	})
}
