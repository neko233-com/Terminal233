package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

type groupQuickResult struct {
	session workspaceRestoredSession
	err     error
}

func groupCloudSnapshotCurrent(expected string, groups cloudGroupSnapshot, accounts []cloudAccount) error {
	if !cloudGroupAccountsMatch(groups, accounts) || groups.Revision != expected {
		return errors.New("云端分组已变化，请刷新后重试")
	}
	return nil
}

// The group action always includes descendants, regardless of search and
// tag filters. Cloud keys are trust decisions made on this device only.
func (d *desktop) groupQuickTargets(group string, cloud bool) ([]workspaceRestoreTarget, []string, error) {
	if group == "" {
		return nil, nil, errors.New("请选择分组")
	}
	var entries []workspaceRestoreTarget
	var failures []string
	count := 0
	if cloud {
		if d.cloud == nil || d.cloud.base == nil || d.cloud.token == "" || d.cloudUser == "" || !d.cloud.cloudGroups || d.cloudGroupsStale || d.cloudListStale || (d.cloud.teamID != "" && d.teamListStale) || !cloudGroupAccountsMatch(d.cloudGroupSnapshot, d.accounts) {
			return nil, nil, errors.New("请先刷新云端连接与分组")
		}
		for _, a := range d.accounts {
			if groupWithin(d.cloudGroupSnapshot.Assignments[a.ID], group) {
				count++
			}
		}
	} else {
		for _, p := range d.profiles {
			if groupWithin(p.Group, group) {
				count++
			}
		}
	}
	if count == 0 {
		return nil, nil, errors.New("此分组没有主机")
	}
	if d.sessionCount(false)+count > maxWorkspaceSessions {
		return nil, nil, fmt.Errorf("分组包含 %d 个主机，最多同时打开 %d 个终端", count, maxWorkspaceSessions)
	}
	if cloud {
		for _, a := range d.accounts {
			if !groupWithin(d.cloudGroupSnapshot.Assignments[a.ID], group) {
				continue
			}
			fp, err := d.trustedWorkspaceCloudFingerprint(a)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s：%v", a.Label, err))
				continue
			}
			e := workspaceTemplateEntry{Kind: "cloud", Label: a.Label, Host: a.Host, Port: strconv.Itoa(a.Port), User: a.Username, Fingerprint: fp, CloudURL: d.cloud.base.String(), CloudUser: d.cloudUser, CloudTeam: d.cloud.teamID, CloudID: a.ID}
			if _, err = (connectionSettings{Host: e.Host, Port: e.Port, User: e.User}).address(); err != nil || e.CloudID == "" {
				failures = append(failures, fmt.Sprintf("%s：主机信息无效", a.Label))
				continue
			}
			entries = append(entries, workspaceRestoreTarget{entry: e, label: a.Label, settings: connectionSettings{Host: e.Host, Port: e.Port, User: e.User, Fingerprint: fp}})
		}
	} else {
		for _, p := range d.profiles {
			if !groupWithin(p.Group, group) {
				continue
			}
			settings, err := d.profileCredential(p)
			if err == nil {
				settings, err = d.resolveSavedRoute(settings)
			}
			if err == nil {
				_, err = settings.address()
			}
			if err == nil && !strings.HasPrefix(settings.Fingerprint, "SHA256:") {
				err = errors.New("请先确认主机指纹")
			}
			if err == nil && !settings.canUseStoredAuth() {
				err = errors.New("没有保存可用凭据")
			}
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s：%v", p.Label, err))
				continue
			}
			e := workspaceTemplateEntry{Kind: "profile", Label: p.Label, Group: p.Group, Host: settings.Host, Port: settings.Port, User: settings.User, KeyPath: settings.KeyPath, Fingerprint: settings.Fingerprint, JumpRoute: settings.savedRouteSignature()}
			label := p.Label
			if label == "" {
				label = settings.User + "@" + settings.Host
			}
			entries = append(entries, workspaceRestoreTarget{entry: e, settings: settings, label: label, sourceProfileID: string(p.ID)})
		}
	}
	return entries, failures, nil
}

// Up to four handshakes run in parallel. Each failure is independent; a
// cancellation closes all not-yet-published sessions before returning.
func connectGroupTargets(ctx context.Context, targets []workspaceRestoreTarget, open func(workspaceRestoreTarget) (workspaceRestoredSession, error), closeSession func(workspaceRestoredSession), progress func(int, int)) ([]groupQuickResult, error) {
	type job struct {
		index  int
		target workspaceRestoreTarget
	}
	type result struct {
		index int
		value groupQuickResult
	}
	jobs := make(chan job, len(targets))
	results := make(chan result, len(targets))
	for i, target := range targets {
		jobs <- job{i, target}
	}
	close(jobs)
	var wg sync.WaitGroup
	for i := 0; i < min(4, len(targets)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := ctx.Err(); err != nil {
					results <- result{j.index, groupQuickResult{err: err}}
					continue
				}
				item, err := open(j.target)
				if err == nil && (item.term == nil || terminalEnded(item.term)) {
					err = errors.New("终端已断开")
				}
				if err != nil {
					closeSession(item)
					item = workspaceRestoredSession{target: j.target}
				}
				results <- result{j.index, groupQuickResult{session: item, err: err}}
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()
	out := make([]groupQuickResult, len(targets))
	done := 0
	for result := range results {
		out[result.index] = result.value
		done++
		if progress != nil {
			progress(done, len(targets))
		}
	}
	if err := ctx.Err(); err != nil {
		for _, result := range out {
			if result.err == nil {
				closeSession(result.session)
			}
		}
		return nil, err
	}
	return out, nil
}

func (d *desktop) quickConnectGroup(group string, cloud bool) {
	if d.busy || d.win == nil {
		return
	}
	if !d.closeConnectionForNavigation() {
		return
	}
	d.groupConnectFailures, d.showGroupConnectFailures = nil, false
	targets, failures, err := d.groupQuickTargets(group, cloud)
	if err != nil {
		d.status = err.Error()
		return
	}
	if len(targets) == 0 {
		d.groupConnectFailures = failures
		d.status = "无法连接：" + strings.Join(failures, "；")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	op := &workspaceRestore{cancel: cancel, groupQuick: true}
	d.workspaceRestore = op
	d.busy = true
	d.status = fmt.Sprintf("正在连接分组 %s（0/%d）…", group, len(targets))
	options := d.terminalOptions()
	win := d.win
	originalClient := d.cloud
	expectedGroupRevision := d.cloudGroupSnapshot.Revision
	groupScope := ""
	var client *cloudClient
	if d.cloud != nil {
		snapshot := *d.cloud
		client = &snapshot
		groupScope = snapshot.teamID
	}
	go func() {
		cache := map[string][]cloudAccount{}
		loadErrors := map[string]error{}
		cloudStateInvalid := false
		for i, target := range targets {
			if target.entry.Kind != "cloud" {
				continue
			}
			resolved, e := hydrateWorkspaceCloudTargets(ctx, []workspaceRestoreTarget{target}, func(team string) ([]cloudAccount, error) {
				if accounts, ok := cache[team]; ok {
					return accounts, loadErrors[team]
				}
				if client == nil {
					return nil, errors.New("请先登录同步服务")
				}
				scope := *client
				scope.teamID = team
				accounts, err := scope.list()
				if err == nil {
					var groups cloudGroupSnapshot
					groups, err = scope.listCloudGroups()
					if err == nil {
						err = groupCloudSnapshotCurrent(expectedGroupRevision, groups, accounts)
					}
				}
				if err != nil {
					cloudStateInvalid = true
				}
				cache[team], loadErrors[team] = accounts, err
				return accounts, err
			})
			if e != nil {
				failures = append(failures, fmt.Sprintf("%s：%v", target.label, e))
				targets[i].entry.Kind = "unavailable"
			} else {
				targets[i] = resolved[0]
			}
		}
		valid := make([]workspaceRestoreTarget, 0, len(targets))
		for _, target := range targets {
			if target.entry.Kind != "unavailable" {
				valid = append(valid, target)
			}
		}
		closeSession := func(item workspaceRestoredSession) {
			if item.term != nil {
				_ = item.term.Close()
			} else if item.shell != nil {
				_ = item.shell.Close()
			}
		}
		results, failure := connectGroupTargets(ctx, valid, func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
			shell, term, err := openSSHSessionAtThroughContext(ctx, target.settings, options, "", nil)
			return workspaceRestoredSession{target: target, shell: shell, term: term}, err
		}, closeSession, func(done, total int) {
			if !op.closed.Load() {
				win.Update(func() {
					if d.workspaceRestore == op && !op.closed.Load() && !op.cancelling {
						d.status = fmt.Sprintf("正在连接分组 %s（%d/%d）…", group, done, total)
					}
				})
			}
		})
		if op.closed.Load() {
			for _, result := range results {
				if result.err == nil {
					closeSession(result.session)
				}
			}
			cancel()
			return
		}
		win.Update(func() {
			defer cancel()
			if op.closed.Load() || d.workspaceRestore != op {
				for _, result := range results {
					if result.err == nil {
						closeSession(result.session)
					}
				}
				return
			}
			if failure == nil && ctx.Err() != nil {
				for _, result := range results {
					if result.err == nil {
						closeSession(result.session)
					}
				}
				failure = ctx.Err()
			}
			d.workspaceRestore = nil
			d.busy = false
			if cloudStateInvalid && d.cloud == originalClient && d.cloud != nil && d.cloud.teamID == groupScope {
				d.cloudListStale, d.cloudGroupsStale = true, true
			}
			if failure != nil {
				d.status = "已取消分组连接，未添加会话"
				return
			}
			opened := 0
			for _, result := range results {
				if result.err != nil {
					failures = append(failures, fmt.Sprintf("%s：%v", result.session.target.label, result.err))
					continue
				}
				item := result.session
				if terminalEnded(item.term) {
					closeSession(item)
					failures = append(failures, item.target.label+"：终端已断开")
					continue
				}
				if opened == 0 {
					d.splitSession, d.splitFirstSession, d.splitFocusedSession = 0, 0, 0
				}
				d.addSession(item.term, item.shell, item.target.settings, item.target.label)
				opened++
				s := d.sessions[len(d.sessions)-1]
				s.sourceProfileID = item.target.sourceProfileID
				if e := item.target.entry; e.Kind == "cloud" {
					s.cloudOriginURL, s.cloudOriginUser, s.cloudOriginTeam, s.cloudOriginID = e.CloudURL, e.CloudUser, e.CloudTeam, e.CloudID
					s.cloudSources = map[string]string{e.CloudTeam: e.CloudID}
					s.cloudVersions = map[string]string{e.CloudTeam: item.target.cloudVersion}
				}
			}
			if opened > 0 {
				d.newTabOpen = false
				d.workspace = "terminal"
				d.pendingTerminalFocus = d.currentSession
			}
			d.status = fmt.Sprintf("分组连接完成：%d 个已连接", opened)
			d.groupConnectFailures = append([]string(nil), failures...)
			if len(failures) > 0 {
				d.status += fmt.Sprintf("，%d 个失败：%s", len(failures), strings.Join(failures, "；"))
			}
		})
	}()
}

func (d *desktop) groupConnectionFailuresButton(c *ui.Context) {
	button := ui.Button(c.Key("group-connect-failures"), fmt.Sprintf("%d 个失败", len(d.groupConnectFailures))).Label("查看分组连接失败详情")
	if button.Clicked() {
		d.showGroupConnectFailures = !d.showGroupConnectFailures
	}
	ui.PopoverBase(c, button, &d.showGroupConnectFailures, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(360).Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() {
				ui.Text(c, "未能连接的主机").FontSize(13).Bold()
				ui.Scroll(c.Key("group-connect-failure-list")).Label("分组连接失败详情").Height(min(float32(250), float32(len(d.groupConnectFailures)*42))).FillWidth().Children(func() {
					for _, failure := range d.groupConnectFailures {
						ui.Text(c, failure).FontSize(12).Wrap()
					}
				})
			})
		})
	})
}
