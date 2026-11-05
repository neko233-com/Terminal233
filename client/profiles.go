package main

import (
	"fmt"
	"net"

	"github.com/egoist/mygo"
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
	d.settingsFromCloud = false
	d.cloudFormSources = nil
	d.cloudFormVersions = nil
	d.settings = p.Settings
	d.profileGroup = p.Group
	d.profileLabel = p.Label
	d.expectedFingerprint = p.Settings.Fingerprint
	d.fingerprint = ""
	d.changedFingerprint = ""
	d.selectedProfile = string(p.ID)
	d.status = "已载入连接，请输入密码并核对指纹。"
}

// Only a locally saved fingerprint from this exact endpoint may be reused.
// The SSH handshake still compares the server key before sending credentials.
func (d *desktop) trustedProfileFingerprint() string {
	for _, p := range d.profiles {
		if string(p.ID) == d.selectedProfile && p.Settings.Host == d.settings.Host &&
			p.Settings.Port == d.settings.Port && p.Settings.User == d.settings.User &&
			p.Settings.Fingerprint != "" && p.Settings.Fingerprint == d.expectedFingerprint {
			return p.Settings.Fingerprint
		}
	}
	return ""
}

func (d *desktop) connectTrustedProfile() {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions {
		return
	}
	if fingerprint := d.trustedProfileFingerprint(); fingerprint != "" {
		d.fingerprint = fingerprint
		d.connect()
	}
}

func (d *desktop) openProfileConnection(p profile) {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions {
		return
	}
	d.loadProfile(p)
	if p.Settings.KeyPath != "" && d.trustedProfileFingerprint() != "" {
		d.connectTrustedProfile()
		return
	}
	if d.trustedProfileFingerprint() != "" {
		d.status = "请输入密码或私钥口令，然后使用已信任指纹连接。"
	}
}

func (d *desktop) profileView(c *ui.Context) {
	width, _ := c.Size()
	if !d.sidebarCollapsed {
		width -= 235
	}
	compact := width < 600
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
			d.settingsFromCloud = false
			d.cloudFormSources = nil
			d.cloudFormVersions = nil
			d.profileGroup = ""
			d.profileLabel = ""
			d.fingerprint = ""
			d.changedFingerprint = ""
			d.expectedFingerprint = ""
			d.selectedProfile = ""
			d.editConnection = true
		}
	})
	help := "单击选择，双击连接；右键管理，拖动可调整顺序。"
	if d.profileRecent {
		help = "最近连接按成功连接时间排序。"
	} else if d.profileSort != "" {
		help = "正在按所选列排序；切换到自定义顺序后可拖动或移动连接。"
	}
	ui.Text(c, help).TextColor(c.Theme().TextMuted).Wrap()
	if d.status != "" {
		ui.Text(c, d.status).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	}
	if d.profileStoreConflict {
		label := "重新载入本机连接"
		if d.confirmProfileReload {
			label = "确认放弃本窗口未保存更改并重新载入"
		}
		if ui.Button(c.Key("profile-reload"), label).Disabled(d.busy).Clicked() {
			if !d.confirmProfileReload {
				d.confirmProfileReload = true
				d.status = "重新载入会放弃本窗口未保存的连接更改；再次点击确认。"
			} else if err := d.loadProfiles(d.profilePath); err != nil {
				d.confirmProfileReload = false
				d.status = "重新载入失败，当前连接未更改：" + err.Error()
			} else {
				d.confirmProfileReload = false
				d.selectedProfile = ""
				d.profileFilter = ""
				d.profileFavorites = false
				d.profileRecent = false
				d.status = "已重新载入本机连接"
			}
		}
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.End).Children(func() {
		ui.TextInput(c.Key("profile-search"), &d.profileSearch).Label("搜索连接或快速连接").Placeholder("搜索连接，或输入 ssh 用户@主机 -p 端口").Grow(1).OnSubmit(d.submitConnectionSearch)
		if looksLikeQuickConnection(d.profileSearch) {
			if ui.Button(c.Key("quick-connect-open"), "填写连接信息").Disabled(d.busy).Clicked() {
				d.submitConnectionSearch()
			}
		}
	})
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		if workspaceButton(c, "profile-source-all", "全部来源", d.profileSource == "").Clicked() {
			d.profileSource = ""
		}
		if workspaceButton(c, "profile-source-local", "本地仓库", d.profileSource == "local").Clicked() {
			d.profileSource = "local"
		}
		if workspaceButton(c, "profile-source-cloud", "云端仓库", d.profileSource == "cloud").Disabled(d.cloud == nil).Clicked() {
			d.profileSource = "cloud"
			d.profileFilter = ""
			d.profileFavorites, d.profileRecent = false, false
		}
	})
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		for _, column := range []struct{ key, label string }{{"name", "名称排序"}, {"host", "地址排序"}} {
			label := column.label
			if d.profileSort == column.key && !d.profileRecent {
				if d.profileSortDescending {
					label += " ↓"
				} else {
					label += " ↑"
				}
			}
			if ui.Button(c.Key("profile-sort-"+column.key), label).Disabled(d.profileRecent).Clicked() {
				if d.profileSort == column.key {
					d.profileSortDescending = !d.profileSortDescending
				} else {
					d.profileSort, d.profileSortDescending = column.key, false
				}
			}
		}
		if ui.Button(c.Key("profile-sort-custom"), "自定义顺序").Disabled(d.profileRecent || d.profileSort == "").Clicked() {
			d.profileSort, d.profileSortDescending = "", false
		}
	})
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		if ui.Button(c.Key("profile-up"), "上移所选连接").Disabled(d.profileSource == "cloud" || d.selectedProfile == "" || d.profileRecent || d.profileSort != "").Clicked() {
			d.moveSelectedProfile(-1)
		}
		if ui.Button(c.Key("profile-down"), "下移所选连接").Disabled(d.profileSource == "cloud" || d.selectedProfile == "" || d.profileRecent || d.profileSort != "").Clicked() {
			d.moveSelectedProfile(1)
		}
		if d.profileSource != "cloud" && ui.Button(c.Key("profile-batch-select-visible"), "勾选当前显示的本机连接").Disabled(d.busy || len(d.profiles) == 0).Clicked() {
			d.selectVisibleProfiles()
		}
		if d.cloud != nil && d.profileSource != "local" && ui.Button(c.Key("cloud-batch-select-visible"), "勾选当前显示的云端连接").Disabled(d.busy || d.cloudListStale).Clicked() {
			d.selectVisibleCloudAccounts()
		}
	})
	if d.profileSource != "cloud" && len(d.checkedProfiles) > 0 {
		ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("已勾选 %d 个本机连接（包括筛选隐藏项）", len(d.checkedProfiles))).Wrap()
			if ui.Button(c.Key("profile-batch-copy"), "复制选中连接").Disabled(d.busy).Clicked() {
				d.copyCheckedProfiles()
			}
			if ui.Button(c.Key("profile-batch-delete"), "移除选中连接…").Disabled(d.busy).Clicked() {
				d.confirmProfileBatchDelete = true
			}
			if ui.Button(c.Key("profile-batch-clear"), "清除勾选").Clicked() {
				d.checkedProfiles, d.confirmProfileBatchDelete = nil, false
			}
		})
		if d.confirmProfileBatchDelete {
			ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
				ui.Text(c, fmt.Sprintf("确认移除全部 %d 个已勾选的本机连接？", len(d.checkedProfiles))).Wrap()
				if ui.Button(c.Key("profile-batch-delete-confirm"), "确认批量移除").Disabled(d.busy).Clicked() {
					d.deleteCheckedProfiles()
				}
				if ui.Button(c.Key("profile-batch-delete-cancel"), "取消批量移除").Clicked() {
					d.confirmProfileBatchDelete = false
				}
			})
		}
	}
	if d.cloud != nil && d.profileSource != "local" && len(d.checkedCloud) > 0 {
		ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("已勾选 %d 个云端连接（包括筛选隐藏项）", len(d.checkedCloud))).Wrap()
			if ui.Button(c.Key("cloud-batch-copy"), "复制选中云端连接").Disabled(d.busy || d.cloudListStale || !d.canWriteCloudScope()).Clicked() {
				d.cloudBatch("copy")
			}
			if ui.Button(c.Key("cloud-batch-delete"), "删除选中云端连接…").Disabled(d.busy || d.cloudListStale || !d.canWriteCloudScope()).Clicked() {
				d.confirmCloudBatchDelete = true
			}
			if ui.Button(c.Key("cloud-batch-clear"), "清除云端勾选").Disabled(d.busy).Clicked() {
				d.clearCloudBatch()
			}
		})
		if d.confirmCloudBatchDelete {
			ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
				ui.Text(c, fmt.Sprintf("确认删除全部 %d 个已勾选的云端连接？", len(d.checkedCloud))).Wrap()
				if !d.cloud.conditionalDeletes {
					ui.Text(c, "当前服务未确认支持并发删除保护，其他设备的更改可能被删除。").Wrap()
				}
				if ui.Button(c.Key("cloud-batch-delete-confirm"), "确认批量删除云端连接").Disabled(d.busy).Clicked() {
					d.cloudBatch("delete")
				}
				if ui.Button(c.Key("cloud-batch-delete-cancel"), "取消批量删除云端连接").Clicked() {
					d.confirmCloudBatchDelete = false
				}
			})
		}
	}
	query := strings.ToLower(strings.TrimSpace(d.profileSearch))
	showLocal := d.profileSource != "cloud" && d.profileFilter == "" && !d.profileFavorites && !d.profileRecent &&
		(query == "" || strings.Contains("本机终端 local terminal powershell", query))
	if showLocal {
		ui.Column(c).FillWidth().Padding(16).Gap(6).Background(c.Theme().Surface).Radius(8).Children(func() {
			ui.Text(c, "本机终端").FontSize(16).Bold()
			ui.Text(c, "内置本机连接，通过 PowerShell 直接使用这台电脑。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			ui.Button(c.Key("profile-local-terminal"), "打开本机终端").Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions).OnClick(d.openLocalTerminal)
		})
	}
	if d.profileSource != "cloud" && len(d.profiles) == 0 {
		ui.Column(c).FillWidth().Padding(28).Gap(10).Background(c.Theme().Surface).Radius(8).Children(func() {
			ui.Text(c, "还没有本机 SSH 连接").FontSize(18).Bold()
			ui.Text(c, "创建连接后，可在这里快速查找和管理。也可以打开本机终端。").TextColor(c.Theme().TextMuted).Wrap()
		})
	} else if d.profileSource != "cloud" {
		ui.Row(c).FillWidth().Padding(8, 12).Gap(12).Background(c.Theme().Surface).Children(func() {
			ui.Text(c, "名称 / 分组").Grow(1).TextColor(c.Theme().TextMuted)
			if !compact {
				ui.Text(c, "地址").Width(220).TextColor(c.Theme().TextMuted)
			}
			if !compact {
				ui.Text(c, "操作").Width(220).TextColor(c.Theme().TextMuted)
			}
		})
	}
	var moveFrom, moveTo profileID
	shown := 0
	items := d.profiles
	if d.profileRecent {
		items = append([]profile(nil), d.profiles...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].LastConnected > items[j].LastConnected })
	} else if d.profileSort != "" {
		items = append([]profile(nil), d.profiles...)
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i].Label, items[j].Label
			if d.profileSort == "host" {
				a, b = items[i].Settings.Host, items[j].Settings.Host
			}
			a, b = strings.ToLower(a), strings.ToLower(b)
			if d.profileSortDescending {
				return a > b
			}
			return a < b
		})
	}
	for _, item := range items {
		if d.profileSource == "cloud" {
			break
		}
		p := item
		if !d.profileVisible(p) {
			continue
		}
		shown++
		label := p.Label
		if p.Group != "" {
			label = p.Group + " / " + label
		}
		if compact {
			label += " · " + p.Settings.Host + ":" + p.Settings.Port
		}
		actions := func() {
			if ui.Button(c.Key("profile-connect-"+string(p.ID)), "连接").Label("连接：" + p.Label).Width(60).Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions).Clicked() {
				d.openProfileConnection(p)
			}
			if ui.Button(c.Key("profile-edit-"+string(p.ID)), "编辑").Width(60).Clicked() {
				d.loadProfile(p)
			}
			if ui.Button(c.Key("profile-copy-"+string(p.ID)), "复制地址").Label("复制地址：" + p.Label).Clicked() {
				mygo.Clipboard.WriteText(net.JoinHostPort(p.Settings.Host, p.Settings.Port))
				d.status = "已复制地址：" + p.Label
			}
		}
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			checked := d.checkedProfiles[p.ID]
			if ui.Checkbox(c.Key("profile-check-"+string(p.ID)), &checked, "").Label("勾选本机连接：" + p.Label).Disabled(d.busy).Changed() {
				if d.checkedProfiles == nil {
					d.checkedProfiles = make(map[profileID]bool)
				}
				if checked {
					d.checkedProfiles[p.ID] = true
				} else {
					delete(d.checkedProfiles, p.ID)
				}
				d.confirmProfileBatchDelete = false
			}
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
			if d.selectedProfile == string(p.ID) {
				row = row.Background(c.Theme().Accent).TextColor(c.Theme().AccentText)
			}
			if !d.profileRecent && d.profileSort == "" {
				row = row.Drag(p.ID)
			}
			row.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("连接").Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions).Chosen() {
					d.openProfileConnection(p)
				}
				if menu.Item("载入").Chosen() {
					d.loadProfile(p)
				}
				if menu.Item("移除…").Chosen() {
					d.confirmProfileDelete = p.ID
				}
			})
			if row.DoubleClicked() {
				d.openProfileConnection(p)
			} else if row.Clicked() {
				d.selectedProfile = string(p.ID)
				d.selectedCloudProfile = ""
				d.confirmProfileDelete = ""
			}
			if !d.profileRecent && d.profileSort == "" {
				if dragged, ok := ui.Drop[profileID](row); ok {
					moveFrom, moveTo = dragged, p.ID
				}
			}
			if !compact {
				ui.Text(c, p.Settings.Host+":"+p.Settings.Port).Width(220).TextColor(c.Theme().TextMuted)
				actions()
			}
		})
		if compact {
			ui.Row(c).FillWidth().Gap(8).Children(actions)
		}
		if d.confirmProfileDelete == p.ID {
			if d.confirmProfileDelete != "" {
				var pending *profile
				for i := range d.profiles {
					if d.profiles[i].ID == d.confirmProfileDelete {
						pending = &d.profiles[i]
						break
					}
				}
				if pending == nil {
					d.confirmProfileDelete = ""
				} else {
					ui.Text(c, "确定移除连接："+pending.Label+"？").Wrap()
					ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
						if ui.Button(c.Key("profile-delete-confirm"), "确认移除连接").Disabled(d.busy).Clicked() {
							id := d.confirmProfileDelete
							d.confirmProfileDelete = ""
							delete(d.checkedProfiles, id)
							d.confirmProfileBatchDelete = false
							for i := range d.profiles {
								if d.profiles[i].ID == id {
									d.profiles = append(d.profiles[:i], d.profiles[i+1:]...)
									break
								}
							}
							if d.selectedProfile == string(id) {
								d.selectedProfile = ""
							}
							if err := d.saveProfiles(); err != nil {
								d.status = "连接已移除，但本机保存失败：" + err.Error()
							} else {
								d.status = "连接已移除"
							}
						}
						if ui.Button(c.Key("profile-delete-cancel"), "取消移除").Clicked() {
							d.confirmProfileDelete = ""
						}
					})
				}
			}
		}
	}
	if d.cloud != nil && d.profileSource != "local" && d.profileFilter == "" && !d.profileFavorites && !d.profileRecent {
		ui.Text(c, "云端连接 · "+d.cloudScopeName()).FontSize(16).Bold()
		accounts := d.accounts
		if d.profileSort != "" {
			accounts = append([]cloudAccount(nil), d.accounts...)
			sort.SliceStable(accounts, func(i, j int) bool {
				a, b := accounts[i].Label, accounts[j].Label
				if d.profileSort == "host" {
					a, b = accounts[i].Host, accounts[j].Host
				}
				a, b = strings.ToLower(a), strings.ToLower(b)
				if d.profileSortDescending {
					return a > b
				}
				return a < b
			})
		}
		for _, entry := range accounts {
			account := entry
			if !d.cloudAccountVisible(account) {
				continue
			}
			shown++
			label := account.Label + " · " + account.Username + "@" + account.Host
			ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				_, checked := d.checkedCloud[account.ID]
				if ui.Checkbox(c.Key("cloud-profile-check-"+account.ID), &checked, "").Label("勾选云端连接：" + account.Label).Disabled(d.busy || d.cloudListStale).Changed() {
					d.checkCloudAccount(account, checked)
				}
				row := ui.Button(c.Key("profile-cloud-"+account.ID), label).Grow(1).Disabled(d.busy)
				if d.selectedCloudProfile == account.ID {
					row = row.Background(c.Theme().Accent).TextColor(c.Theme().AccentText)
				}
				row.ContextMenu(func(menu *ui.Menu) {
					if menu.Item("连接").Disabled(d.busy).Chosen() {
						d.loadCloudAccount(account)
					}
				})
				if row.DoubleClicked() {
					d.loadCloudAccount(account)
				} else if row.Clicked() {
					d.selectedCloudProfile = account.ID
					d.selectedProfile = ""
				}
				if ui.Button(c.Key("profile-cloud-connect-"+account.ID), "连接").Label("连接云端：" + account.Label).Disabled(d.busy).Clicked() {
					d.loadCloudAccount(account)
				}
				if ui.Button(c.Key("profile-cloud-copy-"+account.ID), "复制地址").Label("复制云端地址：" + account.Label).Clicked() {
					mygo.Clipboard.WriteText(net.JoinHostPort(account.Host, fmt.Sprint(account.Port)))
					d.status = "已复制地址：" + account.Label
				}
			})
		}
	}
	if (len(d.profiles) > 0 || d.cloud != nil || query != "") && shown == 0 && !showLocal {
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

func (d *desktop) profileVisible(p profile) bool {
	if d.profileSource == "cloud" || (d.profileRecent && p.LastConnected <= 0) || (d.profileFavorites && !p.Favorite) || (d.profileFilter != "" && d.profileFilter != p.Group) {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(d.profileSearch))
	return query == "" || strings.Contains(strings.ToLower(p.Label+" "+p.Group+" "+p.Settings.Host+" "+p.Settings.User), query)
}

func (d *desktop) selectVisibleProfiles() {
	if d.busy {
		return
	}
	if d.checkedProfiles == nil {
		d.checkedProfiles = make(map[profileID]bool)
	}
	for _, p := range d.profiles {
		if d.profileVisible(p) {
			d.checkedProfiles[p.ID] = true
		}
	}
	d.confirmProfileBatchDelete = false
}

func (d *desktop) copyCheckedProfiles() {
	if d.busy || len(d.checkedProfiles) == 0 {
		return
	}
	d.confirmProfileBatchDelete = false
	original, next := d.profiles, d.nextProfileID
	count := 0
	for _, p := range original {
		if d.checkedProfiles[p.ID] {
			count++
		}
	}
	if count == 0 || len(original)+count > 10000 {
		d.status = "无法复制：连接不存在或复制后超过 10000 条上限。"
		return
	}
	items := append([]profile(nil), original...)
	for _, p := range original {
		if !d.checkedProfiles[p.ID] {
			continue
		}
		d.nextProfileID++
		p.ID = profileID(fmt.Sprint(d.nextProfileID))
		p.Label += "（副本）"
		p.Favorite, p.LastConnected = false, 0
		p.Settings.Password, p.Settings.Passphrase = "", ""
		items = append(items, p)
	}
	d.profiles = items
	if err := d.saveProfiles(); err != nil {
		d.profiles, d.nextProfileID = original, next
		d.status = "复制未保存，连接未更改：" + err.Error()
		return
	}
	d.status = fmt.Sprintf("已复制 %d 个本机连接", count)
	d.checkedProfiles, d.confirmProfileBatchDelete = nil, false
}

func (d *desktop) deleteCheckedProfiles() {
	if d.busy || !d.confirmProfileBatchDelete || len(d.checkedProfiles) == 0 {
		return
	}
	original := d.profiles
	items := make([]profile, 0, len(original))
	removed := 0
	for _, p := range original {
		if d.checkedProfiles[p.ID] {
			removed++
		} else {
			items = append(items, p)
		}
	}
	d.profiles = items
	if err := d.saveProfiles(); err != nil {
		d.profiles = original
		d.confirmProfileBatchDelete = false
		d.status = "移除未保存，连接未更改：" + err.Error()
		return
	}
	if d.checkedProfiles[profileID(d.selectedProfile)] {
		d.selectedProfile = ""
	}
	d.confirmProfileDelete = ""
	d.checkedProfiles, d.confirmProfileBatchDelete = nil, false
	d.status = fmt.Sprintf("已移除 %d 个本机连接", removed)
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
