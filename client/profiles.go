package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"strings"
)

type profileID string
type profile struct {
	ID           profileID
	Group, Label string
	Settings     connectionSettings
}

func (d *desktop) addProfile() error {
	if _, err := d.settings.address(); err != nil {
		return err
	}
	d.nextProfileID++
	safe := d.settings
	safe.Password = ""
	safe.Passphrase = ""
	safe.Fingerprint = d.fingerprint
	d.profiles = append(d.profiles, profile{ID: profileID(fmt.Sprint(d.nextProfileID)), Group: strings.TrimSpace(d.profileGroup), Label: safe.User + "@" + safe.Host, Settings: safe})
	return nil
}
func (d *desktop) moveProfile(from, to profileID) {
	a, b := -1, -1
	for i, p := range d.profiles {
		if p.ID == from {
			a = i
		}
		if p.ID == to {
			b = i
		}
	}
	if a < 0 || b < 0 || a == b {
		return
	}
	item := d.profiles[a]
	if a < b {
		copy(d.profiles[a:b], d.profiles[a+1:b+1])
	} else {
		copy(d.profiles[b+1:a+1], d.profiles[b:a])
	}
	d.profiles[b] = item
}
func (d *desktop) loadProfile(p profile) {
	d.settings = p.Settings
	d.expectedFingerprint = p.Settings.Fingerprint
	d.fingerprint = ""
	d.selectedProfile = string(p.ID)
	d.status = "已载入连接，请输入密码并核对指纹。"
}
func (d *desktop) profileView(c *ui.Context) {
	ui.Text(c, "连接列表（仅本次运行）").Bold()
	ui.TextInput(c.Key("profile-search"), &d.profileSearch).Label("搜索连接").Placeholder("搜索连接或分组")
	ui.TextInput(c.Key("profile-group"), &d.profileGroup).Label("连接分组").Placeholder("分组（可选）")
	if ui.Button(c.Key("profile-add"), "添加当前连接").Disabled(d.busy).Clicked() {
		if err := d.addProfile(); err != nil {
			d.status = err.Error()
		} else {
			d.status = "连接已添加（不保存密码）"
		}
	}
	if ui.Button(c.Key("profile-up"), "上移所选连接").Disabled(d.selectedProfile == "").Clicked() {
		d.moveSelectedProfile(-1)
	}
	if ui.Button(c.Key("profile-down"), "下移所选连接").Disabled(d.selectedProfile == "").Clicked() {
		d.moveSelectedProfile(1)
	}
	query := strings.ToLower(strings.TrimSpace(d.profileSearch))
	var moveFrom, moveTo profileID
	for _, item := range d.profiles {
		p := item
		if query != "" && !strings.Contains(strings.ToLower(p.Label+" "+p.Group), query) {
			continue
		}
		label := p.Label
		if p.Group != "" {
			label = p.Group + " / " + label
		}
		row := ui.Button(c.Key("profile-"+string(p.ID)), label).Drag(p.ID)
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("载入").Chosen() {
				d.loadProfile(p)
			}
			if menu.Item("移除").Chosen() {
				for i, current := range d.profiles {
					if current.ID == p.ID {
						d.profiles = append(d.profiles[:i], d.profiles[i+1:]...)
						break
					}
				}
				d.status = "连接已移除"
			}
		})
		if row.Clicked() {
			d.loadProfile(p)
		}
		if dragged, ok := ui.Drop[profileID](row); ok {
			moveFrom, moveTo = dragged, p.ID
		}
	}
	if moveFrom != "" {
		d.moveProfile(moveFrom, moveTo)
		d.status = "连接顺序已调整"
		c.Invalidate()
	}
}

func (d *desktop) moveSelectedProfile(offset int) {
	for i, p := range d.profiles {
		if string(p.ID) == d.selectedProfile {
			j := i + offset
			if j >= 0 && j < len(d.profiles) {
				d.moveProfile(p.ID, d.profiles[j].ID)
				d.status = "连接顺序已调整"
			}
			return
		}
	}
}
