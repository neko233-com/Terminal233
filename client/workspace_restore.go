package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type workspaceRestore struct {
	cancel     context.CancelFunc
	closed     atomic.Bool
	cancelling bool
	groupQuick bool
}
type workspaceRestoreTarget struct {
	entry           workspaceTemplateEntry
	settings        connectionSettings
	label           string
	sourceProfileID string
	cloudVersion    string
}
type workspaceRestoredSession struct {
	target workspaceRestoreTarget
	shell  *remoteShell
	term   *terminal.Terminal
}

func (d *desktop) resolveWorkspace(w savedWorkspace) ([]workspaceRestoreTarget, error) {
	if err := validateSavedWorkspace(w); err != nil {
		return nil, err
	}
	if d.sessionCount(false)+len(w.Entries) > maxWorkspaceSessions {
		return nil, fmt.Errorf("会话数量超过上限 %d", maxWorkspaceSessions)
	}
	targets := make([]workspaceRestoreTarget, 0, len(w.Entries))
	for i, e := range w.Entries {
		target := workspaceRestoreTarget{entry: e, label: "本机终端"}
		switch e.Kind {
		case "local":
			if e.Label != "" {
				target.label = e.Label
			}
			if e.LocalDir != "" {
				info, err := os.Stat(e.LocalDir)
				if err != nil || !info.IsDir() {
					return nil, fmt.Errorf("第 %d 个本机终端目录不可用", i+1)
				}
			}
		case "profile":
			var matches []profile
			for _, p := range d.profiles {
				if profileMatchesEntry(p, e) {
					matches = append(matches, p)
				}
			}
			if len(matches) != 1 {
				return nil, fmt.Errorf("第 %d 个本机主机已修改或不存在，请重新保存工作区", i+1)
			}
			settings, err := d.profileCredential(matches[0])
			if err != nil {
				return nil, fmt.Errorf("第 %d 个主机：%w", i+1, err)
			}
			if !settings.canUseStoredAuth() {
				return nil, fmt.Errorf("第 %d 个主机没有保存可用凭据", i+1)
			}
			target.settings, target.label, target.sourceProfileID = settings, settings.User+"@"+settings.Host, string(matches[0].ID)
			if matches[0].Label != "" {
				target.label = matches[0].Label
			}
		case "cloud":
			if d.cloud == nil || d.cloud.base == nil || d.cloud.token == "" || d.cloud.base.String() != e.CloudURL || d.cloudUser != e.CloudUser {
				return nil, fmt.Errorf("第 %d 个主机需要先登录对应云账号", i+1)
			}
			target.settings = connectionSettings{Host: e.Host, Port: e.Port, User: e.User, Fingerprint: e.Fingerprint}
			target.label = e.User + "@" + e.Host
			if e.Label != "" {
				target.label = e.Label
			}
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// Resolve every cloud scope before opening any terminal. The caller supplies
// a private client copy, so this never changes the user's selected team.
func hydrateWorkspaceCloudTargets(ctx context.Context, targets []workspaceRestoreTarget, load func(string) ([]cloudAccount, error)) ([]workspaceRestoreTarget, error) {
	resolved := append([]workspaceRestoreTarget(nil), targets...)
	cache := map[string][]cloudAccount{}
	for i := range resolved {
		target := &resolved[i]
		if target.entry.Kind != "cloud" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		accounts, found := cache[target.entry.CloudTeam]
		if !found {
			var err error
			accounts, err = load(target.entry.CloudTeam)
			if err != nil {
				return nil, fmt.Errorf("第 %d 个云主机所属空间不可用：%w", i+1, err)
			}
			cache[target.entry.CloudTeam] = accounts
		}
		var match *cloudAccount
		for j := range accounts {
			if accounts[j].ID == target.entry.CloudID {
				if match != nil {
					return nil, errors.New("云主机引用不唯一")
				}
				match = &accounts[j]
			}
		}
		e := target.entry
		if match == nil || match.Host != e.Host || strconv.Itoa(match.Port) != e.Port || match.Username != e.User || match.Password == "" {
			return nil, fmt.Errorf("第 %d 个云主机已修改或不可用，请重新保存工作区", i+1)
		}
		target.settings.Password = match.Password
		if target.entry.Label == "" && match.Label != "" {
			target.label = match.Label
		}
		target.cloudVersion = match.UpdatedAt
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return resolved, nil
}

func (d *desktop) saveWorkspaceTemplate() {
	if d.busy {
		return
	}
	w, err := d.workspaceSnapshot(d.workspaceTemplateName)
	if err != nil {
		d.workspaceTemplatesStatus = err.Error()
		return
	}
	saved, err := updateWorkspaceTemplates(d.workspaceTemplatesPath, func(file *workspaceTemplatesFile) error {
		for _, existing := range file.Items {
			if strings.EqualFold(existing.Name, w.Name) {
				return errors.New("工作区名称已存在")
			}
		}
		file.Items = append(file.Items, w)
		return nil
	})
	if err != nil {
		d.workspaceTemplatesStatus = "保存失败：" + err.Error()
		return
	}
	d.workspaceTemplates = saved.Items
	if group := d.terminalGroupFor(d.currentSession); group != nil && group.savedLocal == nil && group.savedCloud == nil && group.name == w.Name {
		original := cloneSavedWorkspace(w)
		group.savedLocal, group.savedSignature = &original, d.workspaceGroupSignature(group)
	}
	d.workspaceTemplateName = ""
	d.workspaceTemplatesStatus = "工作区已保存"
	for _, entry := range w.Entries {
		if entry.Kind != "local" && entry.TerminalDir == "" {
			d.workspaceTemplatesStatus = "工作区已保存；部分远程终端未报告目录（需支持 OSC 7）"
			break
		}
	}
	if d.terminalGroupFor(d.currentSession) == nil {
		for _, session := range d.sessions {
			if session.fileOnly {
				d.workspaceTemplatesStatus += "；SFTP 连接未包含在终端工作区中"
				break
			}
		}
	}
	if d.cloud != nil && d.cloud.cloudWorkspaces {
		d.uploadWorkspaceTemplate(w)
	}
}

func (d *desktop) removeWorkspaceTemplate(id string) {
	if d.busy {
		return
	}
	if d.confirmWorkspaceRemove != id {
		d.confirmWorkspaceRemove = id
		return
	}
	saved, err := updateWorkspaceTemplates(d.workspaceTemplatesPath, func(file *workspaceTemplatesFile) error {
		for i, w := range file.Items {
			if w.ID == id {
				file.Items = append(file.Items[:i], file.Items[i+1:]...)
				return nil
			}
		}
		return errors.New("工作区已在其他窗口删除，请刷新")
	})
	if err != nil {
		d.workspaceTemplatesStatus = "删除失败：" + err.Error()
		return
	}
	d.workspaceTemplates = saved.Items
	d.confirmWorkspaceRemove = ""
	d.workspaceTemplatesStatus = "工作区已删除"
}

func prepareWorkspaceSessions(ctx context.Context, targets []workspaceRestoreTarget, open func(workspaceRestoreTarget) (workspaceRestoredSession, error), closeSession func(workspaceRestoredSession), progress func(int)) ([]workspaceRestoredSession, error) {
	opened := make([]workspaceRestoredSession, 0, len(targets))
	rollback := func() {
		for _, session := range opened {
			closeSession(session)
		}
	}
	for i, target := range targets {
		if err := ctx.Err(); err != nil {
			rollback()
			return nil, err
		}
		if progress != nil {
			progress(i + 1)
		}
		session, err := open(target)
		if err != nil {
			closeSession(session)
			rollback()
			return nil, fmt.Errorf("第 %d 个会话连接失败：%w", i+1, err)
		}
		opened = append(opened, session)
	}
	if err := ctx.Err(); err != nil {
		rollback()
		return nil, err
	}
	for i, session := range opened {
		if session.term != nil && terminalEnded(session.term) {
			rollback()
			return nil, fmt.Errorf("第 %d 个会话在恢复期间已断开", i+1)
		}
	}
	return opened, nil
}

func workspaceTemplateUnchanged(filename string, selected savedWorkspace) error {
	file, err := readWorkspaceTemplates(filename)
	if err != nil {
		return err
	}
	for _, current := range file.Items {
		if current.ID == selected.ID {
			if reflect.DeepEqual(current, selected) {
				return nil
			}
			break
		}
	}
	return errors.New("工作区已被其他窗口修改或删除，请刷新")
}

func (d *desktop) restoreWorkspaceTemplate(w savedWorkspace) {
	d.startWorkspace(w, true)
}

func (d *desktop) startWorkspace(w savedWorkspace, saved bool) {
	d.startWorkspaceWithCloudSource(w, saved, nil)
}

type workspaceCloudSource struct {
	workspace cloudWorkspace
	url, user string
}

func (d *desktop) startWorkspaceWithCloudSource(w savedWorkspace, saved bool, cloudSource *workspaceCloudSource) {
	if d.busy || d.win == nil {
		return
	}
	if !d.prepareConnectionNavigation() {
		d.workspaceTemplatesStatus = d.status
		return
	}
	if saved {
		if err := workspaceTemplateUnchanged(d.workspaceTemplatesPath, w); err != nil {
			d.workspaceTemplatesStatus = err.Error()
			return
		}
	}
	targets, err := d.resolveWorkspace(w)
	if err != nil {
		d.workspaceTemplatesStatus = err.Error()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	op := &workspaceRestore{cancel: cancel}
	d.workspaceRestore = op
	d.busy = true
	action := "恢复"
	if !saved {
		action = "创建"
	}
	d.workspaceTemplatesStatus = "正在" + action + "工作区…"
	options := d.terminalOptions()
	var cloudCopy *cloudClient
	if d.cloud != nil {
		snapshot := *d.cloud
		cloudCopy = &snapshot
	}
	win := d.win
	go func() {
		closeSession := func(item workspaceRestoredSession) {
			if item.term != nil {
				_ = item.term.Close()
			}
		}
		resolved, failure := hydrateWorkspaceCloudTargets(ctx, targets, func(team string) ([]cloudAccount, error) {
			if cloudCopy == nil {
				return nil, errors.New("请先登录同步服务")
			}
			scope := *cloudCopy
			scope.teamID = team
			return scope.list()
		})
		var opened []workspaceRestoredSession
		if failure == nil {
			opened, failure = prepareWorkspaceSessions(ctx, resolved, func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
				var shell *remoteShell
				var term *terminal.Terminal
				var err error
				if target.entry.Kind == "local" {
					localOptions := options
					localOptions.Dir = target.entry.LocalDir
					term, err = newLocalTerminal(localOptions)
				} else {
					shell, term, err = openSSHSessionAt(target.settings, options, target.entry.TerminalDir)
				}
				return workspaceRestoredSession{target: target, shell: shell, term: term}, err
			}, closeSession, func(index int) {
				if !op.closed.Load() {
					win.Update(func() {
						if d.workspaceRestore == op && !op.closed.Load() && !op.cancelling {
							d.workspaceTemplatesStatus = fmt.Sprintf("正在%s %d/%d…", action, index, len(targets))
						}
					})
				}
			})
		}
		closeOpened := func() {
			for _, item := range opened {
				closeSession(item)
			}
		}
		if op.closed.Load() {
			if failure == nil {
				closeOpened()
			}
			cancel()
			return
		}
		win.Update(func() {
			defer cancel()
			if op.closed.Load() {
				if failure == nil {
					closeOpened()
				}
				return
			}
			if d.workspaceRestore != op {
				if failure == nil {
					closeOpened()
				}
				return
			}
			if failure == nil && ctx.Err() != nil {
				closeOpened()
				failure = ctx.Err()
			}
			if failure == nil {
				for i, session := range opened {
					if session.term == nil || terminalEnded(session.term) {
						closeOpened()
						failure = fmt.Errorf("第 %d 个会话在恢复期间已断开", i+1)
						break
					}
				}
			}
			d.workspaceRestore = nil
			d.busy = false
			if failure != nil {
				if errors.Is(failure, context.Canceled) {
					d.workspaceTemplatesStatus = "已取消恢复，未添加会话"
				} else {
					d.workspaceTemplatesStatus = failure.Error()
				}
				d.status = d.workspaceTemplatesStatus
				return
			}
			d.splitSession, d.splitFirstSession = 0, 0
			ids := make([]int, 0, len(opened))
			for _, item := range opened {
				d.addSession(item.term, item.shell, item.target.settings, item.target.label)
				id := d.currentSession
				ids = append(ids, id)
				entry := item.target.entry
				if entry.FileDir != "" {
					d.fileDir = entry.FileDir
				}
				for _, s := range d.sessions {
					if s.id == id {
						s.fileDir = d.fileDir
						s.sourceProfileID = item.target.sourceProfileID
						if entry.Kind == "cloud" {
							s.cloudOriginURL, s.cloudOriginUser, s.cloudOriginTeam, s.cloudOriginID = entry.CloudURL, entry.CloudUser, entry.CloudTeam, entry.CloudID
							s.cloudSources = map[string]string{entry.CloudTeam: entry.CloudID}
							s.cloudVersions = map[string]string{entry.CloudTeam: item.target.cloudVersion}
						}
						break
					}
				}
			}
			d.splitSession, d.splitFirstSession = 0, 0
			d.activateSession(ids[w.Active])
			grid := w.ViewMode == "grid" || (w.ViewMode == "" && w.SplitFirst >= 0 && w.SplitSecond >= 0)
			groupErr := d.createTerminalGroup(w.Name, ids, ids[w.Active], grid)
			if groupErr == nil {
				if group := d.terminalGroupFor(ids[w.Active]); group != nil {
					for i, entry := range w.Entries {
						if entry.TabName != "" {
							if group.tabNames == nil {
								group.tabNames = make(map[int]string)
							}
							group.tabNames[ids[i]] = entry.TabName
						}
					}
					copy(group.rowPerc[:], w.RowPerc)
					copy(group.columnPerc[:], w.ColumnPerc)
					if saved {
						original := cloneSavedWorkspace(w)
						group.savedLocal = &original
					}
					if cloudSource != nil {
						original := cloneCloudWorkspace(cloudSource.workspace)
						group.savedCloud, group.savedCloudURL, group.savedUser = &original, cloudSource.url, cloudSource.user
					}
				}
			}
			d.splitLayout = w.SplitLayout
			if group := d.terminalGroupFor(ids[w.Active]); group != nil && (group.savedLocal != nil || group.savedCloud != nil) {
				group.savedSignature = d.workspaceGroupSignature(group)
			}
			d.newTabOpen = false
			d.workspace = "terminal"
			d.pendingTerminalFocus = d.currentSession
			d.status = fmt.Sprintf("已%s工作区 %s（%d 个会话）", action, w.Name, len(opened))
			if groupErr != nil {
				d.status = "会话已恢复，但无法组成工作区：" + groupErr.Error()
			}
			d.workspaceTemplatesStatus = ""
			d.showWorkspaceHostSelection = false
			d.workspaceHostSelected = nil
		})
	}()
}

func (d *desktop) cancelWorkspaceRestore() {
	if d.workspaceRestore != nil && !d.workspaceRestore.cancelling {
		d.workspaceRestore.cancelling = true
		d.workspaceTemplatesStatus = "正在取消，等待当前连接结束…"
		if d.workspaceRestore.groupQuick {
			d.status = d.workspaceTemplatesStatus
		}
		d.workspaceRestore.cancel()
	}
}
func (d *desktop) closeWorkspaceRestore() {
	if d.workspaceRestore != nil {
		d.workspaceRestore.closed.Store(true)
		d.workspaceRestore.cancel()
	}
}

func (d *desktop) workspaceTemplatesView(c *ui.Context) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
		ui.Text(c, "工作区模板").FontSize(13).Bold()
		ui.Box(c).Grow(1)
		label := "管理"
		if d.showWorkspaceTemplates {
			label = "收起"
		}
		if ui.Button(c.Key("workspace-templates-toggle"), label).Label("工作区").Clicked() {
			d.showWorkspaceTemplates = !d.showWorkspaceTemplates
			d.confirmWorkspaceRemove = ""
		}
		if d.showWorkspaceTemplates && d.workspaceRestore == nil {
			if ui.Button(c.Key("workspace-template-refresh"), "刷新工作区").Disabled(d.busy).Clicked() {
				saved, err := readWorkspaceTemplates(d.workspaceTemplatesPath)
				if err != nil {
					d.workspaceTemplatesStatus = "读取失败：" + err.Error()
				} else {
					d.workspaceTemplates, d.workspaceTemplatesStatus = saved.Items, "已刷新"
					if d.cloud != nil && d.cloud.cloudWorkspaces {
						d.cloudRefresh()
					}
				}
			}
		}
	})
	if len(d.workspaceTemplates) > 0 {
		ui.Scroll(c.Key("workspace-templates-list")).Label("已保存的工作区").Height(min(float32(160), float32(len(d.workspaceTemplates)*44+12))).FillWidth().Padding(6).Gap(2).Background(c.Theme().Surface).Radius(10).Children(func() {
			for _, entry := range d.workspaceTemplates {
				w := entry
				ui.Row(c.Key("workspace-template-" + w.ID)).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					button := ui.Button(c.Key("workspace-open-"+w.ID), "").Label("打开工作区："+w.Name).Grow(1).BorderWidth(0).Background(c.Theme().Surface).Padding(8, 10).Justify(ui.Start).Disabled(d.busy)
					button.Children(func() {
						ui.Icon(c, splitIcon).Width(16).Height(16).TextColor(c.Theme().TextMuted)
						ui.Text(c, w.Name).Grow(1).SingleLine()
						ui.Text(c, fmt.Sprintf("%d 个连接", len(w.Entries))).FontSize(12).TextColor(c.Theme().TextMuted)
					})
					if button.Clicked() {
						d.restoreWorkspaceTemplate(w)
					}
					if d.showWorkspaceTemplates {
						if d.cloud != nil && d.cloud.cloudWorkspaces {
							if ui.Button(c.Key("workspace-upload-"+w.ID), "同步").Label("同步工作区：" + w.Name).Disabled(d.busy || d.cloudWorkspacesStale).Clicked() {
								d.uploadWorkspaceTemplate(w)
							}
						}
						label := "删除"
						if d.confirmWorkspaceRemove == w.ID {
							label = "确认删除"
						}
						if ui.Button(c.Key("workspace-delete-"+w.ID), label).Label(label + "工作区：" + w.Name).Disabled(d.busy).Clicked() {
							d.removeWorkspaceTemplate(w.ID)
						}
					}
				})
			}
		})
	}
	if d.cloud != nil && d.cloud.cloudWorkspaces && !d.cloudWorkspacesStale && len(d.cloudWorkspaceSnapshot.Items) > 0 {
		ui.Scroll(c.Key("cloud-workspaces-list")).Label("云端工作区").Height(min(float32(160), float32(len(d.cloudWorkspaceSnapshot.Items)*44+12))).FillWidth().Padding(6).Gap(2).Background(c.Theme().Surface).Radius(10).Children(func() {
			for _, entry := range d.cloudWorkspaceSnapshot.Items {
				w := entry
				ui.Row(c.Key("cloud-workspace-" + w.ID)).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					button := ui.Button(c.Key("cloud-workspace-open-"+w.ID), "").Label("打开云工作区："+w.Name).Grow(1).BorderWidth(0).Background(c.Theme().Surface).Padding(8, 10).Justify(ui.Start).Disabled(d.busy)
					button.Children(func() {
						ui.Icon(c, splitIcon).Width(16).Height(16).TextColor(c.Theme().TextMuted)
						ui.Text(c, w.Name).Grow(1).SingleLine()
						ui.Text(c, "云端").FontSize(12).TextColor(c.Theme().TextMuted)
					})
					if button.Clicked() {
						local, err := cloudWorkspaceLocal(w, d.cloud, d.cloudUser, d.trustedWorkspaceCloudFingerprint)
						if err != nil {
							d.workspaceTemplatesStatus = err.Error()
							d.showWorkspaceTemplates = true
						} else {
							d.startWorkspaceWithCloudSource(local, false, &workspaceCloudSource{workspace: w, url: d.cloud.base.String(), user: d.cloudUser})
						}
					}
					if d.showWorkspaceTemplates {
						label := "删除"
						if d.confirmCloudWorkspaceRemove == w.ID {
							label = "确认删除"
						}
						if ui.Button(c.Key("cloud-workspace-delete-"+w.ID), label).Label(label + "云工作区：" + w.Name).Disabled(d.busy).Clicked() {
							d.removeCloudWorkspace(w.ID)
						}
					}
				})
			}
		})
	}
	if !d.showWorkspaceTemplates && d.workspaceRestore == nil {
		return
	}
	if d.workspaceTemplatesStatus != "" {
		ui.Text(c, d.workspaceTemplatesStatus).FontSize(12).Wrap()
	}
	if d.workspaceRestore != nil {
		if ui.Button(c.Key("workspace-restore-cancel"), "取消恢复").Disabled(d.workspaceRestore.cancelling).Clicked() {
			d.cancelWorkspaceRestore()
		}
		return
	}
	if d.terminalGroupFor(d.currentSession) == nil {
		for _, session := range d.sessions {
			if session.fileOnly {
				ui.Text(c, "终端工作区不包含 SFTP 文件连接。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
				break
			}
		}
	}
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.TextInput(c.Key("workspace-template-name"), &d.workspaceTemplateName).Label("新工作区名称").Placeholder("工作区名称").Grow(1)
		if ui.Button(c.Key("workspace-template-save"), "保存当前会话").Disabled(d.busy || d.sessionCount(false) == 0).Clicked() {
			d.saveWorkspaceTemplate()
		}
		if ui.Button(c.Key("workspace-group-create"), "组成工作区").Disabled(d.busy || d.sessionCount(false) < 2 || d.terminalGroupFor(d.currentSession) != nil).Clicked() {
			d.createGroupFromCurrentSessions()
		}
	})
	selectionLabel := "选择终端"
	if d.showTerminalGroupSelection {
		selectionLabel = "使用全部终端"
	}
	ui.Row(c).FillWidth().Children(func() {
		if ui.Button(c.Key("workspace-group-select"), selectionLabel).Disabled(d.busy || d.sessionCount(false) < 2 || d.terminalGroupFor(d.currentSession) != nil).Clicked() {
			d.showTerminalGroupSelection = !d.showTerminalGroupSelection
			d.terminalGroupSelected = nil
			if d.showTerminalGroupSelection {
				d.terminalGroupSelected = map[int]bool{}
				for _, session := range d.sessions {
					if d.terminalGroupFor(session.id) == nil && session.term != nil && !terminalEnded(session.term) {
						d.terminalGroupSelected[session.id] = true
					}
				}
			}
		}
	})
	if d.showTerminalGroupSelection {
		ui.Scroll(c.Key("workspace-group-selection-list")).Label("选择工作区终端").Height(min(float32(150), float32(len(d.sessions)*38))).FillWidth().Children(func() {
			for _, entry := range d.sessions {
				session := entry
				if d.terminalGroupFor(session.id) != nil || session.term == nil || terminalEnded(session.term) {
					continue
				}
				selected := d.terminalGroupSelected[session.id]
				if ui.Checkbox(c.Key(fmt.Sprintf("workspace-group-select-%d", session.id)), &selected, sessionDisplayLabel(session)).Label(fmt.Sprintf("选择终端：%s · %d", sessionDisplayLabel(session), session.id)).Changed() {
					if d.terminalGroupSelected == nil {
						d.terminalGroupSelected = map[int]bool{}
					}
					if selected {
						d.terminalGroupSelected[session.id] = true
					} else {
						delete(d.terminalGroupSelected, session.id)
					}
				}
			}
		})
	}
}
