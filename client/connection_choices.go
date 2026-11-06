package main

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

type tagCompletion struct{ label, value string }

// Suggestions stay inside the currently edited storage scope; cloud team
// labels are never mixed into a local or another team's host editor.
func (d *desktop) hostTagVocabulary() []string {
	seen := map[string]bool{}
	var result []string
	add := func(tags []string) {
		for _, tag := range tags {
			key := profileTagKey(tag)
			if !seen[key] {
				seen[key] = true
				result = append(result, tag)
			}
		}
	}
	if d.settingsFromCloud {
		if d.cloud != nil && !d.cloudListStale {
			for _, a := range d.accounts {
				add(a.Tags)
			}
		}
	} else {
		for _, p := range d.profiles {
			add(p.Tags)
		}
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

// Typed completions replace only the last comma-delimited fragment. A list
// opened by click appends to the complete text instead of discarding it.
func hostTagCompletions(raw string, vocabulary []string, filtering bool) []tagCompletion {
	if !utf8.ValidString(raw) || len(raw) > 4096 {
		return nil
	}
	prefix, partial := "", ""
	if filtering {
		idx, delim := strings.LastIndex(raw, ","), 1
		if other := strings.LastIndex(raw, "，"); other > idx {
			idx, delim = other, len("，")
		}
		if idx >= 0 {
			prefix = raw[:idx+delim]
			partial = raw[idx+delim:]
		} else {
			partial = raw
		}
	} else {
		prefix = raw
	}
	prior, err := parseProfileTags(prefix)
	if err != nil || len(prior) >= maxProfileTags {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(partial))
	result := make([]tagCompletion, 0, min(len(vocabulary), 50))
	for _, tag := range vocabulary {
		if hasProfileTag(prior, tag) || !validStoredProfileTags([]string{tag}) || (filtering && !strings.Contains(strings.ToLower(tag), query)) {
			continue
		}
		var value string
		if filtering {
			leading := partial[:len(partial)-len(strings.TrimLeftFunc(partial, unicode.IsSpace))]
			value = prefix + leading + tag
		} else {
			tags := append(append([]string(nil), prior...), tag)
			value = strings.Join(tags, ", ")
		}
		if _, err := parseProfileTags(value); err != nil {
			continue
		}
		result = append(result, tagCompletion{label: tag, value: value})
		if len(result) == 50 {
			break
		}
	}
	return result
}

func hostChoiceInput(c *ui.Context, key string, value *string, label, placeholder string, changed func()) ui.ComboboxParts {
	p := ui.ComboboxBase(c.Key(key), value)
	p.Input.Label(label).Placeholder(placeholder).Padding(6, 10).Radius(6).Background(c.Theme().Surface).Border(1, c.Theme().Border).FillWidth().OnChange(changed)
	return p
}

func (d *desktop) hostTagInput(c *ui.Context, changed func()) {
	p := hostChoiceInput(c, "profile-tags", &d.profileTags, "主机标签", "标签，用逗号分隔", changed)
	if d.profileTagsCaretPending {
		n := utf8.RuneCountInString(d.profileTags)
		p.Input.SetTextSelection(n, n)
		d.profileTagsCaretPending = false
	}
	focused := p.Input.Focused()
	start, end := p.Input.TextSelection()
	var choices []tagCompletion
	if p.Open() && (!focused || (start == end && end == utf8.RuneCountInString(d.profileTags))) {
		choices = hostTagCompletions(d.profileTags, d.hostTagVocabulary(), p.Filtering())
	}
	p.Popup(func(panel ui.Element) {
		panel.Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(8).Padding(4).Children(func() {
			ui.Scroll(c.Key("profile-tags-suggestions")).MaxHeight(200).FillWidth().Children(func() {
				for _, choice := range choices {
					choice := choice
					item := p.Item(choice.value).Label("添加标签："+choice.label).Padding(6, 8).Radius(5)
					if item.Highlighted() {
						item = item.Background(c.Theme().SurfacePressed)
					}
					item.Children(func() { ui.Text(c, choice.label).SingleLine() })
				}
			})
		})
	})
	if choice, ok := p.Chosen(); ok {
		d.profileTags = choice
		d.profileTagsCaretPending = true
		changed()
		c.Invalidate()
	}
}

func (d *desktop) localGroupInput(c *ui.Context, changed func()) {
	p := hostChoiceInput(c, "profile-group", &d.profileGroup, "连接分组", "分组（可选）", changed)
	if d.profileGroupCaretPending {
		n := utf8.RuneCountInString(d.profileGroup)
		p.Input.SetTextSelection(n, n)
		d.profileGroupCaretPending = false
	}
	_ = p.Input.Focused()
	var groups []string
	if p.Open() {
		groups, _ = d.groupTree()
	}
	query := strings.ToLower(strings.TrimSpace(d.profileGroup))
	p.Popup(func(panel ui.Element) {
		panel.Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(8).Padding(4).Children(func() {
			ui.Scroll(c.Key("profile-group-suggestions")).MaxHeight(200).FillWidth().Children(func() {
				if !p.Filtering() {
					item := p.Item("").Label("不使用分组").Padding(6, 8)
					if item.Highlighted() {
						item = item.Background(c.Theme().SurfacePressed)
					}
					item.Children(func() { ui.Text(c, "未分组") })
				}
				count := 0
				for _, group := range groups {
					group := group
					if p.Filtering() && !strings.Contains(strings.ToLower(group), query) {
						continue
					}
					if count >= 100 {
						break
					}
					item := p.Item(group).Label("选择分组："+group).Padding(6, 8)
					if item.Highlighted() {
						item = item.Background(c.Theme().SurfacePressed)
					}
					item.Children(func() { ui.Text(c, group).SingleLine() })
					count++
				}
			})
		})
	})
	if choice, ok := p.Chosen(); ok {
		d.profileGroup = choice
		d.profileGroupCaretPending = true
		changed()
		c.Invalidate()
	}
}

func (d *desktop) cloudGroupInput(c *ui.Context) {
	if d.cloud == nil || !d.cloud.cloudGroups {
		return
	}
	const none = "未分组"
	id := d.formCloudSource()
	found := false
	for _, account := range d.accounts {
		if account.ID == id && id != "" {
			found = true
			break
		}
	}
	current := none
	validState := !d.cloudListStale && !d.cloudGroupsStale && cloudGroupAccountsMatch(d.cloudGroupSnapshot, d.accounts)
	if !validState {
		current = "分组未加载"
	} else if !found {
		current = "未同步到当前空间"
	} else if group := d.cloudGroupSnapshot.Assignments[id]; group != "" {
		current = "分组 / " + group
	}
	options := []string{none}
	for _, group := range d.cloudGroupSnapshot.Groups {
		options = append(options, "分组 / "+group)
	}
	disabled := d.busy || !validState || !d.canWriteCloudScope() || !found
	if ui.Select(c.Key("cloud-host-group"), &current, options).Label("云端主机分组").Tooltip("选择云端分组").Disabled(disabled).FillWidth().Changed() {
		target := ""
		if current != none {
			target = strings.TrimPrefix(current, "分组 / ")
		}
		d.cloudGroupOperation("move", id, target)
	}
}
