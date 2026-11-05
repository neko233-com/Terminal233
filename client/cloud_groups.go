package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
)

type cloudGroupSnapshot struct {
	Groups      []string          `json:"groups"`
	Assignments map[string]string `json:"assignments"`
	AccountIDs  []string          `json:"accountIds"`
	Revision    string            `json:"revision"`
}

type cloudGroupDragID string

func (c *cloudClient) cloudGroupsPath() string {
	if c.teamID != "" {
		return "/api/v1/teams/" + url.PathEscape(c.teamID) + "/groups"
	}
	return "/api/v1/groups"
}

func validCloudGroupSnapshot(state cloudGroupSnapshot) bool {
	if len(state.Revision) != 64 || len(state.Groups) > 1000 || len(state.Assignments) > 1000 || state.AccountIDs == nil || len(state.AccountIDs) > 1000 {
		return false
	}
	if _, err := hex.DecodeString(state.Revision); err != nil {
		return false
	}
	groups := make(map[string]bool, len(state.Groups))
	for _, path := range state.Groups {
		if len(path) > 512 || len(strings.Split(path, "/")) > 8 || groups[path] {
			return false
		}
		for _, part := range strings.Split(path, "/") {
			if !validGroupName(part) {
				return false
			}
		}
		groups[path] = true
	}
	for path := range groups {
		if i := strings.LastIndex(path, "/"); i > 0 && !groups[path[:i]] {
			return false
		}
	}
	ids := make(map[string]bool, len(state.AccountIDs))
	for _, id := range state.AccountIDs {
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\\x00") || ids[id] {
			return false
		}
		ids[id] = true
	}
	for id, path := range state.Assignments {
		if !ids[id] || !groups[path] {
			return false
		}
	}
	return true
}

func (c *cloudClient) listCloudGroups() (cloudGroupSnapshot, error) {
	var result cloudGroupSnapshot
	if !c.cloudGroups || c.token == "" {
		return result, errors.New("当前服务不支持云端分组")
	}
	if err := c.request(http.MethodGet, c.cloudGroupsPath(), nil, &result); err != nil {
		return result, err
	}
	if !validCloudGroupSnapshot(result) {
		return cloudGroupSnapshot{}, errors.New("云端分组响应无效")
	}
	return result, nil
}

func (c *cloudClient) replaceCloudGroups(state cloudGroupSnapshot) (cloudGroupSnapshot, error) {
	var result cloudGroupSnapshot
	if !c.cloudGroups || c.token == "" || !validCloudGroupSnapshot(state) {
		return result, errors.New("云端分组状态无效")
	}
	input := map[string]any{"groups": state.Groups, "assignments": state.Assignments, "expectedRevision": state.Revision}
	if err := c.request(http.MethodPut, c.cloudGroupsPath(), input, &result); err != nil {
		return result, err
	}
	if !validCloudGroupSnapshot(result) {
		return cloudGroupSnapshot{}, errors.New("云端分组保存结果无效")
	}
	return result, nil
}

func (d *desktop) invalidateCloudGroups() {
	if d.cloud != nil && d.cloud.cloudGroups {
		d.cloudGroupsStale = true
	}
}

func cloudGroupAccountsMatch(state cloudGroupSnapshot, accounts []cloudAccount) bool {
	if !validCloudGroupSnapshot(state) {
		return false
	}
	ids := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		ids[a.ID] = true
	}
	if len(ids) != len(state.AccountIDs) || len(ids) != len(accounts) {
		return false
	}
	for _, id := range state.AccountIDs {
		if !ids[id] {
			return false
		}
	}
	return true
}

func (d *desktop) setCloudGroupState(state cloudGroupSnapshot, err error) {
	if err == nil && d.cloud != nil && d.cloud.cloudGroups && !cloudGroupAccountsMatch(state, d.accounts) {
		err = errors.New("连接列表与分组不一致")
	}
	if d.cloud == nil || !d.cloud.cloudGroups || err != nil || !validCloudGroupSnapshot(state) {
		d.cloudGroupSnapshot, d.cloudGroupsStale = cloudGroupSnapshot{}, true
		if err != nil {
			d.cloudStatus = "云端分组加载失败，请刷新：" + err.Error()
		}
		return
	}
	d.cloudGroupSnapshot, d.cloudGroupsStale = state, false
}

func (d *desktop) cloudGroupTree() ([]string, map[string]int) {
	groups := append([]string(nil), d.cloudGroupSnapshot.Groups...)
	sort.Strings(groups)
	counts := make(map[string]int)
	for _, path := range d.cloudGroupSnapshot.Assignments {
		for path != "" {
			counts[path]++
			i := strings.LastIndex(path, "/")
			if i <= 0 {
				break
			}
			path = path[:i]
		}
	}
	return groups, counts
}

func (d *desktop) cloudGroupOperation(action, accountID, target string) {
	if d.busy || d.cloud == nil || !d.cloud.cloudGroups || d.cloudGroupsStale || d.cloudListStale || !d.canWriteCloudScope() {
		return
	}
	if action == "remove" && !d.confirmGroupDelete {
		return
	}
	old := d.profileFilter
	state := cloudGroupSnapshot{Groups: append([]string{}, d.cloudGroupSnapshot.Groups...), Assignments: make(map[string]string), AccountIDs: append([]string{}, d.cloudGroupSnapshot.AccountIDs...), Revision: d.cloudGroupSnapshot.Revision}
	for id, group := range d.cloudGroupSnapshot.Assignments {
		state.Assignments[id] = group
	}
	exists := func(path string) bool {
		for _, p := range state.Groups {
			if p == path {
				return true
			}
		}
		return false
	}
	switch action {
	case "create":
		name := strings.TrimSpace(d.groupInput)
		if !validGroupName(name) {
			d.status = "请输入有效的分组名称。"
			return
		}
		target = name
		if old != "" {
			target = old + "/" + name
		}
		if exists(target) || len(strings.Split(target, "/")) > 8 || len(state.Groups) >= 1000 {
			d.status = "分组已存在或超过数量、层级限制。"
			return
		}
		state.Groups = append(state.Groups, target)
	case "rename", "remove":
		if old == "" || !exists(old) {
			d.status = "请先选择云端分组。"
			return
		}
		parent := ""
		if i := strings.LastIndex(old, "/"); i >= 0 {
			parent = old[:i]
		}
		target = parent
		if action == "rename" {
			name := strings.TrimSpace(d.groupInput)
			if !validGroupName(name) {
				d.status = "请输入有效的新分组名称。"
				return
			}
			target = name
			if parent != "" {
				target = parent + "/" + name
			}
			if target == old {
				d.status = "分组名称未改变。"
				return
			}
		}
		mapPath := func(path string) string {
			if !groupWithin(path, old) {
				return path
			}
			suffix := strings.TrimPrefix(path[len(old):], "/")
			if suffix == "" {
				return target
			}
			if target == "" {
				return suffix
			}
			return target + "/" + suffix
		}
		seen := make(map[string]bool)
		groups := make([]string, 0, len(state.Groups))
		for _, p := range state.Groups {
			if action == "remove" && p == old {
				continue
			}
			next := mapPath(p)
			if next == "" {
				continue
			}
			if seen[next] {
				d.status = "目标分组已存在，无法安全合并。"
				return
			}
			seen[next] = true
			groups = append(groups, next)
		}
		state.Groups = groups
		for id, path := range state.Assignments {
			next := mapPath(path)
			if next == "" {
				delete(state.Assignments, id)
			} else {
				state.Assignments[id] = next
			}
		}
	case "move":
		if target != "" && !exists(target) {
			d.status = "目标云端分组不存在。"
			return
		}
		found := false
		for _, a := range d.accounts {
			if a.ID == accountID {
				found = true
				break
			}
		}
		if !found {
			d.status = "云端连接不存在，请刷新。"
			return
		}
		if target == "" {
			delete(state.Assignments, accountID)
		} else {
			state.Assignments[accountID] = target
		}
	default:
		return
	}
	if !validCloudGroupSnapshot(state) {
		d.status = "云端分组名称或数量无效。"
		return
	}
	client, scope := d.cloud, d.cloud.teamID
	d.confirmGroupDelete = false
	d.busy = true
	d.status = "正在保存云端分组…"
	go func() {
		result, err := client.replaceCloudGroups(state)
		d.win.Update(func() {
			d.busy = false
			if d.cloud != client || client.teamID != scope {
				return
			}
			if err != nil {
				d.cloudGroupsStale = true
				var e *cloudHTTPError
				if errors.As(err, &e) && e.Code == "group_conflict" {
					d.status = "云端分组或连接已变化；本次未保存，请刷新。"
				} else {
					d.status = "云端分组请求未确认，可能已生效；请刷新核对：" + err.Error()
				}
			} else {
				d.setCloudGroupState(result, nil)
				if d.cloudGroupsStale {
					d.status = "分组已保存，但返回的连接列表已变化，请刷新。"
					d.cloudStatus = d.status
					return
				}
				d.groupInput = ""
				d.cloudFoldedGroups = nil
				if action != "move" {
					d.profileFilter = target
				}
				d.status = "云端分组已保存"
			}
			d.cloudStatus = d.status
		})
	}()
}

func (d *desktop) cloudGroupControls(c *ui.Context) {
	if d.cloud == nil || !d.cloud.cloudGroups {
		ui.Text(c, "当前同步服务不支持云端分组。").Wrap()
		return
	}
	if d.cloudGroupsStale {
		ui.Text(c, "云端分组需要刷新后才能修改。").Wrap()
		if ui.Button(c.Key("cloud-groups-refresh"), "刷新云端连接和分组").Disabled(d.busy).Clicked() {
			d.cloudRefresh()
		}
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.End).Children(func() {
		ui.TextInput(c.Key("cloud-group-name"), &d.groupInput).Label("云端分组名称").Grow(1).OnChange(func() { d.confirmGroupDelete = false })
		label := "创建云端顶级分组"
		if d.profileFilter != "" {
			label = "创建云端子分组"
		}
		disabled := d.busy || d.cloudGroupsStale || d.cloudListStale || !d.canWriteCloudScope()
		if ui.Button(c.Key("cloud-group-create"), label).Disabled(disabled).Clicked() {
			d.cloudGroupOperation("create", "", "")
		}
		if d.profileFilter != "" {
			if ui.Button(c.Key("cloud-group-rename"), "重命名云端分组").Disabled(disabled).Clicked() {
				d.cloudGroupOperation("rename", "", "")
			}
			if ui.Button(c.Key("cloud-group-remove"), "移除云端分组…").Disabled(disabled).Clicked() {
				d.confirmGroupDelete = true
			}
		}
	})
	if d.confirmGroupDelete && d.profileFilter != "" {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			ui.Text(c, fmt.Sprintf("移除云端分组 %s？连接和子分组将移到上级。", d.profileFilter)).Wrap()
			if ui.Button(c.Key("cloud-group-remove-confirm"), "确认移除云端分组").Disabled(d.busy).Clicked() {
				d.cloudGroupOperation("remove", "", "")
			}
			if ui.Button(c.Key("cloud-group-remove-cancel"), "取消移除云端分组").Clicked() {
				d.confirmGroupDelete = false
			}
		})
	}
}
