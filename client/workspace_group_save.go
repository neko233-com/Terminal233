package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
)

func cloneSavedWorkspace(w savedWorkspace) savedWorkspace {
	w.Entries = slices.Clone(w.Entries)
	w.RowPerc = slices.Clone(w.RowPerc)
	w.ColumnPerc = slices.Clone(w.ColumnPerc)
	return w
}

func cloneCloudWorkspace(w cloudWorkspace) cloudWorkspace {
	w.Entries = slices.Clone(w.Entries)
	w.RowPerc = slices.Clone(w.RowPerc)
	w.ColumnPerc = slices.Clone(w.ColumnPerc)
	return w
}

// Keep this fingerprint in memory only. It contains no passwords and reflects
// the group state rather than relying on repaint or mouse-event timing.
func (d *desktop) workspaceGroupSignature(g *terminalGroup) [32]byte {
	type member struct {
		ID, JumpSession                                                                    int
		Label, TabName, Directory, FileDirectory, Host, Port, User, Fingerprint, JumpRoute string
	}
	type state struct {
		Name, SplitLayout string
		Focus             int
		Grid              bool
		Members           []member
		Rows, Columns     [maxWorkspaceSessions]float32
	}
	value := state{Name: g.name, SplitLayout: d.splitLayout, Focus: g.focused, Grid: g.grid, Rows: g.rowPerc, Columns: g.columnPerc}
	sessions := d.terminalGroupSessions(g)
	for _, s := range sessions {
		dir := s.fileDir
		if s.id == d.currentSession {
			dir = d.fileDir
		}
		jumpSession := 0
		if s.jumpParent != nil {
			jumpSession = s.jumpParent.id
		}
		value.Members = append(value.Members, member{ID: s.id, JumpSession: jumpSession, Label: s.label, TabName: g.tabNames[s.id], Directory: sessionTerminalDirectory(s), FileDirectory: dir, Host: s.active.Host, Port: s.active.Port, User: s.active.User, Fingerprint: s.active.Fingerprint, JumpRoute: s.active.savedRouteSignature()})
	}
	// Rendering fills unset split percentages with the following defaults.
	// Normalize them here so the first paint does not falsely mark a template dirty.
	columns := 2
	if len(sessions) > 4 {
		columns = 3
	}
	if len(sessions) > 9 {
		columns = 4
	}
	rows := (len(sessions) + columns - 1) / columns
	for row := 0; row < rows; row++ {
		if row < rows-1 && value.Rows[row] == 0 {
			value.Rows[row] = 100 / float32(rows-row)
		}
		start, end := row*columns, min(row*columns+columns, len(sessions))
		for i := start; i < end-1; i++ {
			k := row*4 + i - start
			if value.Columns[k] == 0 {
				value.Columns[k] = 100 / float32(end-i)
			}
		}
	}
	data, _ := json.Marshal(value)
	return sha256.Sum256(data)
}

func (d *desktop) workspaceGroupDirty(g *terminalGroup) bool {
	return g != nil && ((g.savedLocal == nil && g.savedCloud == nil) || g.pendingCloud != nil || d.workspaceGroupSignature(g) != g.savedSignature)
}

type pendingWorkspaceSync struct {
	base, attempted *cloudWorkspace
	next            cloudWorkspace
	url, user       string
	cloudOnly       bool
	signature       [32]byte
}

// The previous attempt may have committed even if its response was lost.
// Accept that exact value on retry, but never overwrite a third-party edit.
func preparePendingWorkspaceSync(state cloudWorkspaceSnapshot, p *pendingWorkspaceSync) (cloudWorkspaceSnapshot, bool, error) {
	prior := p.base
	for _, existing := range state.Items {
		if existing.ID != p.next.ID {
			continue
		}
		if reflect.DeepEqual(existing, p.next) {
			return state, true, nil
		}
		if p.attempted != nil && reflect.DeepEqual(existing, *p.attempted) {
			prior = p.attempted
		}
		break
	}
	next, err := replaceCloudGroupSnapshot(state, prior, p.next)
	return next, false, err
}

func replaceLocalGroupTemplate(path string, prior *savedWorkspace, next savedWorkspace) (workspaceTemplatesFile, error) {
	return updateWorkspaceTemplates(path, func(file *workspaceTemplatesFile) error {
		index := -1
		for i, existing := range file.Items {
			if prior == nil && existing.ID == next.ID {
				return errors.New("工作区标识已存在，请重试")
			}
			if prior != nil && existing.ID == prior.ID {
				index = i
				if !reflect.DeepEqual(existing, *prior) {
					return errors.New("工作区已被其他窗口修改，请刷新后另存")
				}
				continue
			}
			if strings.EqualFold(existing.Name, next.Name) {
				return errors.New("工作区名称已存在")
			}
		}
		if prior != nil && index < 0 {
			return errors.New("工作区已被其他窗口删除，请刷新后另存")
		}
		if index < 0 {
			file.Items = append(file.Items, next)
		} else {
			file.Items[index] = next
		}
		return nil
	})
}

// Replace only the expected version. The server additionally checks Revision.
func replaceCloudGroupSnapshot(state cloudWorkspaceSnapshot, prior *cloudWorkspace, next cloudWorkspace) (cloudWorkspaceSnapshot, error) {
	state.Items = append([]cloudWorkspace(nil), state.Items...)
	index := -1
	for i, existing := range state.Items {
		if existing.ID == next.ID {
			if reflect.DeepEqual(existing, next) {
				return state, nil
			}
			if prior == nil || !reflect.DeepEqual(existing, *prior) {
				return cloudWorkspaceSnapshot{}, errors.New("云工作区已修改，请刷新后核对")
			}
			index = i
			continue
		}
		if strings.EqualFold(existing.Name, next.Name) {
			return cloudWorkspaceSnapshot{}, errors.New("云端工作区名称已存在")
		}
	}
	if prior != nil && index < 0 {
		return cloudWorkspaceSnapshot{}, errors.New("云工作区已删除，请刷新后核对")
	}
	if index < 0 {
		state.Items = append(state.Items, next)
	} else {
		state.Items[index] = next
	}
	return state, nil
}

func (d *desktop) saveActiveWorkspaceGroup(g *terminalGroup) {
	if d.busy || g == nil || d.terminalGroupFor(d.currentSession) != g {
		return
	}
	if !d.workspaceGroupDirty(g) {
		d.status = "工作区没有未保存的更改"
		return
	}
	if p := g.pendingCloud; p != nil {
		current := d.workspaceGroupSignature(g)
		if (!p.cloudOnly && current == g.savedSignature) || (p.cloudOnly && current == p.signature) {
			d.syncPendingWorkspace(g)
			return
		}
	}
	state, err := d.workspaceSnapshot(g.name)
	if err != nil {
		d.status = "工作区未保存：" + err.Error()
		return
	}
	signature := d.workspaceGroupSignature(g)
	if g.savedCloud != nil {
		if d.cloud == nil || d.cloud.base == nil || d.cloud.base.String() != g.savedCloudURL || d.cloudUser != g.savedUser {
			d.status = "请先登录该工作区所属云账号"
			return
		}
		state.ID = g.savedCloud.ID
		portable, err := portableWorkspace(state, d.cloud, d.cloudUser)
		if err != nil {
			d.status = "工作区未保存：" + err.Error()
			return
		}
		if g.pendingCloud == nil {
			base := cloneCloudWorkspace(*g.savedCloud)
			g.pendingCloud = &pendingWorkspaceSync{base: &base, url: g.savedCloudURL, user: g.savedUser, cloudOnly: true}
		}
		g.pendingCloud.next, g.pendingCloud.signature = portable, signature
		d.syncPendingWorkspace(g)
		return
	}
	previous := g.savedLocal
	if previous != nil {
		state.ID = previous.ID
	}
	saved, err := replaceLocalGroupTemplate(d.workspaceTemplatesPath, previous, state)
	if err != nil {
		d.status = "工作区未保存：" + err.Error()
		return
	}
	d.workspaceTemplates = saved.Items
	stored := cloneSavedWorkspace(state)
	g.savedLocal, g.savedSignature = &stored, signature
	d.status = "工作区已保存"
	if g.pendingCloud != nil {
		origin, e := newCloudClient(g.pendingCloud.url)
		if e != nil {
			g.pendingCloud.next = cloudWorkspace{}
			d.status = "已保存到本机；原云账号不可用"
			return
		}
		// Build the pending value offline; the actual service capability is checked before the write.
		origin.cloudWorkspaces16, origin.cloudWorkspaceNames = true, true
		portable, e := portableWorkspace(state, origin, g.pendingCloud.user)
		if e != nil {
			g.pendingCloud.next = cloudWorkspace{}
			d.status = "已保存到本机；" + e.Error()
			return
		}
		g.pendingCloud.next = portable
		d.syncPendingWorkspace(g)
		return
	}
	if d.cloud != nil && d.cloud.base != nil && d.cloud.cloudWorkspaces && d.win != nil {
		var old *cloudWorkspace
		if previous != nil {
			if value, e := portableWorkspace(*previous, d.cloud, d.cloudUser); e == nil {
				old = &value
			}
		}
		portable, e := portableWorkspace(state, d.cloud, d.cloudUser)
		if e != nil {
			d.status = "已保存到本机；" + e.Error()
			return
		}
		if g.pendingCloud == nil {
			g.pendingCloud = &pendingWorkspaceSync{base: old, url: d.cloud.base.String(), user: d.cloudUser}
		}
		if g.pendingCloud.url != d.cloud.base.String() || g.pendingCloud.user != d.cloudUser {
			d.status = "已保存到本机；请登录原云账号重试同步"
			return
		}
		g.pendingCloud.next = portable
		d.syncPendingWorkspace(g)
	}
}

func (d *desktop) syncPendingWorkspace(g *terminalGroup) {
	p := g.pendingCloud
	if p == nil || d.busy || d.win == nil {
		return
	}
	if p.next.ID == "" {
		d.status = "工作区包含无法同步的会话，请调整后重试"
		return
	}
	if d.cloud == nil || d.cloud.base == nil || d.cloud.base.String() != p.url || d.cloudUser != p.user {
		d.status = "请登录原云账号重试同步"
		return
	}
	if d.cloudWorkspacesStale {
		d.status = "请先刷新云工作区，再重试同步"
		return
	}
	state, alreadySaved, err := preparePendingWorkspaceSync(d.cloudWorkspaceSnapshot, p)
	if err != nil {
		d.status = err.Error()
		if !p.cloudOnly {
			d.status = "本机已保存；" + d.status
		}
		return
	}
	if alreadySaved {
		finishPendingWorkspaceSync(g, p)
		d.status = "工作区已同步"
		return
	}
	attempt := cloneCloudWorkspace(p.next)
	p.attempted = &attempt
	c := d.cloud
	d.busy, d.status = true, "正在同步工作区…"
	go func() {
		result, err := c.replaceCloudWorkspaces(state)
		d.win.Update(func() {
			d.busy = false
			if d.cloud != c {
				return
			}
			if err != nil {
				d.cloudWorkspacesStale = true
				d.status = "同步未确认，请刷新后重试：" + err.Error()
				if !p.cloudOnly {
					d.status = "本机已保存；" + d.status
				}
				return
			}
			d.setCloudWorkspaceState(result, nil)
			for _, current := range d.terminalGroups {
				if current == g && g.pendingCloud == p {
					finishPendingWorkspaceSync(g, p)
					break
				}
			}
			d.status = "工作区已同步"
		})
	}()
}

func finishPendingWorkspaceSync(g *terminalGroup, p *pendingWorkspaceSync) {
	if p.cloudOnly {
		stored := cloneCloudWorkspace(p.next)
		g.savedCloud, g.savedSignature = &stored, p.signature
	}
	g.pendingCloud = nil
}
