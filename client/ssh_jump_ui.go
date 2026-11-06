package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
)

// The desktop borrows an existing verified SSH session as a jump. Closing
// the jump disconnects descendants; it never silently falls back to direct.
func (d *desktop) connectionJump(id int) (*workspaceSession, error) {
	if id == 0 {
		return nil, nil
	}
	var selected *workspaceSession
	for _, s := range d.sessions {
		if s.id == id {
			selected = s
			break
		}
	}
	seen := make(map[*workspaceSession]bool)
	for current := selected; current != nil; current = current.jumpParent {
		present := false
		for _, s := range d.sessions {
			if s == current {
				present = true
				break
			}
		}
		if !present || seen[current] || current.shell == nil || !current.fileAvailable() || !strings.HasPrefix(current.active.Fingerprint, "SHA256:") || (current.jumpParent != nil && current.jumpParent.active.Fingerprint != current.jumpFingerprint) {
			return nil, errors.New("跳板会话已关闭或信任已撤销，请重新选择")
		}
		if current.jumpParent == nil {
			if err := d.validateSessionSavedRoute(current.active); err != nil {
				return nil, err
			}
		}
		seen[current] = true
	}
	if selected == nil {
		return nil, errors.New("跳板会话已关闭或信任已撤销，请重新选择")
	}
	return selected, nil
}

func (d *desktop) sessionJumpClient(s *workspaceSession) (*ssh.Client, error) {
	if s.jumpParent == nil {
		if err := d.validateSessionSavedRoute(s.active); err != nil {
			return nil, err
		}
		return nil, nil
	}
	jump, err := d.connectionJump(s.jumpParent.id)
	if err != nil || jump != s.jumpParent || jump.active.Fingerprint != s.jumpFingerprint {
		return nil, errors.New("原跳板会话不可用或指纹已改变，请重新建立连接")
	}
	return jump.shell.client, nil
}

func (d *desktop) connectionJumpPicker(c *ui.Context, markMetadataChanged func()) {
	label := "直连"
	if d.settings.JumpID != "" {
		label = "已保存的跳板不可用"
		if p, ok := d.forwardRuleHost(d.settings.JumpID); ok {
			label = p.Label
			if label == "" {
				label = p.Settings.User + "@" + p.Settings.Host
			}
		}
	}
	if d.settings.cloudJumpID != "" {
		label = "云端跳板不可用"
		for _, account := range d.accounts {
			if account.ID == d.settings.cloudJumpID {
				label = account.Label
				if label == "" {
					label = account.Username + "@" + account.Host
				}
				break
			}
		}
	}
	if d.connectionJumpID != 0 {
		label = "跳板已断开"
		if jump, err := d.connectionJump(d.connectionJumpID); err == nil {
			label = jump.label
		}
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "跳板").FontSize(12).TextColor(c.Theme().TextMuted).Width(32)
		button := ui.Button(c.Key("connection-jump-picker"), "").Label("选择 SSH 跳板").Tooltip("选择保存的跳板主机，或使用当前会话作为临时跳板").Grow(1).Gap(6).Justify(ui.Start).Disabled(d.busy)
		button.Children(func() { ui.Text(c, label).SingleLine().Grow(1); ui.Text(c, "▾") })
		if button.Clicked() {
			d.showConnectionJumpPicker = !d.showConnectionJumpPicker
		}
		ui.PopoverBase(c, button, &d.showConnectionJumpPicker, func(panel ui.Element) {
			panel.AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
				ui.Column(c).Width(280).Padding(6).Gap(2).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
					if ui.Button(c.Key("connection-jump-direct"), "直连").Label("不使用跳板").FillWidth().BorderWidth(0).Justify(ui.Start).Clicked() {
						if d.settingsFromCloud && d.cloud != nil && d.cloud.cloudJumps {
							d.setCloudJump("", markMetadataChanged)
						} else {
							d.setSavedConnectionJump("", markMetadataChanged)
						}
					}
					ui.Scroll(c.Key("connection-jump-list")).Label("可用跳板会话").Height(180).FillWidth().Children(func() {
						if !d.settingsFromCloud {
							for _, item := range d.profiles {
								p := item
								if string(p.ID) == d.selectedProfile {
									continue
								}
								name := p.Label
								if name == "" {
									name = p.Settings.User + "@" + p.Settings.Host
								}
								if ui.Button(c.Key("connection-jump-profile-"+string(p.ID)), "主机 · "+name).Label("保存跳板主机：" + name).Tooltip(p.Settings.Host).FillWidth().BorderWidth(0).Justify(ui.Start).Clicked() {
									id, err := d.ensureForwardHostID(p.ID)
									if err != nil {
										d.status = "无法选择跳板：" + err.Error()
									} else {
										d.setSavedConnectionJump(id, markMetadataChanged)
									}
								}
							}
						} else if d.cloud != nil && d.cloud.cloudJumps {
							for _, entry := range d.accounts {
								a := entry
								if a.ID == d.selectedCloudProfile {
									continue
								}
								name := a.Label
								if name == "" {
									name = a.Username + "@" + a.Host
								}
								if ui.Button(c.Key("connection-cloud-jump-"+a.ID), "云主机 · "+name).Label("选择云端跳板：" + name).Tooltip(a.Host).FillWidth().BorderWidth(0).Justify(ui.Start).Clicked() {
									d.setCloudJump(a.ID, markMetadataChanged)
								}
							}
						}
						for _, session := range d.sessions {
							s := session
							if _, err := d.connectionJump(s.id); err != nil || s.shell == nil {
								continue
							}
							if ui.Button(c.Key(fmt.Sprintf("connection-jump-%d", s.id)), "会话 · "+s.label).Label("通过临时跳板连接：" + s.label).Tooltip(s.label).FillWidth().BorderWidth(0).Justify(ui.Start).Clicked() {
								d.setConnectionJump(s.id)
							}
						}
					})
				})
			})
		})
	})
}

func (d *desktop) setSavedConnectionJump(id string, markMetadataChanged func()) {
	if err := d.validateSavedJumpChoice(id); err != nil {
		d.status = "无法选择跳板：" + err.Error()
		return
	}
	changed := d.settings.JumpID != id || d.settings.cloudJumpID != ""
	d.settings.JumpID, d.settings.cloudJumpID, d.settings.route = id, "", nil
	d.settings.cloudRouteURL, d.settings.cloudRouteUser, d.settings.cloudRouteTeam = "", "", ""
	d.setConnectionJump(0)
	if changed && markMetadataChanged != nil {
		markMetadataChanged()
	}
	if id != "" {
		d.status = "已选择保存的跳板；连接前会逐跳校验指纹。"
	}
}

func (d *desktop) setConnectionJump(id int) {
	d.connectionJumpID, d.showConnectionJumpPicker = id, false
	d.fingerprint, d.changedFingerprint = "", ""
	if id == 0 {
		d.status = "已选择主机连接路径，请核对主机指纹。"
	} else {
		d.status = "已选择临时跳板，请通过可信渠道核对目标主机指纹。"
	}
}
