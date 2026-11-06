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
	newTabOpen                                                                              bool
	newTabQuery, newTabReturn, newTabStatus                                                 string
	newTabSelected, newTabSelectionQuery                                                    string
	editConnection                                                                          bool
	connectionFocusPending                                                                  bool
	showKeyOptions                                                                          bool
	showConnectionTools                                                                     bool
	profileSecretError                                                                      bool
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
	showFileTools                                                                           bool
	showFileFilter, showFileOptions                                                         bool
	showFileSessionPicker, fileReturnAfterConnect                                           bool
	showSecondaryFilePicker                                                                 bool
	secondaryPane                                                                           secondaryRemotePane
	secondaryFileSearch                                                                     string
	secondaryConnectPending                                                                 bool
	secondaryPreviousID                                                                     int
	secondaryPreviousShell                                                                  *remoteShell
	secondaryPreviousTerm                                                                   *terminal.Terminal
	fileSessionSearch                                                                       string
	sidebarCollapsed                                                                        bool
	terminalNavigationOpen                                                                  bool
	sessions                                                                                []*workspaceSession
	terminalGroups                                                                          []*terminalGroup
	nextTerminalGroupID, confirmTerminalGroupClose                                          int
	terminalGroupSelected                                                                   map[int]bool
	showTerminalGroupSelection                                                              bool
	showWorkspaceHostSelection                                                              bool
	workspaceHostSelected                                                                   map[string]bool
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
	confirmCloudDeleteVersion                                                               string
	profiles                                                                                []profile
	profileGroups                                                                           []string
	foldedGroups                                                                            map[string]bool
	cloudFoldedGroups                                                                       map[string]bool
	profileFormat                                                                           int
	showGroupControls, showSettings                                                         bool
	showShortcutHelp                                                                        bool
	showConnectionNavigation                                                                bool
	showCompactProfileTools                                                                 bool
	groupInput                                                                              string
	confirmGroupDelete                                                                      bool
	profileSearch, profileGroup, profileTags, selectedProfile, quickConnect                 string
	profileTagQuery                                                                         string
	profileTagFilters                                                                       []string
	showProfileTagFilter                                                                    bool
	groupConnectFailures                                                                    []string
	showGroupConnectFailures                                                                bool
	showProfileInspector                                                                    bool
	profileInspectorAt                                                                      time.Time
	connectionDraft                                                                         connectionDraft
	connectionDraftSet, confirmDiscardConnection                                            bool
	profileAutoDirty                                                                        bool
	profileAutoAt                                                                           time.Time
	confirmProfileDelete                                                                    profileID
	checkedProfiles                                                                         map[profileID]bool
	confirmProfileBatchDelete                                                               bool
	checkedCloud                                                                            map[string]cloudAccount
	confirmCloudBatchDelete                                                                 bool
	cloudGroupSnapshot                                                                      cloudGroupSnapshot
	cloudGroupsStale                                                                        bool
	cloudBatchCancel                                                                        context.CancelFunc
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
	filePaneSelection                                                                       paneSelection
	localPane                                                                               localFilePane
	leftPaneAction                                                                          leftPaneAction
	externalEditorPath                                                                      string
	externalEdits                                                                           []*externalEditJob
	nextExternalEditID                                                                      int
	showExternalEdits                                                                       bool
	narrowFilePane                                                                          string
	fileBatchErrors                                                                         []string
	fileBatchDetails                                                                        bool
	fileChecked                                                                             map[string]bool
	fileCheckedDir, fileBatchTarget                                                         string
	terminalSearchOpen                                                                      bool
	terminalSearchText                                                                      string
	quickOpen                                                                               bool
	quickEditorOpen                                                                         bool
	quickPath, quickName, quickText, quickStatus, quickConfirm, quickFilter, quickSelected  string
	quickPackageID, quickPackageFilter, quickPackageName                                    string
	quickPackageManaging, quickPackageConfirm                                               bool
	quickBatch                                                                              bool
	quickMoveTarget                                                                         string
	quickChecked                                                                            map[string]quickCommand
	quickCommands                                                                           []quickCommand
	quickPackages                                                                           []quickPackage
	quickEdit                                                                               *quickCommand
	quickMulti                                                                              *quickMultiDraft
	expectedFingerprint, changedFingerprint                                                 string
	win                                                                                     *mygo.Window
	settings                                                                                connectionSettings
	settingsFromCloud                                                                       bool
	workspaceTemplatesPath, workspaceTemplateName, workspaceTemplatesStatus                 string
	workspaceTemplates                                                                      []savedWorkspace
	cloudWorkspaceSnapshot                                                                  cloudWorkspaceSnapshot
	cloudWorkspacesStale                                                                    bool
	confirmCloudWorkspaceRemove                                                             string
	showWorkspaceTemplates                                                                  bool
	confirmWorkspaceRemove                                                                  string
	workspaceRestore                                                                        *workspaceRestore
	cloudFormSources                                                                        map[string]string
	cloudFormVersions                                                                       map[string]string
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
	t.Background = ui.Hex("#101325")
	t.Surface = ui.Hex("#20243a")
	t.SurfaceHover = ui.Hex("#2a304a")
	t.SurfacePressed = ui.Hex("#343b59")
	t.Border = ui.Hex("#353c55")
	t.Accent = ui.Hex("#087bb9")
	t.AccentHover = ui.Hex("#1592d1")
	t.AccentPressed = ui.Hex("#075f90")
	t.Focus = ui.Hex("#5bc5ff")
	return t
}()

var workspaceLightTheme = func() *ui.Theme {
	t := ui.LightTheme()
	t.Accent = ui.Hex("#0874aa")
	t.AccentHover = ui.Hex("#0988c6")
	t.AccentPressed = ui.Hex("#075b87")
	t.Focus = ui.Hex("#0874aa")
	return t
}()

func workspaceButton(c *ui.Context, key, label string, active bool) ui.Element {
	if active {
		return ui.PrimaryButton(c.Key(key), label)
	}
	return ui.Button(c.Key(key), label)
}

func navigationButton(c *ui.Context, key, label string, active bool) ui.Element {
	button := ui.Button(c.Key(key), "").Label(label).BorderWidth(0).Justify(ui.Start).Padding(8, 12).Gap(10).Radius(7)
	if active {
		button = button.Background(c.Theme().SurfacePressed)
	}
	button.Children(func() {
		if icon := navigationIcons[key]; icon != nil {
			ui.Icon(c, icon).Width(16).Height(16)
		}
		ui.Text(c, label).FontSize(13)
	})
	return button
}

func terminalActionButton(c *ui.Context, key, label string, icon *ui.SVG, active, disabled bool) ui.Element {
	button := ui.Button(c.Key(key), "").Label(label).Tooltip(label).Width(36).Height(36).Padding(8).BorderWidth(0).Radius(7).Disabled(disabled)
	color := c.Theme().TextMuted
	if active {
		button = button.Background(c.Theme().SurfacePressed)
		color = c.Theme().AccentHover
	}
	button.Children(func() { ui.Icon(c, icon).Width(18).Height(18).TextColor(color) })
	return button
}

func (d *desktop) sidebarVisible() bool {
	if (d.workspace == "terminal" && d.term != nil) || d.workspace == "new-tab" {
		return d.terminalNavigationOpen
	}
	return !d.sidebarCollapsed
}

func (d *desktop) toggleQuickCommands() {
	d.quickOpen = !d.quickOpen
	if d.quickOpen && d.showFilePanel {
		d.showFilePanel = false
		d.savePreference("files", false)
	}
}

func (d *desktop) workspaceShortcuts(c *ui.Context) {
	mods := ui.Cmd | ui.Shift
	switch {
	case d.workspace == "terminal" && len(d.sessions) > 1 && d.canSwitchSession() && c.Shortcut(ui.Cmd, ui.KeyTab):
		d.selectAdjacentSession(1)
	case d.workspace == "terminal" && len(d.sessions) > 1 && d.canSwitchSession() && c.Shortcut(mods, ui.KeyTab):
		d.selectAdjacentSession(-1)
	case c.Shortcut(mods, ui.Key1):
		d.workspace = "connections"
		if d.editConnection {
			d.closeConnectionEditor()
		}
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
	case c.Shortcut(ui.Cmd, ui.KeyT):
		d.openNewTab()
	case c.Shortcut(mods, ui.KeyF):
		if d.term != nil {
			d.workspace, d.terminalSearchOpen = "terminal", true
		}
	case d.workspace == "terminal" && d.term != nil && c.Shortcut(ui.Cmd, ui.KeyPeriod):
		d.toggleQuickCommands()
	}
}

func (d *desktop) inspect() {
	if d.busy {
		return
	}
	if d.profileAutoDirty || !d.profileAutoAt.IsZero() {
		d.flushProfileAutosave()
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
	if d.profileAutoDirty || !d.profileAutoAt.IsZero() {
		d.flushProfileAutosave()
	}
	d.busy = true
	d.status = "正在建立安全连接…"
	settings := d.settings
	returnToFiles := d.fileReturnAfterConnect && d.workspace == "connections"
	options := d.terminalOptions()
	settings.Fingerprint = d.fingerprint
	profileID := d.selectedProfile
	var cloudSources, cloudVersions map[string]string
	cloudOriginURL, cloudOriginUser, cloudOriginTeam, cloudOriginID := "", "", "", ""
	if d.settingsFromCloud && d.cloud != nil {
		if d.cloud.base != nil {
			cloudOriginURL = d.cloud.base.String()
		}
		cloudOriginUser, cloudOriginTeam, cloudOriginID = d.cloudUser, d.cloud.teamID, d.selectedCloudProfile
		cloudSources = cloneCloudSources(d.cloudFormSources)
		cloudVersions = cloneCloudSources(d.cloudFormVersions)
	}
	d.settings.Password = ""
	d.settings.Passphrase = ""
	go func() {
		shell, term, err := openSSHSession(settings, options)
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.settings.Password, d.settings.Passphrase = settings.Password, settings.Passphrase
				d.status = fmt.Sprintf("连接失败：%v", err)
				return
			}
			showFiles := returnToFiles && d.fileReturnAfterConnect && d.workspace == "connections"
			showSecondary := showFiles && d.secondaryConnectPending
			previousID, previousShell, previousTerm := d.secondaryPreviousID, d.secondaryPreviousShell, d.secondaryPreviousTerm
			d.fileReturnAfterConnect = false
			d.clearSecondaryConnectIntent()
			d.addSession(term, shell, settings, settings.User+"@"+settings.Host)
			newSessionID := d.currentSession
			d.editConnection = false
			d.connectionDraft, d.connectionDraftSet, d.confirmDiscardConnection = connectionDraft{}, false, false
			for _, session := range d.sessions {
				if session.id == d.currentSession {
					session.cloudSources = cloudSources
					session.cloudVersions = cloudVersions
					session.sourceProfileID = profileID
					session.cloudOriginURL, session.cloudOriginUser, session.cloudOriginTeam, session.cloudOriginID = cloudOriginURL, cloudOriginUser, cloudOriginTeam, cloudOriginID
					break
				}
			}
			if showSecondary {
				refreshPrevious := false
				for _, previous := range d.sessions {
					if previous.id == previousID && previous.shell == previousShell && previous.term == previousTerm && previous.term != nil && !terminalEnded(previous.term) {
						d.activateSessionWithRefresh(previousID, false)
						refreshPrevious, previous.refreshPending = previous.refreshPending, false
						break
					}
				}
				d.selectSecondarySession(newSessionID)
				if d.currentSession == newSessionID || refreshPrevious || d.files == nil {
					d.refreshFilesAfter(d.fileStatus)
				}
			} else {
				d.refreshFiles()
			}
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
			if showFiles {
				d.workspace, d.narrowFilePane = "files", "session"
				if showSecondary {
					d.narrowFilePane = "local"
				}
			}
		})
	}()
}

func (d *desktop) view(c *ui.Context) {
	d.serviceProfileAutosave(c)
	d.workspaceShortcuts(c)
	if d.workspace != "terminal" && d.workspace != "new-tab" {
		d.terminalNavigationOpen = false
	}
	if d.workspace != "connections" && d.workspace != "" {
		d.fileReturnAfterConnect = false
		d.clearSecondaryConnectIntent()
	}
	if !d.profileInspectorAt.IsZero() && !d.editConnection && (d.workspace == "" || d.workspace == "connections") {
		if delay := time.Until(d.profileInspectorAt); delay > 0 {
			c.After(delay)
		} else {
			d.showProfileInspector = true
			d.profileInspectorAt = time.Time{}
		}
	}
	if d.showProfileInspector && !d.editConnection && (d.workspace == "" || d.workspace == "connections") {
		width, _ := c.Size()
		if d.connectionInspectorDocked(width) {
			if p := d.inspectedProfile(); p != nil {
				d.loadProfile(*p)
			} else if account := d.inspectedCloudAccount(); account != nil {
				d.loadCloudAccount(*account)
			}
		}
	}
	draftBlocked := d.connectionNavigationBlocked()
	if (d.workspace == "files" || d.workspace == "terminal") && d.fileFollowTerminal {
		d.followReportedDirectory(false)
	}
	width, height := c.Size()
	if d.lightTheme {
		c.SetTheme(workspaceLightTheme)
	} else {
		c.SetTheme(workspaceTheme)
	}
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Padding(10, 16).Gap(12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "NekoSSH").FontSize(20).Bold()
			sidebarLabel := "收起侧栏"
			if !d.sidebarVisible() {
				sidebarLabel = "展开侧栏"
			}
			if ui.Button(c.Key("toggle-sidebar"), "☰").Label(sidebarLabel).Tooltip(sidebarLabel).Width(38).Clicked() {
				if (d.workspace == "terminal" && d.term != nil) || d.workspace == "new-tab" {
					d.terminalNavigationOpen = !d.terminalNavigationOpen
				} else {
					d.sidebarCollapsed = !d.sidebarCollapsed
					d.savePreference("sidebar", d.sidebarCollapsed)
				}
			}
			if d.preferencesStatus != "" {
				ui.Text(c, d.preferencesStatus).FontSize(12).TextColor(t.TextMuted).Wrap()
			}
			if len(d.sessions) > 0 || d.newTabOpen {
				if workspaceButton(c, "workspace-hosts-tab", "主机", d.workspace == "" || d.workspace == "connections").Clicked() {
					d.workspace = "connections"
				}
			}
			if width >= 1100 && (len(d.sessions) > 0 || d.newTabOpen) {
				ui.ScrollHorizontal(c).Grow(1).Gap(6).Children(func() { d.sessionTabs(c) })
			} else {
				ui.Box(c).Grow(1)
			}
			if d.workspaceRestore != nil && d.workspace != "new-tab" {
				label := "取消恢复"
				if d.workspaceRestore.groupQuick {
					label = "取消连接"
				}
				if ui.Button(c.Key("workspace-restore-cancel-header"), label).Disabled(d.workspaceRestore.cancelling).Clicked() {
					d.cancelWorkspaceRestore()
				}
			}
			themeLabel := "浅色"
			if d.lightTheme {
				themeLabel = "深色"
			}
			if ui.Button(c.Key("theme-toggle"), "◐").Label(themeLabel).Tooltip("切换" + themeLabel + "主题").Width(38).Clicked() {
				d.lightTheme = !d.lightTheme
				d.savePreference("light", d.lightTheme)
			}
			settingsLabel := d.settingsButtonLabel()
			settingsButton := terminalActionButton(c, "settings-toggle", settingsLabel, settingsIcon, d.showSettings || d.updateAvailable != nil, false)
			if settingsButton.Clicked() {
				d.showSettings = !d.showSettings
			}
			ui.PopoverBase(c, settingsButton, &d.showSettings, func(panel ui.Element) {
				panel.AttachTo(settingsButton, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(4, 0, 0, 0).Children(func() { d.settingsView(c, height) })
			})
			terminalActionButton(c, "local-terminal", "新建标签页", plusIcon, d.workspace == "new-tab", d.busy && !d.canSwitchSession()).OnClick(d.openNewTab)
			closeLabel, closeDisabled := "关闭当前会话", d.busy || d.term == nil
			if d.workspace == "new-tab" {
				closeLabel, closeDisabled = "关闭新标签页", false
			}
			if terminalActionButton(c, "disconnect", closeLabel, closeIcon, false, closeDisabled).Clicked() {
				if d.workspace == "new-tab" {
					d.closeNewTab()
				} else {
					d.closeCurrentSession()
				}
			}
		})
		showSidebar := d.sidebarVisible()
		workspaceWidth := width
		if showSidebar {
			workspaceWidth -= workspaceSidebarWidth(width)
		}
		compactAuxiliaryPanels := height < 650 || (workspaceWidth < 940 && height < 1000)
		ui.Row(c).Grow(1).FillWidth().Children(func() {
			if showSidebar {
				ui.Scroll(c).Width(workspaceSidebarWidth(width)).FillHeight().Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
					ui.Column(c).Padding(16).Gap(8).Children(func() {
						ui.Text(c, "工作区").FontSize(12).TextColor(t.TextMuted)
						if navigationButton(c, "nav-connections", "SSH 连接", d.workspace == "" || d.workspace == "connections").Clicked() {
							d.workspace = "connections"
							if d.editConnection {
								d.closeConnectionEditor()
							}
						}
						if navigationButton(c, "nav-terminal", "终端", d.workspace == "terminal").Clicked() {
							d.workspace = "terminal"
						}
						if navigationButton(c, "nav-files", "远程文件", d.workspace == "files").Clicked() {
							d.workspace = "files"
							if d.term != nil && d.shell == nil && d.files == nil && !d.busy {
								d.refreshFiles()
							}
						}
						if navigationButton(c, "nav-forward", "端口转发", d.workspace == "forward").Clicked() {
							d.workspace = "forward"
						}
						if navigationButton(c, "nav-snippets", "命令片段", d.workspace == "snippets").Clicked() {
							d.workspace = "snippets"
						}
						if navigationButton(c, "nav-cloud", "云同步与团队", d.workspace == "cloud").Clicked() {
							d.workspace = "cloud"
						}
						filterLabel := "连接筛选"
						if d.showConnectionNavigation {
							filterLabel = "收起连接筛选"
						} else if d.profileFavorites {
							filterLabel = "筛选：收藏"
						} else if d.profileRecent {
							filterLabel = "筛选：最近"
						} else if d.profileFilter != "" {
							filterLabel = "分组：" + d.profileFilter
						}
						if ui.Button(c.Key("connection-navigation-toggle"), filterLabel).Label("连接筛选").FillWidth().Clicked() {
							d.showConnectionNavigation = !d.showConnectionNavigation
						}
						if d.showConnectionNavigation {
							connectionCount := len(d.profiles) + 1
							if d.cloud != nil {
								connectionCount += len(d.accounts)
							}
							if navigationButton(c, "group-all", fmt.Sprintf("全部连接  %d", connectionCount), d.profileFilter == "" && !d.profileFavorites && !d.profileRecent).Disabled(draftBlocked).Clicked() {
								if !d.closeConnectionForNavigation() {
									return
								}
								d.workspace = "connections"
								d.profileFilter = ""
								d.profileFavorites = false
								d.profileRecent = false
							}
							isCloudGroups := d.profileSource == "cloud"
							groups, counts := d.groupTree()
							folded := d.foldedGroups
							if isCloudGroups {
								groups, counts = d.cloudGroupTree()
								folded = d.cloudFoldedGroups
							}
							favorites := 0
							recent := 0
							for _, p := range d.profiles {
								if p.Favorite {
									favorites++
								}
								if p.LastConnected > 0 {
									recent++
								}
							}
							if navigationButton(c, "group-favorites", fmt.Sprintf("收藏的连接  %d", favorites), d.profileFavorites).Disabled(draftBlocked).Clicked() {
								if !d.closeConnectionForNavigation() {
									return
								}
								d.workspace = "connections"
								d.profileFilter = ""
								d.profileFavorites = true
								d.profileRecent = false
								if d.profileSource == "cloud" {
									d.profileSource = "local"
								}
							}
							if navigationButton(c, "group-recent", fmt.Sprintf("最近连接  %d", recent), d.profileRecent).Disabled(draftBlocked).Clicked() {
								if !d.closeConnectionForNavigation() {
									return
								}
								d.workspace = "connections"
								d.profileFilter = ""
								d.profileFavorites = false
								d.profileRecent = true
								if d.profileSource == "cloud" {
									d.profileSource = "local"
								}
							}
							parents := groupsWithChildren(groups)
							for _, group := range groups {
								name := group
								if groupHidden(name, folded) {
									continue
								}
								ui.Row(c).FillWidth().Gap(2).AlignItems(ui.Center).Children(func() {
									if depth := groupDepth(name); depth > 0 {
										ui.Box(c).Width(float32(depth * 10)).Shrink(0)
									}
									if parents[name] {
										label, title := "▾", "折叠分组："+name
										if folded[name] {
											label, title = "▸", "展开分组："+name
										}
										if ui.Button(c.Key("group-fold-"+name), label).Label(title).Width(32).Clicked() {
											if isCloudGroups {
												if d.cloudFoldedGroups == nil {
													d.cloudFoldedGroups = make(map[string]bool)
												}
												d.cloudFoldedGroups[name] = !d.cloudFoldedGroups[name]
											} else {
												if d.foldedGroups == nil {
													d.foldedGroups = make(map[string]bool)
												}
												d.foldedGroups[name] = !d.foldedGroups[name]
											}
										}
									}
									groupLabel := "分组：" + name
									if isCloudGroups {
										groupLabel = "云端分组：" + name
									}
									button := navigationButton(c, "group-entry-"+name, fmt.Sprintf("%s  %d", groupLeaf(name), counts[name]), d.profileFilter == name).Label(groupLabel).Disabled(d.busy || draftBlocked).Grow(1)
									button.ContextMenu(func(menu *ui.Menu) {
										if menu.Item("快速连接").Disabled(d.busy || draftBlocked || counts[name] == 0).Chosen() {
											d.quickConnectGroup(name, isCloudGroups)
										}
										if menu.Item("管理分组").Disabled(draftBlocked).Chosen() {
											if !d.closeConnectionForNavigation() {
												return
											}
											d.workspace = "connections"
											if isCloudGroups {
												d.profileSource = "cloud"
											} else {
												d.profileSource = "local"
											}
											d.profileFilter, d.showGroupControls, d.confirmGroupDelete = name, true, false
											d.profileFavorites, d.profileRecent = false, false
										}
									})
									if button.Clicked() {
										if !d.closeConnectionForNavigation() {
											return
										}
										d.workspace = "connections"
										d.profileFilter = name
										d.confirmGroupDelete = false
										d.profileFavorites = false
										d.profileRecent = false
										if !isCloudGroups && d.profileSource == "cloud" {
											d.profileSource = "local"
										}
									}
									if isCloudGroups {
										if dragged, ok := ui.Drop[cloudGroupDragID](button); ok && !d.busy && d.prepareConnectionNavigation() {
											d.cloudGroupOperation("move", string(dragged), name)
										}
									} else {
										if dragged, ok := ui.Drop[profileID](button); ok && !d.busy && d.prepareConnectionNavigation() {
											d.moveProfileToGroup(dragged, name)
										}
									}
								})
							}
						}
					})
				})
			}
			if d.workspace != "terminal" && d.monitorCancel != nil {
				d.stopMonitor()
			}
			ui.Column(c).Grow(1).FillHeight().Children(func() {
				if d.workspace == "terminal" || d.workspace == "new-tab" {
					if width < 1100 {
						ui.ScrollHorizontal(c).FillWidth().Padding(8, 12).Gap(6).Children(func() { d.sessionTabs(c) })
					}
					if d.workspace == "new-tab" {
						ui.Scroll(c).Grow(1).FillWidth().Children(func() { d.newTabView(c) })
						return
					}
					ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Padding(6, 12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
						if width >= 900 {
							ui.Text(c, "终端").FontSize(14).Bold()
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
							ui.Text(c, status).FontSize(12).TextColor(t.TextMuted).MaxWidth(240).SingleLine().Tooltip(status)
						}
						if len(d.groupConnectFailures) > 0 {
							d.groupConnectionFailuresButton(c)
						}
						ui.Box(c).Grow(1)
						if terminalActionButton(c, "terminal-search-toggle", "查找", searchIcon, d.terminalSearchOpen, d.term == nil).Clicked() {
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
						if terminalActionButton(c, "quick-toggle", quickLabel, snippetIcon, d.quickOpen, d.term == nil).Clicked() {
							d.toggleQuickCommands()
						}
						if d.shell != nil || d.monitorCancel != nil {
							monitorLabel := "服务器状态"
							if d.monitorCancel != nil {
								monitorLabel = "关闭状态"
							}
							if terminalActionButton(c, "monitor-toggle", monitorLabel, monitorIcon, d.monitorCancel != nil, d.monitorCancel == nil && terminalEnded(d.term)).Clicked() {
								d.toggleMonitor()
							}
						}
						splitLabel, splitActive := "分屏查看", d.splitSession != 0
						splitDisabled := !d.canSwitchSession() || (d.splitSession == 0 && len(d.sessions) < 2)
						if group := d.terminalGroupFor(d.currentSession); group != nil {
							splitActive = group.grid
							splitDisabled = !d.canSwitchSession() || len(group.members) < 2
						}
						if splitActive {
							splitLabel = "关闭分屏"
						}
						if terminalActionButton(c, "toggle-split", splitLabel, splitIcon, splitActive, splitDisabled).Clicked() {
							d.toggleSplit()
						}
						if group := d.terminalGroupFor(d.currentSession); group != nil {
							broadcastLabel := "广播输入到工作区"
							if group.broadcast {
								broadcastLabel = "关闭广播输入"
							}
							if terminalActionButton(c, "toggle-broadcast", broadcastLabel, broadcastIcon, group.broadcast, d.busy || len(group.members) < 2).Clicked() {
								d.toggleGroupBroadcast(group)
							}
							if group.broadcast {
								status := "广播"
								if !group.broadcastMembers[d.currentSession] || len(group.broadcastMembers) < 2 {
									status = "广播暂停"
								}
								ui.Text(c, fmt.Sprintf("%s · %d/%d", status, len(group.broadcastMembers), len(group.members))).FontSize(12).TextColor(t.Accent)
							}
							d.groupAddButton(c, group)
						}
						label := "打开文件面板"
						if d.showFilePanel {
							label = "收起文件面板"
						}
						if terminalActionButton(c, "toggle-file-panel", label, fileIcon, d.showFilePanel, d.term == nil || !d.canSwitchSession()).Clicked() {
							d.showFilePanel = !d.showFilePanel
							if d.showFilePanel {
								d.quickOpen = false
							}
							d.savePreference("files", d.showFilePanel)
							if d.showFilePanel && d.shell == nil && d.files == nil {
								d.refreshFiles()
							}
						}
					})
					if d.term == nil {
						ui.Column(c).FillWidth().Padding(32).Gap(10).Children(func() {
							ui.Text(c, "开始使用终端").FontSize(20).Bold()
							ui.Text(c, "打开本机终端，或选择 SSH 连接开始远程会话。").TextColor(t.TextMuted).Wrap()
							ui.Row(c).Wrap().Gap(8).Children(func() {
								if ui.PrimaryButton(c.Key("terminal-empty-local"), "打开本机终端").Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions).Clicked() {
									d.openLocalTerminal()
								}
								if ui.Button(c.Key("terminal-empty-connections"), "选择 SSH 连接").Clicked() {
									d.workspace = "connections"
								}
							})
						})
					} else {
						sideQuick := d.quickOpen && workspaceWidth >= 1100 && !compactAuxiliaryPanels
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
						if d.quickOpen && !sideQuick {
							panelHeight := float32(200)
							if compactAuxiliaryPanels {
								panelHeight = 90
							}
							ui.Scroll(c.Key("quick-compact-scroll")).Label("快速命令面板").Height(panelHeight).Shrink(0).FillWidth().Children(func() { d.quickCommandsSideView(c) })
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
						if sideQuick {
							ui.Row(c).Grow(1).FillWidth().Children(func() {
								ui.Column(c).Grow(1).FillHeight().Children(func() { d.terminalMainView(c, workspaceWidth-340, compactAuxiliaryPanels) })
								ui.Scroll(c.Key("quick-side-scroll")).Label("快速命令面板").Width(340).FillHeight().Shrink(0).Background(t.Surface).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() { d.quickCommandsSideView(c) })
							})
						} else {
							d.terminalMainView(c, workspaceWidth, compactAuxiliaryPanels)
						}
					}
					return
				}
				if (d.workspace == "" || d.workspace == "connections") && (!d.editConnection || d.connectionInspectorDocked(width)) {
					ui.Row(c).Grow(1).FillWidth().Padding(24).Gap(16).Children(func() {
						ui.Column(c).Grow(1).FillHeight().Children(func() { d.profileView(c) })
						if d.editConnection {
							ui.Column(c.Key("connection-inspector")).Label("主机详情面板").Width(connectionInspectorWidth).FillHeight().Shrink(0).Background(t.Surface).Radius(10).Children(func() {
								ui.Column(c).FillWidth().Padding(12, 16).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() { d.connectionHeader(c, true) })
								ui.Scroll(c.Key("connection-form-scroll")).Label("主机详情内容").Grow(1).FillWidth().Children(func() {
									ui.Column(c).FillWidth().Padding(16).Gap(10).Children(func() { d.connectionView(c, true) })
								})
								ui.Column(c).FillWidth().Padding(10, 16).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() { d.connectionActions(c) })
							})
						}
					})
					return
				}
				ui.Scroll(c).Grow(1).FillWidth().Children(func() {
					ui.Column(c).Padding(24).Gap(10).Children(func() {
						switch d.workspace {
						case "files":
							ui.Text(c, "SFTP").FontSize(16).Bold()
							d.fileWorkspace(c, width, height)
						case "forward":
							d.forwardView(c)
						case "snippets":
							ui.Column(c).FillWidth().MaxWidth(680).Children(func() { d.quickCommandsSideView(c) })
						case "cloud":
							ui.Text(c, "云端工作区").FontSize(22).Bold()
							d.cloudView(c)
						default:
							if d.editConnection {
								d.connectionView(c, false)
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

func (d *desktop) connectionHeader(c *ui.Context, docked bool) {
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
		title, back := "SSH 连接", "返回连接列表"
		if docked {
			title, back = "主机详情", "×"
		}
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			ui.Text(c, title).FontSize(16).Bold()
			if docked {
				source := "本机连接"
				if d.settingsFromCloud {
					source = "云端 · " + d.cloudScopeName()
				}
				ui.Text(c, source).FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
			}
		})
		if ui.Button(c.Key("connection-options"), "⋯").Label("更多主机操作").Tooltip("更多主机操作").Width(36).Clicked() {
			d.showConnectionTools = !d.showConnectionTools
		}
		if ui.Button(c.Key("connection-back"), back).Label("返回连接列表").Tooltip("返回连接列表").Disabled(d.busy).Clicked() {
			d.closeConnectionEditor()
		}
	})
}

func (d *desktop) connectionView(c *ui.Context, stickyActions bool) {
	width, _ := c.Size()
	docked := d.connectionInspectorDocked(width)
	if !stickyActions {
		d.connectionHeader(c, false)
	}
	sectionColor, sectionPadding, sectionGap := c.Theme().Surface, float32(18), float32(10)
	if docked {
		sectionColor, sectionPadding, sectionGap = c.Theme().SurfaceHover, 12, 6
	}
	markMetadataChanged := func() { d.confirmDiscardConnection = false; d.profileAutoDirty = true }
	markDraftChanged := func() { d.confirmDiscardConnection = false; d.profileSecretError = false; d.profileAutoDirty = true }
	resetFingerprint := func() { d.fingerprint, d.expectedFingerprint, d.changedFingerprint = "", "", ""; markMetadataChanged() }
	ui.Column(c).FillWidth().Padding(sectionPadding).Gap(sectionGap).Background(sectionColor).Radius(10).Children(func() {
		ui.Text(c, "地址").FontSize(15).Bold()
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(34).Height(34).Radius(8).Background(c.Theme().Accent).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Icon(c, serverIcon).Width(18).Height(18).TextColor(c.Theme().AccentText)
			})
			host := ui.TextInput(c.Key("host"), &d.settings.Host).Label("主机地址").Placeholder("主机名或 IP").OnChange(resetFingerprint).Grow(1)
			if d.connectionFocusPending {
				host.Focus()
				d.connectionFocusPending = false
			}
		})
	})
	ui.Column(c).FillWidth().Padding(sectionPadding).Gap(sectionGap).Background(sectionColor).Radius(10).Children(func() {
		ui.Text(c, "通用").FontSize(15).Bold()
		ui.TextInput(c.Key("profile-label"), &d.profileLabel).Label("连接名称").Placeholder("名称（可选）").OnChange(markMetadataChanged).FillWidth()
		if !d.settingsFromCloud {
			ui.TextInput(c.Key("profile-group"), &d.profileGroup).Label("连接分组").Placeholder("分组（可选）").OnChange(markMetadataChanged).FillWidth()
		}
		if !d.settingsFromCloud || (d.cloud != nil && d.cloud.cloudTags) {
			ui.TextInput(c.Key("profile-tags"), &d.profileTags).Label("主机标签").Placeholder("标签，用逗号分隔").OnChange(markMetadataChanged).FillWidth()
		}
	})
	ui.Column(c).FillWidth().Padding(sectionPadding).Gap(sectionGap).Background(sectionColor).Radius(10).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "SSH").FontSize(15).Bold()
			if !d.settingsFromCloud {
				ui.Text(c, "本机加密保存").FontSize(11).TextColor(c.Theme().TextMuted).Tooltip("密码和私钥口令由当前 Windows 用户加密保存")
			}
			ui.Box(c).Grow(1)
			ui.Text(c, "端口").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.TextInput(c.Key("port"), &d.settings.Port).Label("端口").Placeholder("22").OnChange(resetFingerprint).Width(78)
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "用户").FontSize(12).TextColor(c.Theme().TextMuted).Width(32)
			ui.TextInput(c.Key("user"), &d.settings.User).Label("用户名").Placeholder("用户名").OnChange(resetFingerprint).Grow(1)
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "密码").FontSize(12).TextColor(c.Theme().TextMuted).Width(32)
			ui.TextInput(c.Key("password"), &d.settings.Password).Label("SSH 密码").Placeholder("SSH 密码").Password().OnChange(markDraftChanged).Grow(1)
		})
		keyExpanded := d.showKeyOptions || d.settings.KeyPath != "" || d.settings.Passphrase != ""
		if !keyExpanded {
			if ui.Button(c.Key("key-options-toggle"), "使用私钥…").Clicked() {
				d.showKeyOptions = true
			}
		} else {
			if d.settings.KeyPath != "" {
				ui.Text(c, "已配置私钥，连接时优先使用私钥。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			}
			ui.Text(c, "私钥文件（可选）").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
				ui.TextInput(c.Key("key"), &d.settings.KeyPath).Label("私钥路径").Placeholder("私钥路径（可选）").OnChange(markMetadataChanged).Grow(1)
				if ui.Button(c.Key("pick-key"), "选择私钥文件…").Disabled(d.busy).Clicked() {
					d.pickLocalPath("key")
				}
			})
			ui.Text(c, "私钥口令（可选）").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.TextInput(c.Key("passphrase"), &d.settings.Passphrase).Label("私钥口令").Placeholder("私钥口令（可选）").Password().OnChange(markDraftChanged).FillWidth()
			if ui.Button(c.Key("key-options-close"), "收起私钥选项").Disabled(d.settings.KeyPath != "" || d.settings.Passphrase != "").Tooltip("清空私钥和口令后可收起").Clicked() {
				d.showKeyOptions = false
			}
		}
	})
	if d.trustedProfileFingerprint() != "" || d.changedFingerprint != "" || (d.fingerprint != "" && !stickyActions) {
		ui.Text(c, "主机验证").FontSize(16).Bold()
	}
	if trusted := d.trustedProfileFingerprint(); trusted != "" {
		ui.Text(c, "本机已信任指纹："+trusted).FontSize(12).Wrap()
	}
	if d.changedFingerprint != "" && !stickyActions {
		ui.Text(c, "原指纹："+d.expectedFingerprint).FontSize(12).Wrap()
		ui.Text(c, "新指纹："+d.changedFingerprint).FontSize(12).Wrap()
		ui.Text(c, "请先通过可信渠道核实新指纹；确认前不会发送认证信息。").FontSize(12).Wrap()
	}
	if d.fingerprint != "" && !stickyActions {
		ui.Text(c, d.fingerprint).FontSize(12)
	}
	if d.cloud != nil {
		if d.showConnectionTools {
			if d.formCloudSource() != "" {
				ui.Text(c, "将更新已载入的云端连接；其他相同地址的连接不会被修改。").FontSize(12).Wrap()
			} else {
				ui.Text(c, "将新建云端连接；相同地址不会自动覆盖。").FontSize(12).Wrap()
			}
		}
	}
	if !stickyActions {
		d.connectionActions(c)
	}
}

func (d *desktop) connectionActions(c *ui.Context) {
	if d.changedFingerprint != "" {
		ui.Text(c, "原指纹："+d.expectedFingerprint).FontSize(11).Wrap()
		ui.Text(c, "新指纹："+d.changedFingerprint).FontSize(11).Wrap()
		ui.Text(c, "请先通过可信渠道核实新指纹；确认前不会发送认证信息。").FontSize(11).Wrap()
		ui.Button(c.Key("accept-changed-fingerprint"), "已核实，使用新指纹").Disabled(d.busy).OnClick(d.acceptChangedFingerprint)
	} else if d.fingerprint != "" {
		ui.Text(c, "请核对指纹："+d.fingerprint).FontSize(11).Wrap()
	}
	if d.cloud != nil {
		scope := "同步目标：" + d.cloudScopeName()
		if d.cloud.teamID != "" {
			scope += "（团队成员可访问）"
		}
		operation := "同步时新建连接"
		if d.formCloudSource() != "" {
			operation = "同步时更新当前云端连接"
		}
		ui.Text(c, scope+" · "+operation).FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
		if d.cloud.capabilityUnknown {
			ui.Text(c, "无法确认服务的并发保护能力；更新已有连接前请刷新云端连接。").FontSize(11).Wrap()
		} else if !d.cloud.conditionalUpdates {
			ui.Text(c, "未确认同步服务支持并发修改保护；更新可能覆盖其他设备的更改。").FontSize(11).Wrap()
		}
		if d.cloudListStale {
			ui.Text(c, "云端列表需要先刷新后才能同步。").FontSize(11).Wrap()
		}
	}
	if d.showConnectionTools {
		ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
			if ui.Button(c.Key("profile-add"), "保存").Label("保存到连接列表").Tooltip("保存到连接列表").Disabled(d.busy).Clicked() {
				if err := d.saveCurrentProfile(); err != nil {
					d.status = "保存失败，表单内容仍保留：" + err.Error()
				} else {
					d.status = "连接已保存"
					current := d.currentConnectionDraft()
					d.connectionDraft.metadata = current.metadata
					if !d.settingsFromCloud {
						d.connectionDraft.secrets = current.secrets
					}
					d.profileAutoDirty, d.profileAutoAt = false, time.Time{}
				}
			}
			ui.Button(c.Key("inspect"), "获取指纹").Label("获取主机指纹").Tooltip("获取主机指纹").Disabled(d.busy).OnClick(d.inspect)
		})
	}
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		if d.cloud != nil {
			ui.Button(c.Key("cloudsave-form"), "同步").Label("确认指纹并同步密码连接").Tooltip("确认指纹并同步密码连接").Disabled(d.busy || !d.canWriteCloudScope() || d.cloudListStale || d.fingerprint == "" || d.settings.Password == "" || d.settings.KeyPath != "").OnClick(d.cloudSaveForm)
		}
		label, accessible := "连接", "连接并核验主机指纹"
		if d.fingerprint != "" {
			label, accessible = "确认并连接", "确认指纹并连接"
		} else if d.trustedProfileFingerprint() != "" {
			accessible = "使用已信任指纹连接"
		}
		if ui.PrimaryButton(c.Key("connect"), label).Label(accessible).Grow(1).Disabled(d.busy || d.changedFingerprint != "" || len(d.sessions) >= maxWorkspaceSessions).Clicked() {
			if d.fingerprint != "" {
				d.connect()
			} else if d.trustedProfileFingerprint() != "" {
				d.connectTrustedProfile()
			} else {
				d.inspect()
			}
		}
	})
	if d.status != "" {
		ui.Text(c, d.status).FontSize(12).Wrap()
	}
}

const version = "0.3.109"
const defaultCloudURL = "https://nekossh.neko233.com"

func main() {
	showVersion := flag.Bool("version", false, "显示版本")
	checkBroadcast := flag.Bool("verify-broadcast", false, "检查广播输入构建支持")
	flag.Parse()
	if *showVersion {
		fmt.Println("Terminal233 " + version)
		return
	}
	if *checkBroadcast {
		if _, ok := any((*terminal.Terminal)(nil)).(broadcastTerminal); !ok {
			fmt.Fprintln(os.Stderr, "broadcast-input: unavailable")
			os.Exit(1)
		}
		fmt.Println("broadcast-input: available")
		return
	}
	d := &desktop{settings: connectionSettings{Port: "22"}, cloudURL: defaultCloudURL, forwardLocalPort: "0", forwardHost: "127.0.0.1", teamInviteHours: "24", teamInviteUses: "1"}
	d.terminalLightTheme, d.terminalDarkTheme, _ = terminalThemePair("")
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
		var saved quickCommandFile
		if saved, err = readQuickCommandFile(d.quickPath); err != nil {
			d.quickStatus = "读取快速命令失败，原文件未覆盖：" + err.Error()
		} else {
			d.quickCommands, d.quickPackages = saved.Items, saved.Packages
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		d.workspaceTemplatesPath = filepath.Join(dir, "NekoSSH", "workspaces.json")
		if saved, readErr := readWorkspaceTemplates(d.workspaceTemplatesPath); readErr != nil {
			d.workspaceTemplatesStatus = "读取工作区失败（原文件已保留）：" + readErr.Error()
		} else {
			d.workspaceTemplates = saved.Items
		}
	} else {
		d.workspaceTemplatesStatus = "工作区存储不可用：" + err.Error()
	}
	mygo.Theme.SetSource(mygo.ThemeDark)
	mygo.App.WhenReady(func() {
		d.win = mygo.NewWindow(mygo.WindowOptions{Title: "NekoSSH", Width: 1200, Height: 760, MinWidth: 640, MinHeight: 480, Content: ui.View(d.view)})
		d.win.OnClosed(func() {
			if d.profileAutoDirty || !d.profileAutoAt.IsZero() {
				d.flushProfileAutosave()
			}
			d.closeWorkspaceRestore()
			d.stopExternalEdits()
			d.closeAllSessions()
		})
		d.startUpdateCheck()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
