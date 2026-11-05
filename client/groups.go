package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func groupWithin(group, parent string) bool {
	return group == parent || strings.HasPrefix(group, parent+"/")
}

// Groups formerly existed only as connection metadata. Derive their ancestors
// so existing installations retain their groups, including nested paths.
func (d *desktop) groupTree() ([]string, map[string]int) {
	set := make(map[string]bool)
	add := func(group string) {
		if group == "" {
			return
		}
		set[group] = true
		for i := strings.LastIndex(group, "/"); i > 0; i = strings.LastIndex(group, "/") {
			group = group[:i]
			set[group] = true
		}
	}
	for _, group := range d.profileGroups {
		add(group)
	}
	counts := make(map[string]int)
	for _, p := range d.profiles {
		add(p.Group)
		group := p.Group
		for group != "" {
			counts[group]++
			i := strings.LastIndex(group, "/")
			if i <= 0 {
				break
			}
			group = group[:i]
		}
	}
	groups := make([]string, 0, len(set))
	for group := range set {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups, counts
}

func validGroupName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && utf8.RuneCountInString(name) <= 64 && !strings.ContainsAny(name, "/\\\r\n\x00") && name != "." && name != ".."
}

func (d *desktop) groupExists(group string) bool {
	groups, _ := d.groupTree()
	for _, g := range groups {
		if g == group {
			return true
		}
	}
	return false
}

func (d *desktop) createGroup() {
	name := strings.TrimSpace(d.groupInput)
	if !validGroupName(name) {
		d.status = "分组名称无效；请输入不含斜杠的名称（最多 64 字符）。"
		return
	}
	path := name
	if d.profileFilter != "" {
		path = d.profileFilter + "/" + name
	}
	if strings.Count(path, "/") >= 8 || d.groupExists(path) {
		d.status = "分组已存在或超过 8 级。"
		return
	}
	groups, _ := d.groupTree()
	if len(groups) >= 10000 {
		d.status = "分组数量已达上限。"
		return
	}
	original := d.profileGroups
	d.profileGroups = append(append([]string(nil), original...), path)
	if err := d.saveProfiles(); err != nil {
		d.profileGroups = original
		d.status = "分组未创建：" + err.Error()
		return
	}
	d.profileFilter, d.groupInput, d.confirmGroupDelete = path, "", false
	d.profileFavorites, d.profileRecent = false, false
	d.status = "分组已创建：" + path
}

func (d *desktop) moveProfileToGroup(id profileID, group string) {
	if d.busy || !d.groupExists(group) {
		return
	}
	original := d.profiles
	items := append([]profile(nil), original...)
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if items[i].Group == group {
			return
		}
		items[i].Group = group
		d.profiles = items
		if err := d.saveProfiles(); err != nil {
			d.profiles = original
			d.status = "连接未移动：" + err.Error()
		} else {
			d.status = "连接已移动到分组：" + group
		}
		return
	}
}

// Renaming or removing a group updates the whole subtree in one store write.
// Removing a group promotes its children and direct connections to its parent.
func (d *desktop) changeGroup(remove bool) {
	old := d.profileFilter
	if old == "" || !d.groupExists(old) {
		d.status = "请先选择分组。"
		return
	}
	if remove && !d.confirmGroupDelete {
		return
	}
	parent := ""
	if i := strings.LastIndex(old, "/"); i >= 0 {
		parent = old[:i]
	}
	replacement := parent
	if !remove {
		name := strings.TrimSpace(d.groupInput)
		if !validGroupName(name) {
			d.status = "请输入有效的新分组名称。"
			return
		}
		replacement = name
		if parent != "" {
			replacement = parent + "/" + name
		}
		if replacement == old {
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
			return replacement
		}
		if replacement == "" {
			return suffix
		}
		return replacement + "/" + suffix
	}
	groups, _ := d.groupTree()
	seen := make(map[string]bool, len(groups))
	for _, g := range groups {
		if remove && g == old {
			continue
		}
		next := mapPath(g)
		if next == "" {
			continue
		}
		if seen[next] {
			d.status = "目标分组已存在，无法安全合并；请先重命名冲突分组。"
			return
		}
		seen[next] = true
	}
	originalGroups, originalProfiles := d.profileGroups, d.profiles
	nextGroups := make([]string, 0, len(originalGroups))
	for _, g := range originalGroups {
		next := mapPath(g)
		if next != "" && !(remove && g == old) {
			nextGroups = append(nextGroups, next)
		}
	}
	nextProfiles := append([]profile(nil), originalProfiles...)
	for i := range nextProfiles {
		nextProfiles[i].Group = mapPath(nextProfiles[i].Group)
	}
	d.profileGroups, d.profiles = nextGroups, nextProfiles
	if err := d.saveProfiles(); err != nil {
		d.profileGroups, d.profiles = originalGroups, originalProfiles
		d.confirmGroupDelete = false
		d.status = "分组未修改：" + err.Error()
		return
	}
	if !d.settingsFromCloud {
		d.profileGroup = mapPath(d.profileGroup)
	}
	d.profileFilter, d.groupInput, d.confirmGroupDelete = replacement, "", false
	if remove {
		d.status = "分组已移除，连接和子分组已移到上级。"
	} else {
		d.status = "分组已重命名：" + replacement
	}
}

func (d *desktop) groupControls(c *ui.Context) {
	ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.End).Children(func() {
		ui.TextInput(c.Key("group-name"), &d.groupInput).Label("分组名称").Placeholder("输入分组名称").Grow(1).OnChange(func() { d.confirmGroupDelete = false })
		label := "创建顶级分组"
		if d.profileFilter != "" {
			label = "创建子分组"
		}
		if ui.Button(c.Key("group-create"), label).Disabled(d.busy).Clicked() {
			d.createGroup()
		}
		if d.profileFilter != "" {
			if ui.Button(c.Key("group-rename"), "重命名当前分组").Disabled(d.busy).Clicked() {
				d.changeGroup(false)
			}
			if ui.Button(c.Key("group-remove"), "移除当前分组…").Disabled(d.busy).Clicked() {
				d.confirmGroupDelete = true
			}
		}
	})
	if d.confirmGroupDelete && d.profileFilter != "" {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			ui.Text(c, fmt.Sprintf("移除分组 %s？连接和子分组将移到上级。", d.profileFilter)).Wrap()
			if ui.Button(c.Key("group-remove-confirm"), "确认移除分组").Disabled(d.busy).Clicked() {
				d.changeGroup(true)
			}
			if ui.Button(c.Key("group-remove-cancel"), "取消移除分组").Clicked() {
				d.confirmGroupDelete = false
			}
		})
	}
}
