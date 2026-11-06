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
		seen[current] = true
	}
	if selected == nil {
		return nil, errors.New("跳板会话已关闭或信任已撤销，请重新选择")
	}
	return selected, nil
}

func (d *desktop) sessionJumpClient(s *workspaceSession) (*ssh.Client, error) {
	if s.jumpParent == nil {
		return nil, nil
	}
	jump, err := d.connectionJump(s.jumpParent.id)
	if err != nil || jump != s.jumpParent || jump.active.Fingerprint != s.jumpFingerprint {
		return nil, errors.New("原跳板会话不可用或指纹已改变，请重新建立连接")
	}
	return jump.shell.client, nil
}

func (d *desktop) connectionJumpPicker(c *ui.Context) {
	label := "直连"
	if d.connectionJumpID != 0 {
		label = "跳板已断开"
		if jump, err := d.connectionJump(d.connectionJumpID); err == nil {
			label = jump.label
		}
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "跳板").FontSize(12).TextColor(c.Theme().TextMuted).Width(32)
		button := ui.Button(c.Key("connection-jump-picker"), "").Label("选择 SSH 跳板会话").Tooltip("使用已连接的 SSH 会话作为临时跳板").Grow(1).Gap(6).Justify(ui.Start).Disabled(d.busy)
		button.Children(func() { ui.Text(c, label).SingleLine().Grow(1); ui.Text(c, "▾") })
		if button.Clicked() {
			d.showConnectionJumpPicker = !d.showConnectionJumpPicker
		}
		ui.PopoverBase(c, button, &d.showConnectionJumpPicker, func(panel ui.Element) {
			panel.AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
				ui.Column(c).Width(280).Padding(6).Gap(2).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
					if ui.Button(c.Key("connection-jump-direct"), "直连").Label("不使用跳板").FillWidth().BorderWidth(0).Justify(ui.Start).Clicked() {
						d.setConnectionJump(0)
					}
					ui.Scroll(c.Key("connection-jump-list")).Label("可用跳板会话").Height(180).FillWidth().Children(func() {
						for _, session := range d.sessions {
							s := session
							if _, err := d.connectionJump(s.id); err != nil || s.shell == nil {
								continue
							}
							if ui.Button(c.Key(fmt.Sprintf("connection-jump-%d", s.id)), s.label).Label("通过跳板连接：" + s.label).Tooltip(s.label).FillWidth().BorderWidth(0).Justify(ui.Start).Clicked() {
								d.setConnectionJump(s.id)
							}
						}
					})
				})
			})
		})
	})
}

func (d *desktop) setConnectionJump(id int) {
	d.connectionJumpID, d.showConnectionJumpPicker = id, false
	d.fingerprint, d.changedFingerprint = "", ""
	if id == 0 {
		d.status = "已选择直连，请核对主机指纹。"
	} else {
		d.status = "已选择临时跳板，请通过可信渠道核对目标主机指纹。"
	}
}
