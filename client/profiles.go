package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"sort"
	"strings"
)

type profileID string
type profile struct {
	ID            profileID
	Group, Label  string
	Favorite      bool
	LastConnected int64
	Settings      connectionSettings
}

func (d *desktop) addProfile() error {
	if _, err := d.settings.address(); err != nil {
		return err
	}
	safe := d.settings
	safe.Password = ""
	safe.Passphrase = ""
	label := strings.TrimSpace(d.profileLabel)
	if label == "" {
		label = safe.User + "@" + safe.Host
	}
	// Inspection alone does not mean the user trusted a host key. Retain
	// a verified key only for the same endpoint or a completed session.
	safe.Fingerprint = ""
	if d.active.Host == safe.Host && d.active.Port == safe.Port && d.active.User == safe.User {
		safe.Fingerprint = d.active.Fingerprint
	}
	for i, old := range d.profiles {
		if string(old.ID) != d.selectedProfile {
			continue
		}
		if safe.Fingerprint == "" && old.Settings.Host == safe.Host && old.Settings.Port == safe.Port && old.Settings.User == safe.User {
			safe.Fingerprint = old.Settings.Fingerprint
		}
		d.profiles[i] = profile{ID: old.ID, Group: strings.TrimSpace(d.profileGroup), Label: label, Favorite: old.Favorite, LastConnected: old.LastConnected, Settings: safe}
		return nil
	}
	d.nextProfileID++
	id := profileID(fmt.Sprint(d.nextProfileID))
	d.profiles = append(d.profiles, profile{ID: id, Group: strings.TrimSpace(d.profileGroup), Label: label, Settings: safe})
	d.selectedProfile = string(id)
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
	d.workspace = "connections"
	d.editConnection = true
	d.settings = p.Settings
	d.profileGroup = p.Group
	d.profileLabel = p.Label
	d.expectedFingerprint = p.Settings.Fingerprint
	d.fingerprint = ""
	d.selectedProfile = string(p.ID)
	d.status = "已载入连接，请输入密码并核对指纹。"
}
func (d *desktop) profileView(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
		title := "连接列表"
		if d.profileRecent {
			title = "最近连接"
		} else if d.profileFavorites {
			title = "收藏的连接"
		} else if d.profileFilter != "" {
			title = "分组 / " + d.profileFilter
		}
		ui.Text(c, title).FontSize(22).Bold().Grow(1)
		if ui.PrimaryButton(c.Key("connection-new"), "+ 新建连接").Clicked() {
			d.settings = connectionSettings{Port: "22"}
			d.profileGroup = ""
			d.profileLabel = ""
			d.fingerprint = ""
			d.expectedFingerprint = ""
			d.selectedProfile = ""
			d.editConnection = true
		}
	})
	help := "搜索、分组和管理 SSH 连接。右键可移除，拖动可调整顺序。"
	if d.profileRecent {
		help = "最近连接按成功连接时间排序。"
	}
	ui.Text(c, help).TextColor(c.Theme().TextMuted).Wrap()
	if d.status != "" {
		ui.Text(c, d.status).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	}
	ui.TextInput(c.Key("profile-search"), &d.profileSearch).Label("搜索连接").Placeholder("搜索连接或分组")
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c.Key("profile-up"), "上移所选连接").Disabled(d.selectedProfile == "" || d.profileRecent).Clicked() {
			d.moveSelectedProfile(-1)
		}
		if ui.Button(c.Key("profile-down"), "下移所选连接").Disabled(d.selectedProfile == "" || d.profileRecent).Clicked() {
			d.moveSelectedProfile(1)
		}
	})
	query := strings.ToLower(strings.TrimSpace(d.profileSearch))
	if len(d.profiles) == 0 {
		ui.Column(c).FillWidth().Padding(28).Gap(10).Background(c.Theme().Surface).Radius(8).Children(func() {
			ui.Text(c, "还没有 SSH 连接").FontSize(18).Bold()
			ui.Text(c, "创建连接后，可在这里快速查找和管理。也可以打开本机终端。").TextColor(c.Theme().TextMuted).Wrap()
		})
	} else {
		ui.Row(c).FillWidth().Padding(8, 12).Gap(12).Background(c.Theme().Surface).Children(func() {
			ui.Text(c, "名称 / 分组").Grow(1).TextColor(c.Theme().TextMuted)
			ui.Text(c, "地址").Width(220).TextColor(c.Theme().TextMuted)
			ui.Text(c, "操作").Width(60).TextColor(c.Theme().TextMuted)
		})
	}
	var moveFrom, moveTo profileID
	shown := 0
	items := d.profiles
	if d.profileRecent {
		items = append([]profile(nil), d.profiles...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].LastConnected > items[j].LastConnected })
	}
	for _, item := range items {
		p := item
		if d.profileRecent && p.LastConnected <= 0 {
			continue
		}
		if d.profileFavorites && !p.Favorite {
			continue
		}
		if d.profileFilter != "" && d.profileFilter != p.Group {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(p.Label+" "+p.Group+" "+p.Settings.Host+" "+p.Settings.User), query) {
			continue
		}
		shown++
		label := p.Label
		if p.Group != "" {
			label = p.Group + " / " + label
		}
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			favoriteLabel := "☆"
			favoriteAction := "加入收藏"
			if p.Favorite {
				favoriteLabel = "★"
				favoriteAction = "取消收藏"
			}
			if ui.Button(c.Key("profile-favorite-"+string(p.ID)), favoriteLabel).Label(favoriteAction + "：" + p.Label).Width(36).Clicked() {
				for i := range d.profiles {
					if d.profiles[i].ID == p.ID {
						d.profiles[i].Favorite = !d.profiles[i].Favorite
						break
					}
				}
				if err := d.saveProfiles(); err != nil {
					d.status = "收藏状态已修改，但本机保存失败：" + err.Error()
				} else {
					d.status = "收藏状态已更新"
				}
			}
			row := ui.Button(c.Key("profile-"+string(p.ID)), label).Grow(1)
			if !d.profileRecent {
				row = row.Drag(p.ID)
			}
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
					if err := d.saveProfiles(); err != nil {
						d.status = "连接已移除，但本机保存失败：" + err.Error()
					} else {
						d.status = "连接已移除"
					}
				}
			})
			if row.Clicked() {
				d.loadProfile(p)
			}
			if !d.profileRecent {
				if dragged, ok := ui.Drop[profileID](row); ok {
					moveFrom, moveTo = dragged, p.ID
				}
			}
			ui.Text(c, p.Settings.Host+":"+p.Settings.Port).Width(220).TextColor(c.Theme().TextMuted)
			if ui.Button(c.Key("profile-edit-"+string(p.ID)), "编辑").Width(60).Clicked() {
				d.loadProfile(p)
			}
		})
	}
	if len(d.profiles) > 0 && shown == 0 {
		ui.Text(c, "没有匹配的连接").TextColor(c.Theme().TextMuted)
	}
	if moveFrom != "" {
		d.moveProfile(moveFrom, moveTo)
		if err := d.saveProfiles(); err != nil {
			d.status = "排序已调整，但本机保存失败：" + err.Error()
		} else {
			d.status = "连接顺序已调整"
		}
		c.Invalidate()
	}
}

func (d *desktop) moveSelectedProfile(offset int) {
	for i, p := range d.profiles {
		if string(p.ID) == d.selectedProfile {
			j := i + offset
			if j >= 0 && j < len(d.profiles) {
				d.moveProfile(p.ID, d.profiles[j].ID)
				if err := d.saveProfiles(); err != nil {
					d.status = "排序已调整，但本机保存失败：" + err.Error()
				} else {
					d.status = "连接顺序已调整"
				}
			}
			return
		}
	}
}
