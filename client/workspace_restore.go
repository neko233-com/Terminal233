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
	if len(d.sessions)+len(w.Entries) > maxWorkspaceSessions {
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
			settings, err := profileCredential(matches[0])
			if err != nil {
				return nil, fmt.Errorf("第 %d 个主机：%w", i+1, err)
			}
			if settings.KeyPath == "" && settings.Password == "" {
				return nil, fmt.Errorf("第 %d 个主机没有保存可用凭据", i+1)
			}
			target.settings, target.label, target.sourceProfileID = settings, settings.User+"@"+settings.Host, string(matches[0].ID)
		case "cloud":
			if d.cloud == nil || d.cloud.base == nil || d.cloud.token == "" || d.cloud.base.String() != e.CloudURL || d.cloudUser != e.CloudUser {
				return nil, fmt.Errorf("第 %d 个主机需要先登录对应云账号", i+1)
			}
			target.settings = connectionSettings{Host: e.Host, Port: e.Port, User: e.User, Fingerprint: e.Fingerprint}
			target.label = e.User + "@" + e.Host
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
	d.workspaceTemplateName = ""
	d.workspaceTemplatesStatus = "工作区已保存"
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
	if d.busy || d.win == nil {
		return
	}
	if !d.prepareConnectionNavigation() {
		d.workspaceTemplatesStatus = d.status
		return
	}
	if err := workspaceTemplateUnchanged(d.workspaceTemplatesPath, w); err != nil {
		d.workspaceTemplatesStatus = err.Error()
		return
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
	d.workspaceTemplatesStatus = "正在恢复工作区…"
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
					shell, term, err = openSSHSession(target.settings, options)
				}
				return workspaceRestoredSession{target: target, shell: shell, term: term}, err
			}, closeSession, func(index int) {
				if !op.closed.Load() {
					win.Update(func() {
						if d.workspaceRestore == op && !op.closed.Load() && !op.cancelling {
							d.workspaceTemplatesStatus = fmt.Sprintf("正在恢复 %d/%d…", index, len(targets))
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
			if w.SplitFirst >= 0 && w.SplitSecond >= 0 {
				d.splitFirstSession = ids[w.SplitFirst]
				if ids[w.SplitFirst] == d.currentSession {
					d.splitSession = ids[w.SplitSecond]
				} else {
					d.splitSession = ids[w.SplitFirst]
				}
			}
			d.splitLayout = w.SplitLayout
			d.newTabOpen = false
			d.workspace = "terminal"
			d.pendingTerminalFocus = d.currentSession
			d.status = fmt.Sprintf("已恢复工作区 %s（%d 个会话）", w.Name, len(opened))
			d.workspaceTemplatesStatus = ""
		})
	}()
}

func (d *desktop) cancelWorkspaceRestore() {
	if d.workspaceRestore != nil && !d.workspaceRestore.cancelling {
		d.workspaceRestore.cancelling = true
		d.workspaceTemplatesStatus = "正在取消，等待当前连接结束…"
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
	title := "工作区"
	if len(d.workspaceTemplates) > 0 {
		title = fmt.Sprintf("工作区 · %d", len(d.workspaceTemplates))
	}
	if ui.Button(c.Key("workspace-templates-toggle"), title).Clicked() {
		d.showWorkspaceTemplates = !d.showWorkspaceTemplates
		d.confirmWorkspaceRemove = ""
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
	if ui.Button(c.Key("workspace-template-refresh"), "刷新工作区").Disabled(d.busy).Clicked() {
		saved, err := readWorkspaceTemplates(d.workspaceTemplatesPath)
		if err != nil {
			d.workspaceTemplatesStatus = "读取失败：" + err.Error()
		} else {
			d.workspaceTemplates, d.workspaceTemplatesStatus = saved.Items, "已刷新"
		}
	}
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.TextInput(c.Key("workspace-template-name"), &d.workspaceTemplateName).Label("新工作区名称").Placeholder("工作区名称").Grow(1)
		if ui.Button(c.Key("workspace-template-save"), "保存当前会话").Disabled(d.busy || len(d.sessions) == 0).Clicked() {
			d.saveWorkspaceTemplate()
		}
	})
	if len(d.workspaceTemplates) == 0 {
		return
	}
	ui.Scroll(c.Key("workspace-templates-list")).Label("已保存的工作区").Height(min(float32(160), float32(len(d.workspaceTemplates)*44))).FillWidth().Children(func() {
		for _, entry := range d.workspaceTemplates {
			w := entry
			ui.Row(c.Key("workspace-template-" + w.ID)).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				if ui.Button(c.Key("workspace-open-"+w.ID), w.Name).Label("打开工作区：" + w.Name).Grow(1).Justify(ui.Start).Disabled(d.busy).Clicked() {
					d.restoreWorkspaceTemplate(w)
				}
				label := "删除"
				if d.confirmWorkspaceRemove == w.ID {
					label = "确认删除"
				}
				if ui.Button(c.Key("workspace-delete-"+w.ID), label).Label(label + "工作区：" + w.Name).Disabled(d.busy).Clicked() {
					d.removeWorkspaceTemplate(w.ID)
				}
			})
		}
	})
}
