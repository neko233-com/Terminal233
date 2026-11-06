package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func groupWithin(group, parent string) bool {
	return group == parent || strings.HasPrefix(group, parent+"/")
}

func groupDepth(group string) int {
	return min(strings.Count(group, "/"), 8)
}

func groupLeaf(group string) string {
	if i := strings.LastIndex(group, "/"); i >= 0 && i+1 < len(group) {
		return group[i+1:]
	}
	return group
}

func groupsWithChildren(groups []string) map[string]bool {
	parents := make(map[string]bool)
	for _, group := range groups {
		if i := strings.LastIndex(group, "/"); i > 0 {
			parents[group[:i]] = true
		}
	}
	return parents
}

func groupHidden(group string, folded map[string]bool) bool {
	for i := strings.LastIndex(group, "/"); i > 0; i = strings.LastIndex(group, "/") {
		group = group[:i]
		if folded[group] {
			return true
		}
	}
	return false
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

// Group cards mirror the hierarchy shown in the sidebar. The current group
// displays its immediate children; hosts still include descendants.
func (d *desktop) profileGroupCards(c *ui.Context, cardWidth float32, draftBlocked bool) {
	groups, counts := d.groupTree()
	cloud := d.profileSource == "cloud"
	if cloud {
		groups, counts = d.cloudGroupTree()
	}
	var children []string
	for _, group := range groups {
		parent := ""
		if i := strings.LastIndex(group, "/"); i >= 0 {
			parent = group[:i]
		}
		if parent == d.profileFilter {
			children = append(children, group)
		}
	}
	if len(children) == 0 {
		return
	}
	ui.Text(c, "分组").FontSize(13).TextColor(c.Theme().TextMuted)
	ui.Row(c).FillWidth().Wrap().Gap(d.hostLayoutGap()).Children(func() {
		for _, name := range children {
			group := name
			card := ui.Button(c.Key("group-card-"+group), "").Label("打开分组：" + group).Tooltip(group).Width(cardWidth).Height(66).Padding(10).Gap(10).Justify(ui.Start).Background(c.Theme().Surface).BorderWidth(0).Radius(10).Disabled(d.busy || draftBlocked)
			if d.hostListView {
				card = card.Height(46).Padding(8, 12).Radius(6).Background(ui.Transparent)
				if card.Hovered() {
					card = card.Background(c.Theme().SurfaceHover)
				}
			}
			card.Children(func() {
				size, glyph := float32(38), float32(19)
				if d.hostListView {
					size, glyph = 28, 16
				}
				ui.Box(c).Width(size).Height(size).Shrink(0).Radius(8).Background(c.Theme().Accent).AlignItems(ui.Center).Justify(ui.Center).Children(func() { ui.Icon(c, fileIcon).Width(glyph).Height(glyph).TextColor(c.Theme().AccentText) })
				if d.hostListView {
					ui.Text(c, groupLeaf(group)).FontSize(13).Grow(1).SingleLine().Tooltip(group)
					ui.Text(c, fmt.Sprintf("%d 个连接", counts[group])).FontSize(12).TextColor(c.Theme().TextMuted).Width(76).TextAlign(ui.End)
				} else {
					ui.Column(c).Grow(1).Gap(3).Children(func() {
						ui.Text(c, groupLeaf(group)).FontSize(14).Bold().SingleLine()
						ui.Text(c, fmt.Sprintf("%d 个连接", counts[group])).FontSize(12).TextColor(c.Theme().TextMuted)
					})
				}
			})
			card.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("快速连接").Disabled(d.busy || draftBlocked || counts[group] == 0).Chosen() {
					d.quickConnectGroup(group, cloud)
				}
				if menu.Item("管理分组").Disabled(draftBlocked).Chosen() {
					if !d.closeConnectionForNavigation() {
						return
					}
					d.profileFilter, d.showGroupControls, d.confirmGroupDelete = group, true, false
				}
			})
			if card.Clicked() {
				if !d.closeConnectionForNavigation() {
					return
				}
				d.profileFilter, d.confirmGroupDelete = group, false
				d.profileFavorites, d.profileRecent = false, false
				d.showProfileInspector = false
				d.profileInspectorAt = time.Time{}
			}
			if cloud {
				if dragged, ok := ui.Drop[cloudHostDrag](card); ok && !d.busy && !draftBlocked && d.prepareConnectionNavigation() {
					d.moveCloudHostDrag(dragged, group)
				}
			} else {
				if dragged, ok := ui.Drop[localHostDrag](card); ok && !d.busy && !draftBlocked && d.prepareConnectionNavigation() {
					d.moveLocalHostDrag(dragged, group)
				}
			}
		}
	})
}

func validGroupName(name string) bool {
	if name == "" || !utf8.ValidString(name) || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 64 || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
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
	d.moveProfilesToGroup([]profileID{id}, group, false)
}

func (d *desktop) moveDraggedProfilesToGroup(id profileID, group string) {
	ids := []profileID{id}
	selected := d.checkedProfiles[id]
	if selected {
		ids = nil
		for checked, yes := range d.checkedProfiles {
			if yes {
				ids = append(ids, checked)
			}
		}
	}
	d.moveProfilesToGroup(ids, group, selected)
}

func (d *desktop) moveProfilesToGroup(ids []profileID, group string, clearSelection bool) {
	if d.busy || !d.groupExists(group) {
		return
	}
	wanted := make(map[profileID]bool, len(ids))
	for _, id := range ids {
		if id == "" || wanted[id] {
			d.status = "选择无效，请重新选择主机。"
			return
		}
		wanted[id] = true
	}
	if len(wanted) == 0 {
		return
	}
	original := d.profiles
	items := append([]profile(nil), original...)
	found, changed := 0, 0
	for i := range items {
		if !wanted[items[i].ID] {
			continue
		}
		found++
		if items[i].Group != group {
			items[i].Group = group
			changed++
		}
	}
	if found != len(wanted) {
		d.status = "部分主机已不存在，请重新选择。"
		return
	}
	if changed == 0 {
		d.status = "所选主机已在该分组。"
		return
	}
	d.profiles = items
	if err := d.saveProfiles(); err != nil {
		d.profiles = original
		d.status = "连接未移动：" + err.Error()
		return
	}
	if clearSelection {
		d.checkedProfiles = nil
		d.profileSelectionAnchor = ""
		d.confirmProfileBatchDelete = false
	}
	d.status = fmt.Sprintf("已将 %d 个连接移动到分组：%s", changed, group)
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
	d.foldedGroups = nil
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
