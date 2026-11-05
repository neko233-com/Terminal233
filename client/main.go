package main

import (
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
	showFilePanel                                                                           bool
	sidebarCollapsed                                                                        bool
	sessions                                                                                []*workspaceSession
	currentSession, nextSession                                                             int
	showTeams, showAdmin                                                                    bool
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
	teamInviteHours, teamInviteUses                                                         string
	confirmCloudDelete                                                                      string
	profiles                                                                                []profile
	profileSearch, profileGroup, selectedProfile                                            string
	confirmProfileDelete                                                                    profileID
	profileLabel                                                                            string
	profileFilter                                                                           string
	profileFavorites                                                                        bool
	profileRecent                                                                           bool
	profileSort                                                                             string
	profileSortDescending                                                                   bool
	profilePath                                                                             string
	profileStoreBlocked                                                                     bool
	profileStoreConflict                                                                    bool
	confirmProfileReload                                                                    bool
	profileExists                                                                           bool
	profileRevision                                                                         [32]byte
	nextProfileID                                                                           int
	shell                                                                                   *remoteShell
	fileDir, fileLocal, fileStatus, selectedFile, confirmDelete                             string
	fileFilter                                                                              string
	files                                                                                   []remoteFile
	expectedFingerprint                                                                     string
	win                                                                                     *mygo.Window
	settings                                                                                connectionSettings
	fingerprint, status                                                                     string
	busy                                                                                    bool
	term                                                                                    *terminal.Terminal
	active                                                                                  connectionSettings
	cloud                                                                                   *cloudClient
	cloudURL, cloudUser, cloudPassword, cloudRegistration, cloudStatus                      string
	accounts                                                                                []cloudAccount
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
	}
}

func (d *desktop) inspect() {
	if d.busy {
		return
	}
	d.busy = true
	d.fingerprint = ""
	d.status = "正在获取服务器指纹…"
	settings := d.settings
	go func() {
		fp, err := inspectHost(settings)
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.status = fmt.Sprintf("获取指纹失败：%v", err)
				return
			}
			if d.settings.Host != settings.Host || d.settings.Port != settings.Port || d.settings.User != settings.User {
				d.status = "连接信息已改变，请重新获取指纹。"
				return
			}
			if d.expectedFingerprint != "" && d.expectedFingerprint != fp {
				d.status = "主机密钥已变化，已阻止连接。请通过可信渠道核实。"
				return
			}
			d.fingerprint = fp
			d.status = "请通过可信渠道核对指纹，再点击确认并连接。"
		})
	}()
}

func (d *desktop) connect() {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions || d.fingerprint == "" {
		return
	}
	d.busy = true
	d.status = "正在建立安全连接…"
	settings := d.settings
	settings.Fingerprint = d.fingerprint
	profileID := d.selectedProfile
	d.settings.Password = ""
	d.settings.Passphrase = ""
	go func() {
		shell, err := connectShell(settings)
		var term *terminal.Terminal
		if err == nil {
			term, err = terminal.New(terminal.Options{Conn: shell, NoClipboardWrite: true, ImageLimit: -1})
			if err != nil {
				_ = shell.Close()
			}
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.status = fmt.Sprintf("连接失败：%v", err)
				return
			}
			d.addSession(term, shell, settings, settings.User+"@"+settings.Host)
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
	width, _ := c.Size()
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
			}
			ui.Text(c, "安全连接 · 本地与云端").FontSize(12).TextColor(t.TextMuted).Grow(1)
			themeLabel := "浅色"
			if d.lightTheme {
				themeLabel = "深色"
			}
			if ui.Button(c.Key("theme-toggle"), themeLabel).Clicked() {
				d.lightTheme = !d.lightTheme
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
						if workspaceButton(c, "nav-cloud", "云同步与团队", d.workspace == "cloud").Clicked() {
							d.workspace = "cloud"
						}
						ui.Text(c, "连接").FontSize(12).TextColor(t.TextMuted)
						if ui.Button(c.Key("group-all"), fmt.Sprintf("全部连接  %d", len(d.profiles)+1)).Clicked() {
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
						}
						if ui.Button(c.Key("group-recent"), fmt.Sprintf("最近连接  %d", recent)).Clicked() {
							d.workspace = "connections"
							d.editConnection = false
							d.profileFilter = ""
							d.profileFavorites = false
							d.profileRecent = true
						}
						for _, group := range groups {
							name := group
							if ui.Button(c.Key("group-entry-"+name), fmt.Sprintf("%s  %d", name, counts[name])).Clicked() {
								d.workspace = "connections"
								d.editConnection = false
								d.profileFilter = name
								d.profileFavorites = false
								d.profileRecent = false
							}
						}
						ui.Text(c, "本机连接不保存密码或私钥口令").FontSize(12).TextColor(t.TextMuted).Wrap()
						ui.Text(c, "Ctrl+Shift+1–4 切换工作区\nCtrl+Shift+T 打开本机终端").FontSize(11).TextColor(t.TextMuted).Wrap()
					})
				})
			}
			ui.Column(c).Grow(1).FillHeight().Children(func() {
				if d.workspace == "terminal" {
					closeSessionID := 0
					ui.ScrollHorizontal(c).FillWidth().Padding(8, 12).Gap(6).Children(func() {
						for _, session := range d.sessions {
							s := session
							ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
								if workspaceButton(c, fmt.Sprintf("session-%d", s.id), s.label, s.id == d.currentSession).Disabled(d.busy).Clicked() {
									d.activateSession(s.id)
								}
								if ui.Button(c.Key(fmt.Sprintf("session-close-%d", s.id)), "×").Label("关闭会话：" + s.label).Disabled(d.busy).Clicked() {
									closeSessionID = s.id
								}
							})
						}
					})
					if closeSessionID != 0 {
						d.closeSession(closeSessionID)
					}
					ui.Row(c).FillWidth().Padding(12, 18).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
						ui.Text(c, "终端").FontSize(17).Bold().Grow(1)
						ui.Text(c, d.status).FontSize(12).TextColor(t.TextMuted)
						label := "打开文件面板"
						if d.showFilePanel {
							label = "收起文件面板"
						}
						if ui.Button(c.Key("toggle-file-panel"), label).Disabled(d.term == nil || d.busy).Clicked() {
							d.showFilePanel = !d.showFilePanel
							if d.showFilePanel && d.shell == nil && d.files == nil {
								d.refreshFiles()
							}
						}
					})
					if d.term == nil {
						ui.Text(c, "选择连接，或打开本机终端").FontSize(18).TextColor(t.TextMuted)
					} else {
						if d.showFilePanel && width < 940 {
							ui.Column(c).Grow(1).FillWidth().Children(func() {
								ui.Column(c).Grow(1).FillWidth().Children(func() { terminal.View(c, d.term).Fill() })
								ui.Scroll(c).Height(260).Shrink(0).FillWidth().BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
									ui.Column(c).Padding(14).Gap(8).Children(func() { d.fileView(c, true) })
								})
							})
						} else {
							ui.Row(c).Grow(1).FillWidth().AlignItems(ui.Stretch).Children(func() {
								ui.Column(c).Grow(1).FillHeight().Children(func() { terminal.View(c, d.term).Fill() })
								if d.showFilePanel {
									ui.Scroll(c).Width(380).Shrink(0).FillHeight().BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
										ui.Column(c).Padding(14).Gap(8).Children(func() { d.fileView(c, true) })
									})
								}
							})
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
	ui.TextInput(c.Key("host"), &d.settings.Host).Label("主机地址").Placeholder("主机地址").OnChange(func() { d.fingerprint = ""; d.expectedFingerprint = "" })
	ui.TextInput(c.Key("port"), &d.settings.Port).Label("端口").Placeholder("端口").OnChange(func() { d.fingerprint = ""; d.expectedFingerprint = "" })
	ui.TextInput(c.Key("user"), &d.settings.User).Label("用户名").Placeholder("用户名").OnChange(func() { d.fingerprint = ""; d.expectedFingerprint = "" })
	ui.TextInput(c.Key("password"), &d.settings.Password).Label("SSH 密码").Placeholder("SSH 密码").Password()
	ui.TextInput(c.Key("key"), &d.settings.KeyPath).Label("私钥路径").Placeholder("私钥路径（可选）")
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
	if d.fingerprint != "" {
		ui.Text(c, d.fingerprint).FontSize(12)
	}
	ui.PrimaryButton(c.Key("connect"), "确认指纹并连接").Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions || d.fingerprint == "").OnClick(d.connect)
	ui.Text(c, d.status).FontSize(13)
}

const version = "0.3.1"

func main() {
	showVersion := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *showVersion {
		fmt.Println("Terminal233 " + version)
		return
	}
	d := &desktop{settings: connectionSettings{Port: "22"}, status: "请填写连接信息", cloudURL: "https://nekossh.neko233.com", teamInviteHours: "24", teamInviteUses: "1"}
	if dir, err := os.UserConfigDir(); err != nil {
		d.profileStoreBlocked = true
		d.status = "无法找到本机配置目录：" + err.Error()
	} else if err = d.loadProfiles(filepath.Join(dir, "NekoSSH", "profiles.json")); err != nil {
		d.profileStoreBlocked = true
		d.status = "读取本机连接失败（未覆盖原文件）：" + err.Error()
	}
	mygo.Theme.SetSource(mygo.ThemeDark)
	mygo.App.WhenReady(func() {
		d.win = mygo.NewWindow(mygo.WindowOptions{Title: "NekoSSH", Width: 1200, Height: 760, MinWidth: 640, MinHeight: 480, Content: ui.View(d.view)})
		d.win.OnClosed(d.closeAllSessions)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
