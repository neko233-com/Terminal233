package main

import (
	"fmt"
	"net"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"sort"
	"strings"
)

type profileID string

func workspaceSidebarWidth(width float32) float32 {
	if width < 800 {
		return 170
	}
	return 235
}

type profile struct {
	ID                   profileID
	Group, Label         string
	Tags                 []string
	Favorite             bool
	LastConnected        int64
	Settings             connectionSettings
	EncryptedCredentials string
}

func (d *desktop) addProfile() error {
	if _, err := d.settings.address(); err != nil {
		return err
	}
	tags, err := parseProfileTags(d.profileTags)
	if err != nil {
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
		d.profiles[i] = profile{ID: old.ID, Group: strings.TrimSpace(d.profileGroup), Label: label, Tags: tags, Favorite: old.Favorite, LastConnected: old.LastConnected, Settings: safe, EncryptedCredentials: old.EncryptedCredentials}
		return nil
	}
	d.nextProfileID++
	id := profileID(fmt.Sprint(d.nextProfileID))
	d.profiles = append(d.profiles, profile{ID: id, Group: strings.TrimSpace(d.profileGroup), Label: label, Tags: tags, Settings: safe})
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
	d.connectionFocusPending = false
	d.showKeyOptions = false
	d.showConnectionTools = false
	d.showProfileInspector = false
	d.profileInspectorAt = time.Time{}
	d.selectedCloudProfile = ""
	d.settingsFromCloud = false
	d.cloudFormSources = nil
	d.cloudFormVersions = nil
	d.settings = p.Settings
	d.profileSecretError = false
	if p.EncryptedCredentials != "" {
		password, passphrase, err := unprotectProfileCredentials(p.EncryptedCredentials, p.Settings)
		if err != nil {
			d.profileSecretError = true
		} else {
			d.settings.Password, d.settings.Passphrase = password, passphrase
		}
	}
	d.profileGroup = p.Group
	d.profileLabel = p.Label
	d.profileTags = strings.Join(p.Tags, ", ")
	d.expectedFingerprint = p.Settings.Fingerprint
	d.fingerprint = ""
	d.changedFingerprint = ""
	d.selectedProfile = string(p.ID)
	d.status = "已载入连接。"
	if d.profileSecretError {
		d.status = "无法解锁本机凭据；原数据已保留，请重新输入凭据。"
	}
	d.captureConnectionDraft()
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
	if !d.profileSecretError && (p.Settings.KeyPath != "" || d.settings.Password != "") && d.trustedProfileFingerprint() != "" {
		d.connectTrustedProfile()
		return
	}
	if d.trustedProfileFingerprint() != "" {
		d.status = "请输入密码或私钥口令，然后点击连接。"
	}
}

// Re-read after a navigation flush, which may have updated the selected host.
func (d *desktop) currentProfile(id profileID) (profile, bool) {
	for _, p := range d.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return profile{}, false
}

func (d *desktop) profileView(c *ui.Context) {
	width, _ := c.Size()
	inspectorCapable := d.connectionInspectorDocked(width)
	draftBlocked := d.connectionNavigationBlocked()
	docked := d.connectionInspectorDocked(width) && d.editConnection
	if !d.sidebarCollapsed {
		width -= workspaceSidebarWidth(width)
	}
	if docked {
		width -= connectionInspectorWidth + 16
	}
	compact := width < 600
	columns := 1
	if width >= 950 {
		columns = 3
	} else if width >= 500 {
		columns = 2
	}
	cardWidth := (width - 48 - float32(columns-1)*10) / float32(columns)
	ui.Column(c).Fill().Gap(10).Children(func() {
		if d.cloudBatchCancel != nil {
			ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
				ui.Text(c, "云端批量操作进行中；取消会等待当前请求结束。").Wrap()
				if ui.Button(c.Key("cloud-batch-cancel"), "取消云端批量操作").Clicked() {
					d.cloudBatchCancel()
					d.status = "正在取消；请等待当前请求和列表刷新结束。"
				}
			})
		}
		ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.End).Children(func() {
			ui.TextInput(c.Key("profile-search"), &d.profileSearch).Label("连接列表：搜索连接或快速连接").Placeholder("搜索主机或输入 SSH 地址").Grow(1).OnSubmit(func() {
				d.submitConnectionSearch()
			})
			if looksLikeQuickConnection(d.profileSearch) {
				if ui.Button(c.Key("quick-connect-open"), "填写连接信息").Disabled(d.busy || draftBlocked).Clicked() {
					d.submitConnectionSearch()
				}
			}
		})
		ui.Row(c).FillWidth().Wrap().Gap(8).Padding(0, 0, 10, 0).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("connection-new"), "+ 新建连接").Disabled(draftBlocked).Clicked() {
				if !d.prepareConnectionNavigation() {
					return
				}
				d.settings = connectionSettings{Port: "22"}
				d.profileSecretError = false
				d.showKeyOptions = false
				d.showConnectionTools = false
				d.settingsFromCloud = false
				d.cloudFormSources = nil
				d.cloudFormVersions = nil
				d.profileGroup = d.profileFilter
				d.profileLabel = ""
				d.profileTags = ""
				d.fingerprint = ""
				d.changedFingerprint = ""
				d.expectedFingerprint = ""
				d.selectedProfile = ""
				d.selectedCloudProfile = ""
				d.showProfileInspector = false
				d.profileInspectorAt = time.Time{}
				d.editConnection = true
				d.connectionFocusPending = true
				d.captureConnectionDraft()
			}
			local := ui.Button(c.Key("profile-open-local"), "").Label("本机终端").Tooltip("打开本机终端").Gap(8).Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions)
			local.Children(func() {
				ui.Icon(c, terminalIcon).Width(16).Height(16).TextColor(c.Theme().TextMuted)
				ui.Text(c, "本机终端")
			})
			if local.Clicked() {
				d.openLocalTerminal()
			}
			label := "分组管理"
			if d.showGroupControls {
				label = "收起分组管理"
			}
			if terminalActionButton(c, "group-controls-toggle", label, fileIcon, d.showGroupControls, false).Clicked() {
				d.showGroupControls = !d.showGroupControls
				d.confirmGroupDelete = false
			}
			label, accessible := "更多操作", "更多连接操作"
			if d.showCompactProfileTools {
				label, accessible = "收起操作", "收起连接操作"
			} else if d.profileSource == "local" {
				label = "本地 · 更多"
			} else if d.profileSource == "cloud" {
				label = "云端 · 更多"
			}
			var more ui.Element
			if !d.showCompactProfileTools && d.profileSource != "" {
				more = ui.Button(c.Key("profile-tools-toggle"), label).Label(accessible).Tooltip(label)
			} else {
				more = terminalActionButton(c, "profile-tools-toggle", accessible, moreIcon, d.showCompactProfileTools, false).Tooltip(label)
			}
			if more.Clicked() {
				d.showCompactProfileTools = !d.showCompactProfileTools
			}
		})
		ui.Scroll(c.Key("profile-list-scroll")).Label("主机列表").Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(10).Children(func() {
				d.profileList(c, cardWidth, compact, inspectorCapable, draftBlocked)
			})
		})
	})
}

func (d *desktop) profileList(c *ui.Context, cardWidth float32, compact, inspectorCapable, draftBlocked bool) {
	if d.profileRecent {
		ui.Text(c, "最近连接").FontSize(13).TextColor(c.Theme().TextMuted)
	} else if d.profileFavorites {
		ui.Text(c, "收藏的连接").FontSize(13).TextColor(c.Theme().TextMuted)
	}
	if d.profileFilter != "" {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("profile-group-parent"), "‹").Label("返回上级分组").Tooltip("返回上级分组").Width(36).Disabled(draftBlocked).Clicked() {
				if !d.closeConnectionForNavigation() {
					return
				}
				if i := strings.LastIndex(d.profileFilter, "/"); i >= 0 {
					d.profileFilter = d.profileFilter[:i]
				} else {
					d.profileFilter = ""
				}
				d.confirmGroupDelete, d.showProfileInspector = false, false
				d.profileInspectorAt = time.Time{}
			}
			ui.Text(c, "分组 / "+d.profileFilter).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		})
	}
	if d.profileSort != "" && !d.profileRecent {
		ui.Text(c, "按所选列排序；切换到自定义顺序后可拖动连接。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	}
	if d.status != "" && !d.editConnection {
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
	if d.showGroupControls {
		ui.Column(c).FillWidth().Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() {
			if d.profileSource == "cloud" {
				d.cloudGroupControls(c)
			} else {
				d.groupControls(c)
			}
		})
	}
	if d.showCompactProfileTools {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			if workspaceButton(c, "profile-source-all", "全部来源", d.profileSource == "").Clicked() {
				if d.profileSource == "cloud" {
					d.profileFilter = ""
				}
				d.profileSource = ""
			}
			if workspaceButton(c, "profile-source-local", "本地仓库", d.profileSource == "local").Clicked() {
				if d.profileSource == "cloud" {
					d.profileFilter = ""
				}
				d.profileSource = "local"
			}
			if workspaceButton(c, "profile-source-cloud", "云端仓库", d.profileSource == "cloud").Disabled(d.cloud == nil).Clicked() {
				d.profileSource = "cloud"
				d.profileFilter = ""
				d.profileFavorites, d.profileRecent = false, false
			}
		})
	}
	if d.showCompactProfileTools {
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
	}
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
	if query == "" && !d.profileFavorites && !d.profileRecent {
		d.profileGroupCards(c, cardWidth, draftBlocked)
	}
	showLocal := d.profileSource != "cloud" && d.profileFilter == "" && !d.profileFavorites && !d.profileRecent &&
		(query == "" || strings.Contains("本机终端 local terminal powershell", query))
	ui.Text(c, "主机").FontSize(13).TextColor(c.Theme().TextMuted)
	var moveFrom, moveTo profileID
	shown := 0
	ui.Row(c).FillWidth().Wrap().Gap(10).AlignItems(ui.Start).Children(func() {
		if showLocal {
			button := ui.Button(c.Key("profile-local-terminal"), "").Label("打开本机终端").Tooltip("打开本机终端").Width(cardWidth).Height(66).Padding(14).Gap(12).Justify(ui.Start).Background(c.Theme().Surface).BorderWidth(0).Radius(10).Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions)
			button.Children(func() {
				ui.Box(c).Width(38).Height(38).Radius(9).Background(c.Theme().Accent).AlignItems(ui.Center).Justify(ui.Center).Children(func() { ui.Icon(c, terminalIcon).Width(19).Height(19).TextColor(c.Theme().AccentText) })
				ui.Column(c).Grow(1).Gap(3).Children(func() {
					ui.Text(c, "本机终端").FontSize(14).Bold()
					ui.Text(c, "PowerShell · 此电脑").FontSize(12).TextColor(c.Theme().TextMuted)
				})
			})
			if button.Clicked() {
				d.openLocalTerminal()
			}
		}
		if d.profileSource != "cloud" && len(d.profiles) == 0 {
			ui.Column(c).Width(cardWidth).Padding(16).Gap(6).Background(c.Theme().Surface).Radius(10).Children(func() {
				ui.Text(c, "还没有本机 SSH 连接").FontSize(14).Bold()
				ui.Text(c, "新建连接后即可在这里访问。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			})
		}
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
			ui.Column(c).Width(cardWidth).Padding(10).Gap(6).Background(c.Theme().Surface).Radius(10).Children(func() {
				row := ui.Button(c.Key("profile-"+string(p.ID)), "").Label(label).BorderWidth(0).Padding(4).Gap(10).Justify(ui.Start).FillWidth()
				row.Children(func() {
					ui.Box(c).Width(38).Height(38).Radius(9).Background(c.Theme().Accent).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
						ui.Icon(c, serverIcon).Width(19).Height(19).TextColor(c.Theme().AccentText)
					})
					ui.Column(c).Grow(1).Gap(3).Children(func() {
						ui.Text(c, p.Label).FontSize(14).Bold().Wrap()
						ui.Text(c, p.Settings.User+"@"+p.Settings.Host+":"+p.Settings.Port).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
					})
				})
				if d.selectedProfile == string(p.ID) {
					row = row.Background(c.Theme().SurfacePressed).BorderWidth(1).BorderColor(c.Theme().Accent)
				}
				if !d.profileRecent && d.profileSort == "" {
					row = row.Drag(p.ID)
				}
				blockedByEditor := draftBlocked
				row.ContextMenu(func(menu *ui.Menu) {
					if menu.Item("连接").Disabled(d.busy || blockedByEditor || len(d.sessions) >= maxWorkspaceSessions).Chosen() {
						if d.prepareConnectionNavigation() {
							if current, ok := d.currentProfile(p.ID); ok {
								d.openProfileConnection(current)
							}
						}
					}
					if menu.Item("载入").Disabled(blockedByEditor).Chosen() {
						if d.prepareConnectionNavigation() {
							if current, ok := d.currentProfile(p.ID); ok {
								d.loadProfile(current)
							}
						}
					}
					if menu.Item("移除…").Disabled(blockedByEditor).Chosen() {
						if d.prepareConnectionNavigation() {
							d.confirmProfileDelete = p.ID
						}
					}
					favorite := "加入收藏"
					if p.Favorite {
						favorite = "取消收藏"
					}
					if menu.Item(favorite).Disabled(d.busy).Chosen() {
						d.toggleProfileFavorite(p.ID)
					}
					if menu.Item("复制地址").Chosen() {
						mygo.Clipboard.WriteText(net.JoinHostPort(p.Settings.Host, p.Settings.Port))
						d.status = "已复制地址：" + p.Label
					}
				})
				if row.DoubleClicked() && !blockedByEditor {
					if d.prepareConnectionNavigation() {
						if current, ok := d.currentProfile(p.ID); ok {
							d.openProfileConnection(current)
						}
					}
				} else if row.Clicked() {
					if blockedByEditor {
						d.status = "请先保存或关闭当前主机详情。"
					} else if !d.prepareConnectionNavigation() {
						// A failed save keeps the current editor and its error.
					} else if d.editConnection {
						if d.settingsFromCloud || d.selectedProfile != string(p.ID) {
							if current, ok := d.currentProfile(p.ID); ok {
								d.loadProfile(current)
							}
						}
					} else {
						d.selectedProfile = string(p.ID)
						d.selectedCloudProfile = ""
						d.confirmProfileDelete = ""
						d.scheduleProfileInspector(c)
					}
				}
				if len(p.Tags) > 0 {
					ui.Row(c).FillWidth().Wrap().Gap(4).Children(func() {
						for i, tag := range p.Tags {
							tag := tag
							if ui.Button(c.Key(fmt.Sprintf("profile-tag-%s-%d", p.ID, i)), tag).Label("搜索标签："+tag).Tooltip("搜索标签："+tag).Padding(3, 7).BorderWidth(0).Background(c.Theme().SurfaceHover).Disabled(draftBlocked).Clicked() {
								if d.closeConnectionForNavigation() {
									d.profileFilter, d.profileSearch = "", tag
									d.profileFavorites, d.profileRecent = false, false
								}
							}
						}
					})
				}
				if !d.profileRecent && d.profileSort == "" {
					if dragged, ok := ui.Drop[profileID](row); ok {
						moveFrom, moveTo = dragged, p.ID
					}
				}
				showActions := d.showCompactProfileTools || len(d.checkedProfiles) > 0 || (!inspectorCapable && d.selectedProfile == string(p.ID))
				if showActions {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
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
						favoriteLabel, favoriteAction := "☆", "加入收藏"
						if p.Favorite {
							favoriteLabel, favoriteAction = "★", "取消收藏"
						}
						if ui.Button(c.Key("profile-favorite-"+string(p.ID)), favoriteLabel).Label(favoriteAction + "：" + p.Label).Tooltip(favoriteAction).Width(36).Clicked() {
							d.toggleProfileFavorite(p.ID)
						}
						ui.Box(c).Grow(1)
						if ui.Button(c.Key("profile-connect-"+string(p.ID)), "↗").Label("连接：" + p.Label).Tooltip("连接").Width(36).Disabled(d.busy || blockedByEditor || len(d.sessions) >= maxWorkspaceSessions).Clicked() {
							if d.prepareConnectionNavigation() {
								if current, ok := d.currentProfile(p.ID); ok {
									d.openProfileConnection(current)
								}
							}
						}
						if ui.Button(c.Key("profile-edit-"+string(p.ID)), "✎").Label("编辑").Tooltip("编辑连接").Width(36).Disabled(blockedByEditor).Clicked() {
							if d.prepareConnectionNavigation() {
								if current, ok := d.currentProfile(p.ID); ok {
									d.loadProfile(current)
								}
							}
						}
						if ui.Button(c.Key("profile-copy-"+string(p.ID)), "⧉").Label("复制地址：" + p.Label).Tooltip("复制地址").Width(36).Clicked() {
							mygo.Clipboard.WriteText(net.JoinHostPort(p.Settings.Host, p.Settings.Port))
							d.status = "已复制地址：" + p.Label
						}
					})
				}
			})
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
	})
	if d.cloud != nil && d.profileSource != "local" && (d.profileFilter == "" || d.profileSource == "cloud") && !d.profileFavorites && !d.profileRecent {
		ui.Text(c, "云端连接 · "+d.cloudScopeName()).FontSize(16).Bold()
		if d.cloud.cloudGroups && d.cloudGroupsStale {
			ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "云端分组需要刷新后才能修改。").Wrap()
				if ui.Button(c.Key("cloud-groups-refresh"), "刷新云端连接和分组").Disabled(d.busy).Clicked() {
					d.cloudRefresh()
				}
			})
		}
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
		ui.Row(c).FillWidth().Wrap().Gap(10).AlignItems(ui.Start).Children(func() {
			for _, entry := range accounts {
				account := entry
				if !d.cloudAccountVisible(account) {
					continue
				}
				shown++
				label := account.Label + " · " + account.Username + "@" + account.Host
				ui.Column(c).Width(cardWidth).Padding(10).Gap(6).Background(c.Theme().Surface).Radius(10).Children(func() {
					row := ui.Button(c.Key("profile-cloud-"+account.ID), "").Label(label).FillWidth().BorderWidth(0).Padding(4).Gap(10).Justify(ui.Start).Disabled(d.busy).Drag(cloudGroupDragID(account.ID))
					row.Children(func() {
						ui.Box(c).Width(38).Height(38).Radius(9).Background(c.Theme().Accent).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
							ui.Icon(c, cloudIcon).Width(19).Height(19).TextColor(c.Theme().AccentText)
						})
						ui.Column(c).Grow(1).Gap(3).Children(func() {
							ui.Text(c, account.Label).FontSize(14).Bold().Wrap()
							ui.Text(c, account.Username+"@"+account.Host).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
						})
					})
					if d.selectedCloudProfile == account.ID {
						row = row.Background(c.Theme().SurfacePressed).BorderWidth(1).BorderColor(c.Theme().Accent)
					}
					blockedByEditor := draftBlocked
					row.ContextMenu(func(menu *ui.Menu) {
						if menu.Item("连接").Disabled(d.busy || blockedByEditor).Chosen() {
							if d.prepareConnectionNavigation() {
								d.loadCloudAccount(account)
							}
						}
					})
					if row.DoubleClicked() && !blockedByEditor {
						if d.prepareConnectionNavigation() {
							d.loadCloudAccount(account)
						}
					} else if row.Clicked() {
						if blockedByEditor {
							d.status = "请先保存或关闭当前主机详情。"
						} else if !d.prepareConnectionNavigation() {
							// A failed save keeps the current editor and its error.
						} else if d.editConnection {
							if !d.settingsFromCloud || d.selectedCloudProfile != account.ID {
								d.loadCloudAccount(account)
							}
						} else {
							d.selectedCloudProfile = account.ID
							d.selectedProfile = ""
							d.scheduleProfileInspector(c)
						}
					}
					showActions := d.showCompactProfileTools || len(d.checkedCloud) > 0 || (!inspectorCapable && d.selectedCloudProfile == account.ID)
					if showActions {
						ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
							_, checked := d.checkedCloud[account.ID]
							if ui.Checkbox(c.Key("cloud-profile-check-"+account.ID), &checked, "").Label("勾选云端连接：" + account.Label).Disabled(d.busy || d.cloudListStale).Changed() {
								d.checkCloudAccount(account, checked)
							}
							ui.Box(c).Grow(1)
							if ui.Button(c.Key("profile-cloud-connect-"+account.ID), "↗").Label("连接云端：" + account.Label).Tooltip("连接").Width(36).Disabled(d.busy || blockedByEditor).Clicked() {
								if d.prepareConnectionNavigation() {
									d.loadCloudAccount(account)
								}
							}
							if ui.Button(c.Key("profile-cloud-copy-"+account.ID), "⧉").Label("复制云端地址：" + account.Label).Tooltip("复制地址").Width(36).Clicked() {
								mygo.Clipboard.WriteText(net.JoinHostPort(account.Host, fmt.Sprint(account.Port)))
								d.status = "已复制地址：" + account.Label
							}
							if d.cloudGroupSnapshot.Assignments[account.ID] != "" && ui.Button(c.Key("profile-cloud-ungroup-"+account.ID), "−").Label("移出云端分组："+account.Label).Width(36).Disabled(d.busy || d.cloudGroupsStale || d.cloudListStale || !d.canWriteCloudScope()).Clicked() {
								d.cloudGroupOperation("move", account.ID, "")
							}
						})
					}
				})
			}
		})
	}
	if (len(d.profiles) > 0 || d.cloud != nil || query != "") && shown == 0 && !showLocal {
		ui.Text(c, "没有匹配的连接").TextColor(c.Theme().TextMuted)
	}
	if moveFrom != "" && d.prepareConnectionNavigation() {
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
	if d.profileSource == "cloud" || (d.profileRecent && p.LastConnected <= 0) || (d.profileFavorites && !p.Favorite) || (d.profileFilter != "" && !groupWithin(p.Group, d.profileFilter)) {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(d.profileSearch))
	return query == "" || strings.Contains(strings.ToLower(p.Label+" "+p.Group+" "+p.Settings.Host+" "+p.Settings.User+" "+strings.Join(p.Tags, " ")), query)
}

func (d *desktop) toggleProfileFavorite(id profileID) {
	for i := range d.profiles {
		if d.profiles[i].ID == id {
			d.profiles[i].Favorite = !d.profiles[i].Favorite
			if err := d.saveProfiles(); err != nil {
				d.status = "收藏状态已修改，但本机保存失败：" + err.Error()
			} else {
				d.status = "收藏状态已更新"
			}
			return
		}
	}
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
		p.EncryptedCredentials = ""
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
