package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type desktop struct {
	workspace                                                                               string
	editConnection                                                                          bool
	lightTheme                                                                              bool
	terminalFontSize                                                                        int
	terminalTheme                                                                           string
	splitLayout                                                                             string
	splitHorizontalPercent, splitVerticalPercent                                            float32
	terminalFilePercent                                                                     float32
	monitorCancel                                                                           context.CancelFunc
	monitorGeneration, monitorSession                                                       int
	monitorStatus                                                                           string
	monitorData                                                                             monitorDisplay
	terminalLightTheme, terminalDarkTheme                                                   *terminal.Theme
	showFilePanel                                                                           bool
	sidebarCollapsed                                                                        bool
	sessions                                                                                []*workspaceSession
	currentSession, nextSession, splitSession, splitFirstSession, splitFocusedSession       int
	pendingTerminalFocus                                                                    int
	showTeams, showAdmin, showInviteEntry                                                   bool
	teamInvites                                                                             []cloudInvite
	confirmInvite                                                                           string
	adminTeams                                                                              []cloudTeam
	adminConfirmTeam                                                                        string
	adminUsers                                                                              []cloudAdminUser
	adminTOTP, adminBackupPath, adminStatus, adminConfirmUser                               string
	adminConfirmRestore                                                                     bool
	teams                                                                                   []cloudTeam
	members                                                                                 []cloudTeamMember
	teamName, teamInvitePassword, teamInviteInput, teamInviteURL, teamStatus, confirmMember string
	teamListStale                                                                           bool
	selectedMember                                                                          string
	teamInviteHours, teamInviteUses                                                         string
	confirmCloudDelete                                                                      string
	profiles                                                                                []profile
	profileSearch, profileGroup, selectedProfile, quickConnect                              string
	confirmProfileDelete                                                                    profileID
	profileLabel                                                                            string
	profileFilter                                                                           string
	profileSource                                                                           string
	selectedCloudProfile                                                                    string
	profileFavorites                                                                        bool
	profileRecent                                                                           bool
	profileSort                                                                             string
	profileSortDescending                                                                   bool
	profilePath, preferencesPath, preferencesStatus                                         string
	profileStoreBlocked                                                                     bool
	profileStoreConflict                                                                    bool
	confirmProfileReload                                                                    bool
	profileExists                                                                           bool
	profileRevision                                                                         [32]byte
	nextProfileID                                                                           int
	shell                                                                                   *remoteShell
	fileDir, fileLocal, fileStatus, selectedFile, confirmDelete                             string
	fileFilter, fileNewDirectory, fileRename, fileSort                                      string
	fileSortDescending, fileHideDotfiles, fileFollowTerminal                                bool
	lastTerminalDir                                                                         string
	files                                                                                   []remoteFile
	fileHistory                                                                             fileHistory
	fileBatchErrors                                                                         []string
	fileBatchDetails                                                                        bool
	fileChecked                                                                             map[string]bool
	fileCheckedDir, fileBatchTarget                                                         string
	terminalSearchOpen                                                                      bool
	terminalSearchText                                                                      string
	quickOpen                                                                               bool
	quickPath, quickName, quickText, quickStatus, quickConfirm, quickFilter                 string
	quickCommands                                                                           []quickCommand
	quickEdit                                                                               *quickCommand
	expectedFingerprint, changedFingerprint                                                 string
	win                                                                                     *mygo.Window
	settings                                                                                connectionSettings
	settingsFromCloud                                                                       bool
	cloudFormSources                                                                        map[string]string
	fingerprint, status                                                                     string
	busy                                                                                    bool
	cancelTransfer                                                                          context.CancelFunc
	transferSessionID                                                                       int
	term                                                                                    *terminal.Terminal
	active                                                                                  connectionSettings
	cloud                                                                                   *cloudClient
	cloudURL, cloudUser, cloudPassword, cloudRegistration, cloudStatus                      string
	cloudRegistrationLocal, cloudListStale                                                  bool
	showArchive, archiveConfirm                                                             bool
	archivePath, archivePassword, archiveStatus                                             string
	archiveCancel                                                                           context.CancelFunc
	accounts                                                                                []cloudAccount
	forwardLocalPort, forwardHost, forwardPort, forwardStatus                               string
	updateStatus, updatePath                                                                string
	updateAvailable                                                                         *updateCandidate
	updateChecking, updateDownloading                                                       bool
	updateCancel                                                                            context.CancelFunc
}

var workspaceTheme = func() *ui.Theme {
	t := ui.DarkTheme()
	t.Background = ui.Hex("#111418")
	t.Surface = ui.Hex("#1a1f24")
	t.SurfaceHover = ui.Hex("#252c32")
	t.SurfacePressed = ui.Hex("#303940")
	t.Border = ui.Hex("#303840")
	t.Accent = ui.Hex("#26885e")
	t.AccentHover = ui.Hex("#2fa775")
	t.AccentPressed = ui.Hex("#1c6948")
	t.Focus = ui.Hex("#6ad9a4")
	return t
}()

var workspaceLightTheme = func() *ui.Theme {
	t := ui.LightTheme()
	t.Accent = ui.Hex("#197a50")
	t.AccentHover = ui.Hex("#238f60")
	t.AccentPressed = ui.Hex("#135e3e")
	t.Focus = ui.Hex("#197a50")
	return t
}()

func workspaceButton(c *ui.Context, key, label string, active bool) ui.Element {
	if active {
		return ui.PrimaryButton(c.Key(key), label)
	}
	return ui.Button(c.Key(key), label)
}

func (d *desktop) workspaceShortcuts(c *ui.Context) {
	mods := ui.Cmd | ui.Shift
	switch {
	case d.workspace == "terminal" && len(d.sessions) > 1 && d.canSwitchSession() && c.Shortcut(ui.Cmd, ui.KeyTab):
		d.selectAdjacentSession(1)
	case d.workspace == "terminal" && len(d.sessions) > 1 && d.canSwitchSession() && c.Shortcut(mods, ui.KeyTab):
		d.selectAdjacentSession(-1)
	case c.Shortcut(mods, ui.Key1):
		d.workspace, d.editConnection = "connections", false
	case c.Shortcut(mods, ui.Key2):
		d.workspace = "terminal"
	case c.Shortcut(mods, ui.Key3):
		d.workspace = "files"
		if d.term != nil && d.shell == nil && d.files == nil && !d.busy {
			d.refreshFiles()
		}
	case c.Shortcut(mods, ui.Key4):
		d.workspace = "cloud"
	case c.Shortcut(mods, ui.KeyT):
		d.openLocalTerminal()
	case c.Shortcut(mods, ui.KeyF):
		if d.term != nil {
			d.workspace, d.terminalSearchOpen = "terminal", true
		}
	}
}

func (d *desktop) inspect() {
	if d.busy {
		return
	}
	d.busy = true
	d.fingerprint = ""
	d.changedFingerprint = ""
	d.status = "正在获取服务器指纹…"
	settings := d.settings
	go func() {
		fp, err := inspectHost(settings)
		d.win.Update(func() {
			d.busy = false
			d.finishHostInspection(settings, fp, err)
		})
	}()
}

func (d *desktop) finishHostInspection(settings connectionSettings, fp string, err error) {
	if err != nil {
		d.status = fmt.Sprintf("获取指纹失败：%v", err)
		return
	}
	if d.settings.Host != settings.Host || d.settings.Port != settings.Port || d.settings.User != settings.User {
		d.status = "连接信息已改变，请重新获取指纹。"
		return
	}
	if d.expectedFingerprint != "" && d.expectedFingerprint != fp {
		d.changedFingerprint = fp
		d.status = "主机密钥已变化，已阻止连接。请通过可信渠道核实新指纹。"
		return
	}
	d.fingerprint = fp
	d.status = "请通过可信渠道核对指纹，再点击确认并连接。"
}

func (d *desktop) acceptChangedFingerprint() {
	if d.busy || d.changedFingerprint == "" {
		return
	}
	d.fingerprint, d.changedFingerprint = d.changedFingerprint, ""
	d.status = "已选择新指纹；请确认并连接。本机已保存连接会更新，云端连接需重新同步。"
}

func (d *desktop) connect() {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions || d.fingerprint == "" {
		return
	}
	d.busy = true
	d.status = "正在建立安全连接…"
	settings := d.settings
	options := d.terminalOptions()
	settings.Fingerprint = d.fingerprint
	profileID := d.selectedProfile
	var cloudSources map[string]string
	if d.settingsFromCloud && d.cloud != nil {
		cloudSources = cloneCloudSources(d.cloudFormSources)
	}
	d.settings.Password = ""
	d.settings.Passphrase = ""
	go func() {
		shell, term, err := openSSHSession(settings, options)
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.status = fmt.Sprintf("连接失败：%v", err)
				return
			}
			d.addSession(term, shell, settings, settings.User+"@"+settings.Host)
			for _, session := range d.sessions {
				if session.id == d.currentSession {
					session.cloudSources = cloudSources
					break
				}
			}
			d.refreshFiles()
			d.status = "已连接"
			for i, p := range d.profiles {
				if string(p.ID) == profileID && p.Settings.Host == settings.Host && p.Settings.Port == settings.Port && p.Settings.User == settings.User {
					d.profiles[i].Settings.Fingerprint = settings.Fingerprint
					d.profiles[i].LastConnected = time.Now().Unix()
					if err := d.saveProfiles(); err != nil {
						d.status = "已连接，但主机指纹未保存：" + err.Error()
					}
					break
				}
			}
			d.workspace = "terminal"
		})
	}()
}

func (d *desktop) view(c *ui.Context) {
	d.workspaceShortcuts(c)
	if (d.workspace == "files" || d.workspace == "terminal") && d.fileFollowTerminal {
		d.followReportedDirectory(false)
	}
	width, height := c.Size()
	compactAuxiliaryPanels := height < 650 || (width < 940 && height < 1000)
	if d.lightTheme {
		c.SetTheme(workspaceLightTheme)
	} else {
		c.SetTheme(workspaceTheme)
	}
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Padding(10, 16).Gap(12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "NekoSSH").FontSize(21).Bold()
			sidebarLabel := "收起侧栏"
			if d.sidebarCollapsed {
				sidebarLabel = "展开侧栏"
			}
			if ui.Button(c.Key("toggle-sidebar"), sidebarLabel).Clicked() {
				d.sidebarCollapsed = !d.sidebarCollapsed
				d.savePreference("sidebar", d.sidebarCollapsed)
			}
			if d.preferencesStatus != "" {
				ui.Text(c, d.preferencesStatus).FontSize(12).TextColor(t.TextMuted).Grow(1).Wrap()
			} else {
				ui.Text(c, "安全连接 · 本地与云端").FontSize(12).TextColor(t.TextMuted).Grow(1)
			}
			themeLabel := "浅色"
			if d.lightTheme {
				themeLabel = "深色"
			}
			if ui.Button(c.Key("theme-toggle"), themeLabel).Clicked() {
				d.lightTheme = !d.lightTheme
				d.savePreference("light", d.lightTheme)
			}
			ui.Button(c.Key("local-terminal"), "本机终端").Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions).OnClick(d.openLocalTerminal)
			if ui.Button(c.Key("disconnect"), "关闭当前会话").Disabled(d.busy || d.term == nil).Clicked() {
				d.closeCurrentSession()
			}
		})
		ui.Row(c).Grow(1).FillWidth().Children(func() {
			if !d.sidebarCollapsed {
				ui.Scroll(c).Width(235).FillHeight().Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
					ui.Column(c).Padding(16).Gap(8).Children(func() {
						ui.Text(c, "工作区").FontSize(12).TextColor(t.TextMuted)
						if workspaceButton(c, "nav-connections", "SSH 连接", d.workspace == "" || d.workspace == "connections").Clicked() {
							d.workspace = "connections"
							d.editConnection = false
						}
						if workspaceButton(c, "nav-terminal", "终端", d.workspace == "terminal").Clicked() {
							d.workspace = "terminal"
						}
						if workspaceButton(c, "nav-files", "远程文件", d.workspace == "files").Clicked() {
							d.workspace = "files"
							if d.term != nil && d.shell == nil && d.files == nil && !d.busy {
								d.refreshFiles()
							}
						}
						if workspaceButton(c, "nav-forward", "端口转发", d.workspace == "forward").Clicked() {
							d.workspace = "forward"
						}
						if workspaceButton(c, "nav-cloud", "云同步与团队", d.workspace == "cloud").Clicked() {
							d.workspace = "cloud"
						}
						ui.Text(c, "终端字号").FontSize(12).TextColor(t.TextMuted)
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
							if ui.Button(c.Key("font-smaller"), "A−").Label("缩小终端字号").Disabled(d.currentTerminalFontSize() <= minTerminalFontSize).Clicked() {
								d.changeTerminalFont(terminal.Zoom{Steps: -1})
							}
							ui.Text(c, fmt.Sprint(d.currentTerminalFontSize())).Grow(1)
							if ui.Button(c.Key("font-larger"), "A+").Label("放大终端字号").Disabled(d.currentTerminalFontSize() >= maxTerminalFontSize).Clicked() {
								d.changeTerminalFont(terminal.Zoom{Steps: 1})
							}
							if ui.Button(c.Key("font-reset"), "重置").Disabled(d.currentTerminalFontSize() == defaultTerminalFontSize).Clicked() {
								d.changeTerminalFont(terminal.Zoom{Reset: true})
							}
						})
						ui.Text(c, "终端配色").FontSize(12).TextColor(t.TextMuted)
						selectedTheme := terminalThemeLabel(d.terminalTheme)
						if ui.Select(c.Key("terminal-theme"), &selectedTheme, terminalThemeLabels()).Label("选择终端配色").FillWidth().Changed() {
							d.changeTerminalTheme(terminalThemeID(selectedTheme))
						}
						ui.Text(c, "终端分屏布局").FontSize(12).TextColor(t.TextMuted)
						layout := splitLayoutLabel(d.splitLayout)
						if ui.Select(c.Key("split-layout"), &layout, []string{"自动布局", "左右分屏", "上下分屏"}).Label("选择分屏布局").FillWidth().Changed() {
							d.changeSplitLayout(splitLayoutID(layout))
						}
						ui.Text(c, "连接").FontSize(12).TextColor(t.TextMuted)
						connectionCount := len(d.profiles) + 1
						if d.cloud != nil {
							connectionCount += len(d.accounts)
						}
						if ui.Button(c.Key("group-all"), fmt.Sprintf("全部连接  %d", connectionCount)).Clicked() {
							d.workspace = "connections"
							d.editConnection = false
							d.profileFilter = ""
							d.profileFavorites = false
							d.profileRecent = false
						}
						counts := make(map[string]int)
						var groups []string
						favorites := 0
						recent := 0
						for _, p := range d.profiles {
							if p.Favorite {
								favorites++
							}
							if p.LastConnected > 0 {
								recent++
							}
							if p.Group != "" {
								if counts[p.Group] == 0 {
									groups = append(groups, p.Group)
								}
								counts[p.Group]++
							}
						}
						if ui.Button(c.Key("group-favorites"), fmt.Sprintf("收藏的连接  %d", favorites)).Clicked() {
							d.workspace = "connections"
							d.editConnection = false
							d.profileFilter = ""
							d.profileFavorites = true
							d.profileRecent = false
							if d.profileSource == "cloud" {
								d.profileSource = "local"
							}
						}
						if ui.Button(c.Key("group-recent"), fmt.Sprintf("最近连接  %d", recent)).Clicked() {
							d.workspace = "connections"
							d.editConnection = false
							d.profileFilter = ""
							d.profileFavorites = false
							d.profileRecent = true
							if d.profileSource == "cloud" {
								d.profileSource = "local"
							}
						}
						for _, group := range groups {
							name := group
							if ui.Button(c.Key("group-entry-"+name), fmt.Sprintf("%s  %d", name, counts[name])).Clicked() {
								d.workspace = "connections"
								d.editConnection = false
								d.profileFilter = name
								d.profileFavorites = false
								d.profileRecent = false
								if d.profileSource == "cloud" {
									d.profileSource = "local"
								}
							}
						}
						ui.Text(c, "本机连接不保存密码或私钥口令").FontSize(12).TextColor(t.TextMuted).Wrap()
						ui.Text(c, "Ctrl+Shift+1–4 切换工作区\nCtrl+Shift+T 打开本机终端\nCtrl+Shift+F 查找终端输出\nCtrl+Tab / Ctrl+Shift+Tab 切换会话").FontSize(11).TextColor(t.TextMuted).Wrap()
						d.updateView(c)
					})
				})
			}
			if d.workspace != "terminal" && d.monitorCancel != nil {
				d.stopMonitor()
			}
			ui.Column(c).Grow(1).FillHeight().Children(func() {
				if d.workspace == "terminal" {
					closeSessionID := 0
					var moveSessionFrom, moveSessionTo sessionDragID
					ui.ScrollHorizontal(c).FillWidth().Padding(8, 12).Gap(6).Children(func() {
						for index, session := range d.sessions {
							s := session
							index := index
							ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
								tab := workspaceButton(c, fmt.Sprintf("session-%d", s.id), sessionDisplayLabel(s), s.id == d.currentSession).Disabled(!d.canSwitchSession()).Drag(sessionDragID(s.id))
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
								}
								if dragged, ok := ui.Drop[sessionDragID](tab); ok {
									moveSessionFrom, moveSessionTo = dragged, sessionDragID(s.id)
								}
								if ui.Button(c.Key(fmt.Sprintf("session-close-%d", s.id)), "×").Label("关闭会话：" + s.label).Disabled(d.busy).Clicked() {
									closeSessionID = s.id
								}
							})
						}
					})
					if moveSessionFrom != 0 {
						d.moveSession(int(moveSessionFrom), int(moveSessionTo))
						c.Invalidate()
					}
					if closeSessionID != 0 {
						d.closeSession(closeSessionID)
					}
					ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Padding(12, 18).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
						if width >= 900 {
							ui.Text(c, "终端").FontSize(17).Bold().Grow(1)
						}
						status := d.status
						if terminalEnded(d.term) && !d.busy {
							if d.shell != nil {
								status = "SSH 已断开"
							} else {
								status = "本机终端已退出"
							}
						}
						if width >= 900 {
							ui.Text(c, status).FontSize(12).TextColor(t.TextMuted)
						}
						if ui.Button(c.Key("terminal-search-toggle"), "查找").Disabled(d.term == nil).Clicked() {
							if d.terminalSearchOpen {
								d.closeTerminalSearch()
							} else {
								d.terminalSearchOpen = true
							}
						}
						quickLabel := "快速命令"
						if d.quickOpen {
							quickLabel = "收起快速命令"
						}
						if width < 900 {
							quickLabel = "命令+"
							if d.quickOpen {
								quickLabel = "命令−"
							}
						}
						if ui.Button(c.Key("quick-toggle"), quickLabel).Disabled(d.term == nil).Clicked() {
							d.quickOpen = !d.quickOpen
						}
						if d.shell != nil || d.monitorCancel != nil {
							monitorLabel := "服务器状态"
							if width < 900 {
								monitorLabel = "状态"
							}
							if d.monitorCancel != nil {
								monitorLabel = "关闭状态"
								if width < 900 {
									monitorLabel = "状态−"
								}
							}
							if ui.Button(c.Key("monitor-toggle"), monitorLabel).Disabled(d.monitorCancel == nil && terminalEnded(d.term)).Clicked() {
								d.toggleMonitor()
							}
						}
						splitLabel := "分屏查看"
						if d.splitSession != 0 {
							splitLabel = "关闭分屏"
						}
						if width < 900 {
							splitLabel = "分屏+"
							if d.splitSession != 0 {
								splitLabel = "分屏−"
							}
						}
						if ui.Button(c.Key("toggle-split"), splitLabel).Disabled(!d.canSwitchSession() || (d.splitSession == 0 && len(d.sessions) < 2)).Clicked() {
							d.toggleSplit()
						}
						label := "打开文件面板"
						if d.showFilePanel {
							label = "收起文件面板"
						}
						if width < 900 {
							label = "文件+"
							if d.showFilePanel {
								label = "文件−"
							}
						}
						if ui.Button(c.Key("toggle-file-panel"), label).Disabled(d.term == nil || !d.canSwitchSession()).Clicked() {
							d.showFilePanel = !d.showFilePanel
							d.savePreference("files", d.showFilePanel)
							if d.showFilePanel && d.shell == nil && d.files == nil {
								d.refreshFiles()
							}
						}
					})
					if d.term == nil {
						ui.Text(c, "选择连接，或打开本机终端").FontSize(18).TextColor(t.TextMuted)
					} else {
						if d.monitorCancel != nil {
							if width < 900 {
								ui.ScrollHorizontal(c).Height(20).FillWidth().Children(func() { ui.Text(c, d.monitorStatus).FontSize(12) })
							} else {
								ui.Text(c, d.monitorStatus).FontSize(12).Wrap()
							}
							if width >= 1200 && height >= 800 {
								d.monitorGauges(c)
							}
						}
						if d.quickOpen {
							if compactAuxiliaryPanels {
								ui.Scroll(c.Key("quick-compact-scroll")).Label("快速命令面板").Height(90).Shrink(0).FillWidth().Children(func() { d.quickCommandsView(c) })
							} else {
								d.quickCommandsView(c)
							}
						}
						if d.terminalSearchOpen {
							d.terminalSearchView(c)
						}
						if terminalEnded(d.term) {
							ui.Row(c).FillWidth().Padding(8, 12).Gap(10).Wrap().Children(func() {
								message := "本机终端已退出；输出仍可查看。"
								if d.shell != nil {
									message = "SSH 会话已断开；文件操作已暂停。"
								}
								ui.Text(c, message).Grow(1).Wrap()
								if d.shell != nil && ui.Button(c.Key("session-reconnect"), "重新连接").Disabled(d.busy).Clicked() {
									d.reconnectCurrentSession()
								}
							})
						}
						if d.showFilePanel && width < 940 {
							fileHeight := float32(260)
							if compactAuxiliaryPanels {
								fileHeight = 75
							}
							ui.Column(c).Grow(1).FillWidth().Children(func() {
								ui.Column(c).Grow(1).FillWidth().Children(func() { d.terminalWorkspace(c, width) })
								ui.Scroll(c).Height(fileHeight).Shrink(0).FillWidth().BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
									ui.Column(c).Padding(14).Gap(8).Children(func() { d.fileView(c, true) })
								})
							})
						} else if d.showFilePanel {
							if d.terminalFilePercent == 0 {
								d.terminalFilePercent = 60
							}
							ui.SplitPercent(c.Key("terminal-file-split"), &d.terminalFilePercent,
								func() { ui.Column(c).Fill().Children(func() { d.terminalWorkspace(c, width) }) },
								func() {
									ui.Scroll(c.Key("terminal-file-panel")).Label("终端文件面板").Fill().Children(func() {
										ui.Column(c).Padding(14).Gap(8).Children(func() { d.fileView(c, true) })
									})
								},
							).Grow(1).FillWidth()
						} else {
							ui.Column(c).Grow(1).FillWidth().Children(func() { d.terminalWorkspace(c, width) })
						}
					}
					return
				}
				ui.Scroll(c).Grow(1).FillWidth().Children(func() {
					ui.Column(c).Padding(24).Gap(10).Children(func() {
						switch d.workspace {
						case "files":
							ui.Text(c, "文件管理").FontSize(22).Bold()
							d.fileView(c, false)
						case "forward":
							d.forwardView(c)
						case "cloud":
							ui.Text(c, "云端工作区").FontSize(22).Bold()
							d.cloudView(c)
						default:
							if d.editConnection {
								d.connectionView(c)
							} else {
								d.profileView(c)
							}
						}
					})
				})
			})
		})
	})
}

func (d *desktop) connectionView(c *ui.Context) {
	ui.Text(c, "SSH 连接").FontSize(22).Bold()
	ui.Text(c, "填写连接信息，核对主机指纹后连接。可将连接保存到列表。").TextColor(c.Theme().TextMuted).Wrap()
	if ui.Button(c.Key("connection-back"), "返回连接列表").Clicked() {
		d.editConnection = false
	}
	ui.TextInput(c.Key("profile-label"), &d.profileLabel).Label("连接名称").Placeholder("连接名称（可选）")
	resetFingerprint := func() { d.fingerprint, d.expectedFingerprint, d.changedFingerprint = "", "", "" }
	ui.TextInput(c.Key("host"), &d.settings.Host).Label("主机地址").Placeholder("主机地址").OnChange(resetFingerprint)
	ui.TextInput(c.Key("port"), &d.settings.Port).Label("端口").Placeholder("端口").OnChange(resetFingerprint)
	ui.TextInput(c.Key("user"), &d.settings.User).Label("用户名").Placeholder("用户名").OnChange(resetFingerprint)
	ui.TextInput(c.Key("password"), &d.settings.Password).Label("SSH 密码").Placeholder("SSH 密码").Password()
	ui.TextInput(c.Key("key"), &d.settings.KeyPath).Label("私钥路径").Placeholder("私钥路径（可选）")
	if ui.Button(c.Key("pick-key"), "选择私钥文件…").Disabled(d.busy).Clicked() {
		d.pickLocalPath("key")
	}
	ui.TextInput(c.Key("passphrase"), &d.settings.Passphrase).Label("私钥口令").Placeholder("私钥口令（可选）").Password()
	ui.TextInput(c.Key("profile-group"), &d.profileGroup).Label("连接分组").Placeholder("分组（可选）")
	if ui.Button(c.Key("profile-add"), "保存到连接列表").Disabled(d.busy).Clicked() {
		if err := d.addProfile(); err != nil {
			d.status = err.Error()
		} else {
			if err := d.saveProfiles(); err != nil {
				d.status = "连接已添加，但本机保存失败：" + err.Error()
			} else {
				d.status = "连接已保存（不保存密码）"
			}
		}
	}
	ui.Button(c.Key("inspect"), "获取主机指纹").Disabled(d.busy).OnClick(d.inspect)
	if trusted := d.trustedProfileFingerprint(); trusted != "" {
		ui.Text(c, "本机已信任指纹："+trusted).FontSize(12).Wrap()
		ui.Text(c, "重连时仍会验证服务器指纹；若指纹变化，将在发送凭据前阻止连接。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		ui.Button(c.Key("connect-trusted"), "使用已信任指纹连接").Disabled(d.busy || d.changedFingerprint != "" || len(d.sessions) >= maxWorkspaceSessions).OnClick(d.connectTrustedProfile)
	}
	if d.changedFingerprint != "" {
		ui.Text(c, "原指纹："+d.expectedFingerprint).FontSize(12).Wrap()
		ui.Text(c, "新指纹："+d.changedFingerprint).FontSize(12).Wrap()
		ui.Text(c, "请先通过可信渠道核实新指纹；确认前不会发送认证信息。").FontSize(12).Wrap()
		ui.Button(c.Key("accept-changed-fingerprint"), "已核实，使用新指纹").Disabled(d.busy).OnClick(d.acceptChangedFingerprint)
	}
	if d.fingerprint != "" {
		ui.Text(c, d.fingerprint).FontSize(12)
	}
	if d.cloud != nil {
		ui.Text(c, "同步目标："+d.cloudScopeName()+"；团队空间的成员可访问共享连接。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		if d.cloudListStale {
			ui.Text(c, "云端列表需要先刷新后才能同步。").FontSize(12).Wrap()
		}
		if d.formCloudSource() != "" {
			ui.Text(c, "将更新已载入的云端连接；其他相同地址的连接不会被修改。").FontSize(12).Wrap()
		} else {
			ui.Text(c, "将新建云端连接；相同地址不会自动覆盖。").FontSize(12).Wrap()
		}
		ui.Button(c.Key("cloudsave-form"), "确认指纹并同步密码连接").Disabled(d.busy || !d.canWriteCloudScope() || d.cloudListStale || d.fingerprint == "" || d.settings.Password == "" || d.settings.KeyPath != "").OnClick(d.cloudSaveForm)
	}
	ui.PrimaryButton(c.Key("connect"), "确认指纹并连接").Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions || d.fingerprint == "").OnClick(d.connect)
	ui.Text(c, d.status).FontSize(13)
}

const version = "0.3.50"
const defaultCloudURL = "https://nekossh.neko233.com"

func main() {
	showVersion := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *showVersion {
		fmt.Println("Terminal233 " + version)
		return
	}
	d := &desktop{settings: connectionSettings{Port: "22"}, status: "请填写连接信息", cloudURL: defaultCloudURL, forwardLocalPort: "0", forwardHost: "127.0.0.1", teamInviteHours: "24", teamInviteUses: "1"}
	if dir, err := os.UserConfigDir(); err != nil {
		d.profileStoreBlocked = true
		d.status = "无法找到本机配置目录：" + err.Error()
	} else if err = d.loadProfiles(filepath.Join(dir, "NekoSSH", "profiles.json")); err != nil {
		d.profileStoreBlocked = true
		d.status = "读取本机连接失败（未覆盖原文件）：" + err.Error()
	}
	if dir, err := os.UserConfigDir(); err != nil {
		d.preferencesStatus = "界面设置不可用：" + err.Error()
	} else if err = d.loadPreferences(filepath.Join(dir, "NekoSSH", "preferences.json")); err != nil {
		d.preferencesStatus = "界面设置未载入，原文件未覆盖：" + err.Error()
	}
	if dir, err := os.UserConfigDir(); err != nil {
		d.quickStatus = "快速命令存储不可用：" + err.Error()
	} else {
		d.quickPath = filepath.Join(dir, "NekoSSH", "quick-commands.json")
		if d.quickCommands, err = readQuickCommands(d.quickPath); err != nil {
			d.quickStatus = "读取快速命令失败，原文件未覆盖：" + err.Error()
		}
	}
	mygo.Theme.SetSource(mygo.ThemeDark)
	mygo.App.WhenReady(func() {
		d.win = mygo.NewWindow(mygo.WindowOptions{Title: "NekoSSH", Width: 1200, Height: 760, MinWidth: 640, MinHeight: 480, Content: ui.View(d.view)})
		d.win.OnClosed(d.closeAllSessions)
		d.startUpdateCheck()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
