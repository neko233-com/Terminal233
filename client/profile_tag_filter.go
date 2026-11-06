package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
)

type profileTagOption struct {
	name  string
	count int
}

func profileTagKey(tag string) string {
	var key strings.Builder
	for _, r := range tag {
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		key.WriteRune(minimum)
	}
	return key.String()
}

func hasProfileTag(tags []string, target string) bool {
	for _, tag := range tags {
		if strings.EqualFold(tag, target) {
			return true
		}
	}
	return false
}
func matchesProfileTags(tags, filters []string) bool {
	for _, filter := range filters {
		if !hasProfileTag(tags, filter) {
			return false
		}
	}
	return true
}
func (d *desktop) toggleProfileTagFilter(tag string) {
	for i, existing := range d.profileTagFilters {
		if strings.EqualFold(existing, tag) {
			d.profileTagFilters = append(d.profileTagFilters[:i], d.profileTagFilters[i+1:]...)
			return
		}
	}
	d.profileTagFilters = append(d.profileTagFilters, tag)
}
func (d *desktop) profileInScope(p profile) bool {
	return d.profileSource != "cloud" && (!d.profileRecent || p.LastConnected > 0) && (!d.profileFavorites || p.Favorite) && (d.profileFilter == "" || groupWithin(p.Group, d.profileFilter))
}
func (d *desktop) cloudAccountInScope(a cloudAccount) bool {
	return d.cloud != nil && d.profileSource != "local" && !d.profileFavorites && !d.profileRecent && (d.profileFilter == "" || (d.profileSource == "cloud" && groupWithin(d.cloudGroupSnapshot.Assignments[a.ID], d.profileFilter)))
}

// Count tags in the current source and group, without applying search or the
// selected tags, so a second filter is always discoverable.
func (d *desktop) profileTagOptions() []profileTagOption {
	options := make([]profileTagOption, 0)
	index := map[string]int{}
	add := func(tag string) {
		key := profileTagKey(tag)
		i, ok := index[key]
		if ok {
			options[i].count++
		} else {
			index[key] = len(options)
			options = append(options, profileTagOption{name: tag, count: 1})
		}
	}
	for _, p := range d.profiles {
		if d.profileInScope(p) {
			for _, tag := range p.Tags {
				add(tag)
			}
		}
	}
	for _, a := range d.accounts {
		if d.cloudAccountInScope(a) {
			for _, tag := range a.Tags {
				add(tag)
			}
		}
	}
	for _, tag := range d.profileTagFilters {
		if _, ok := index[profileTagKey(tag)]; !ok {
			index[profileTagKey(tag)] = len(options)
			options = append(options, profileTagOption{name: tag})
		}
	}
	sort.Slice(options, func(i, j int) bool { return strings.ToLower(options[i].name) < strings.ToLower(options[j].name) })
	return options
}
func (d *desktop) profileTagFilterButton(c *ui.Context, blocked bool) {
	label := "标签"
	accessible := "标签筛选"
	if len(d.profileTagFilters) > 0 {
		label = fmt.Sprintf("标签 · %d", len(d.profileTagFilters))
		accessible = fmt.Sprintf("标签筛选（已选 %d）", len(d.profileTagFilters))
	}
	button := ui.Button(c.Key("profile-tags-filter"), label).Label(accessible).Tooltip("按标签筛选主机").Disabled(blocked)
	if button.Clicked() {
		d.showProfileTagFilter = !d.showProfileTagFilter
	}
	ui.PopoverBase(c, button, &d.showProfileTagFilter, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(250).Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, "标签筛选").FontSize(13).Bold()
					ui.Box(c).Grow(1)
					if ui.Button(c.Key("profile-tags-clear"), "清除").Label("清除标签筛选").Disabled(len(d.profileTagFilters) == 0 || blocked).Clicked() {
						d.profileTagFilters = nil
					}
				})
				ui.TextInput(c.Key("profile-tags-query"), &d.profileTagQuery).Label("搜索标签").Placeholder("搜索标签").FillWidth()
				options := d.profileTagOptions()
				query := strings.ToLower(strings.TrimSpace(d.profileTagQuery))
				filtered := make([]profileTagOption, 0, len(options))
				for _, option := range options {
					if query == "" || strings.Contains(strings.ToLower(option.name), query) {
						filtered = append(filtered, option)
					}
				}
				if len(filtered) == 0 {
					ui.Text(c, "没有标签").FontSize(12).TextColor(c.Theme().TextMuted)
					return
				}
				ui.Scroll(c.Key("profile-tags-options")).Label("可筛选的标签").Height(min(float32(260), float32(len(filtered)*34))).FillWidth().Children(func() {
					for i, option := range filtered {
						if i >= 100 {
							break
						}
						option := option
						selected := hasProfileTag(d.profileTagFilters, option.name)
						if ui.Checkbox(c.Key(fmt.Sprintf("profile-tags-option-%d", i)), &selected, fmt.Sprintf("%s  %d", option.name, option.count)).Label("筛选标签：" + option.name).Disabled(blocked).Changed() {
							if d.closeConnectionForNavigation() {
								d.toggleProfileTagFilter(option.name)
							}
						}
					}
				})
				if len(filtered) > 100 {
					ui.Text(c, "输入名称查找更多标签").FontSize(12).TextColor(c.Theme().TextMuted)
				}
			})
		})
	})
}
