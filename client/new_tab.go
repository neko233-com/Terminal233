package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) openNewTab() {
	if d.workspace != "new-tab" {
		d.newTabReturn = d.workspace
	}
	d.newTabOpen, d.workspace, d.terminalNavigationOpen = true, "new-tab", false
	d.newTabStatus = ""
}

func (d *desktop) closeNewTab() {
	d.newTabOpen = false
	d.newTabQuery, d.newTabStatus = "", ""
	if d.workspace != "new-tab" {
		return
	}
	d.workspace = d.newTabReturn
	if d.workspace == "" || d.workspace == "new-tab" || (d.workspace == "terminal" && d.term == nil) {
		d.workspace = "connections"
	}
	if d.workspace == "terminal" && d.currentSession != 0 {
		d.pendingTerminalFocus = d.currentSession
	}
}

func (d *desktop) newTabQuickConnect() {
	if d.busy || d.hasUnsavedConnection() || len(d.sessions) >= maxWorkspaceSessions {
		return
	}
	if _, err := parseQuickConnection(d.newTabQuery); err != nil {
		d.newTabStatus = err.Error()
		return
	}
	d.quickConnect = d.newTabQuery
	d.openQuickConnection()
	d.workspace, d.newTabOpen = "connections", false
	d.newTabQuery, d.newTabStatus = "", ""
}

func (d *desktop) newTabView(c *ui.Context) {
	query := strings.ToLower(strings.TrimSpace(d.newTabQuery))
	matches := func(text string) bool { return query == "" || strings.Contains(strings.ToLower(text), query) }
	draftBlocked := d.hasUnsavedConnection()
	openDisabled := d.busy || draftBlocked || len(d.sessions) >= maxWorkspaceSessions
	ui.Column(c).FillWidth().MaxWidth(760).Padding(28).Gap(12).Children(func() {
		ui.Text(c, "新标签页").FontSize(22).Bold()
		ui.TextInput(c.Key("new-tab-search"), &d.newTabQuery).Label("搜索主机或会话").Placeholder("搜索主机或会话，或输入 ssh 用户@主机").FillWidth().AutoFocus().OnSubmit(func() {
			if looksLikeQuickConnection(d.newTabQuery) {
				d.newTabQuickConnect()
			}
		})
		if d.newTabStatus != "" {
			ui.Text(c, d.newTabStatus).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		}
		if draftBlocked {
			ui.Text(c, "主机详情有未保存的更改；保存或关闭后可打开其他主机。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		}
		if looksLikeQuickConnection(d.newTabQuery) {
			if ui.Button(c.Key("new-tab-quick-connect"), "填写 SSH 连接信息").Disabled(openDisabled).Clicked() {
				d.newTabQuickConnect()
			}
		}
		if matches("本机终端 local terminal powershell") {
			if ui.Button(c.Key("new-tab-local"), "本机终端").Label("打开本机终端").FillWidth().Justify(ui.Start).Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions).Clicked() {
				before := d.nextSession
				d.openLocalTerminal()
				if d.nextSession != before {
					d.newTabOpen, d.newTabQuery = false, ""
				} else {
					d.newTabStatus = d.status
				}
			}
		}
		shown := 0
		for _, session := range d.sessions {
			s := session
			if !matches(s.label) {
				continue
			}
			shown++
			label := fmt.Sprintf("%s · 会话 %d", sessionDisplayLabel(s), s.id)
			if ui.Button(c.Key(fmt.Sprintf("new-tab-session-%d", s.id)), label).Label("切换到会话：" + label).FillWidth().Justify(ui.Start).Disabled(!d.canSwitchSession()).Clicked() {
				d.selectSession(s.id)
			}
		}
		for _, profile := range d.profiles {
			p := profile
			name := p.Label
			if name == "" {
				name = p.Settings.User + "@" + p.Settings.Host
			}
			if !matches(name + " " + p.Settings.Host + " " + p.Settings.User + " " + p.Group) {
				continue
			}
			shown++
			if ui.Button(c.Key("new-tab-profile-"+string(p.ID)), name+" · 本机").Label("打开主机：" + name + " · 本机").FillWidth().Justify(ui.Start).Disabled(openDisabled).Clicked() {
				d.newTabOpen, d.newTabQuery = false, ""
				d.openProfileConnection(p)
			}
		}
		if d.cloud != nil {
			for _, account := range d.accounts {
				a := account
				name := a.Label
				if name == "" {
					name = a.Username + "@" + a.Host
				}
				if !matches(name + " " + a.Host + " " + a.Username) {
					continue
				}
				shown++
				if ui.Button(c.Key("new-tab-cloud-"+a.ID), name+" · 云端").Label("打开主机：" + name + " · 云端").FillWidth().Justify(ui.Start).Disabled(openDisabled).Clicked() {
					d.newTabOpen, d.newTabQuery = false, ""
					d.loadCloudAccount(a)
				}
			}
		}
		if shown == 0 && query != "" && !matches("本机终端 local terminal powershell") && !looksLikeQuickConnection(d.newTabQuery) {
			ui.Text(c, "没有匹配的主机或会话").FontSize(12).TextColor(c.Theme().TextMuted)
		}
	})
}
