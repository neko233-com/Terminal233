package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// File panes use the same session objects as terminals. Switching here keeps
// the user in the current workspace while preserving each session's directory.
func (d *desktop) selectFileSession(id int) {
	if !d.canSwitchSession() {
		return
	}
	workspace := d.workspace
	d.activateSession(id)
	if d.currentSession != id {
		return
	}
	d.workspace = workspace
	d.showFileSessionPicker = false
	if workspace == "files" {
		d.narrowFilePane = "session"
	}
	if d.files == nil && d.fileStatus == "" && !d.busy && !d.fileSessionUnavailable() && d.win != nil {
		d.refreshFiles()
	}
}

func (d *desktop) fileSessionPicker(c *ui.Context) {
	query := strings.ToLower(strings.TrimSpace(d.fileSessionSearch))
	draftBlocked := d.connectionNavigationBlocked()
	matches := func(text string) bool { return query == "" || strings.Contains(strings.ToLower(text), query) }
	itemCount := len(d.sessions) + len(d.profiles)
	if d.cloud != nil {
		itemCount += len(d.accounts)
	}
	listHeight := float32(itemCount * 38)
	if listHeight < 42 {
		listHeight = 42
	} else if listHeight > 180 {
		listHeight = 180
	}
	ui.Column(c).Width(320).Padding(10).Gap(8).Background(c.Theme().SurfaceHover).Border(1, c.Theme().Border).Radius(8).Children(func() {
		ui.TextInput(c.Key("file-session-search"), &d.fileSessionSearch).Label("搜索文件会话或主机").Placeholder("搜索会话或主机").FillWidth()
		ui.Scroll(c.Key("file-session-picker-list")).Label("文件会话与主机列表").Height(listHeight).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				shown := 0
				for _, session := range d.sessions {
					s := session
					if !matches(s.label) {
						continue
					}
					shown++
					label := fmt.Sprintf("%s · 会话 %d", sessionDisplayLabel(s), s.id)
					button := ui.Button(c.Key(fmt.Sprintf("file-picker-session-%d", s.id)), label).Label("文件会话：" + label).FillWidth().Justify(ui.Start).Disabled(!d.canSwitchSession() || !s.fileAvailable())
					if s.id == d.currentSession {
						button = button.Background(c.Theme().SurfacePressed)
					}
					if button.Clicked() {
						d.selectFileSession(s.id)
					}
				}
				for _, profile := range d.profiles {
					p := profile
					name := p.Label
					if name == "" {
						name = p.Settings.User + "@" + p.Settings.Host
					}
					if !matches(name + " " + p.Settings.Host + " " + p.Group) {
						continue
					}
					shown++
					if ui.Button(c.Key("file-picker-profile-"+string(p.ID)), name+" · 本机").Label("文件主机：" + name + " · 本机").FillWidth().Justify(ui.Start).Disabled(d.busy || draftBlocked || d.fileLimitReached()).Clicked() {
						if !d.prepareConnectionNavigation() {
							d.fileStatus = d.status
							return
						}
						current, ok := d.currentProfile(p.ID)
						if !ok {
							return
						}
						d.showFileSessionPicker, d.fileReturnAfterConnect = false, true
						d.openProfileConnection(current)
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
						if ui.Button(c.Key("file-picker-cloud-"+a.ID), name+" · 云端").Label("文件主机：" + name + " · 云端").FillWidth().Justify(ui.Start).Disabled(d.busy || draftBlocked || d.fileLimitReached()).Clicked() {
							if !d.prepareConnectionNavigation() {
								d.fileStatus = d.status
								return
							}
							d.showFileSessionPicker, d.fileReturnAfterConnect = false, true
							d.loadCloudAccount(a)
						}
					}
				}
				if shown == 0 {
					ui.Text(c, "没有匹配的会话或主机").FontSize(12).TextColor(c.Theme().TextMuted)
				}
			})
		})
		if draftBlocked {
			ui.Text(c, "请先保存或关闭未完成的主机详情。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		}
		if ui.Button(c.Key("file-picker-hosts"), "选择其他 SSH 连接").Clicked() {
			d.showFileSessionPicker, d.fileReturnAfterConnect = false, true
			d.workspace = "connections"
		}
	})
}
