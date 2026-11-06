package main

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) openNewTab() {
	if d.workspace != "new-tab" {
		d.newTabReturn = d.workspace
		d.showTerminalGroupSelection = false
		d.terminalGroupSelected = nil
		d.showWorkspaceHostSelection = false
		d.workspaceHostSelected = nil
	}
	d.newTabOpen, d.workspace, d.terminalNavigationOpen = true, "new-tab", false
	d.newTabStatus = ""
}

func (d *desktop) closeNewTab() {
	d.newTabOpen = false
	d.newTabQuery, d.newTabStatus = "", ""
	d.newTabSelected, d.newTabSelectionQuery = "", ""
	if d.workspace != "new-tab" {
		return
	}
	d.workspace = d.newTabReturn
	if d.workspace == "" || d.workspace == "new-tab" || (d.workspace == "terminal" && d.term == nil) {
		d.workspace = "connections"
	}
	if d.workspace == "terminal" && d.currentSession != 0 {
		d.pendingTerminalFocus = d.currentSession
	}
}

func (d *desktop) newTabQuickConnect() {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions {
		return
	}
	if !d.prepareConnectionNavigation() {
		d.newTabStatus = d.status
		return
	}
	if _, err := parseQuickConnection(d.newTabQuery); err != nil {
		d.newTabStatus = err.Error()
		return
	}
	d.quickConnect = d.newTabQuery
	d.openQuickConnection()
	d.workspace, d.newTabOpen = "connections", false
	d.newTabQuery, d.newTabStatus = "", ""
	d.newTabSelected, d.newTabSelectionQuery = "", ""
}

type newTabResult struct {
	key, label, detail, accessible, kind, id string
	sessionID                                int
	enabled                                  bool
}

func newTabMatches(query, text string) bool {
	text = strings.ToLower(text)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

func (d *desktop) newTabResults(query string) []newTabResult {
	query = strings.TrimSpace(query)
	openDisabled := d.busy || d.connectionNavigationBlocked() || len(d.sessions) >= maxWorkspaceSessions
	localDisabled := d.busy || len(d.sessions) >= maxWorkspaceSessions
	quick := newTabResult{key: "new-tab-quick-connect", label: "填写 SSH 连接信息", accessible: "填写 SSH 连接信息", kind: "quick", enabled: !openDisabled}
	results := make([]newTabResult, 0, 1+len(d.sessions)+len(d.profiles)+len(d.accounts))
	explicitSSH := strings.HasPrefix(query, "ssh ")
	if explicitSSH {
		results = append(results, quick)
	}
	if newTabMatches(query, "本机终端 local terminal powershell") {
		results = append(results, newTabResult{key: "new-tab-local", label: "本机终端", detail: "PowerShell 7", accessible: "打开本机终端", kind: "local", enabled: !localDisabled})
	}
	for _, s := range d.sessions {
		if newTabMatches(query, s.label) {
			label := sessionDisplayLabel(s)
			results = append(results, newTabResult{key: fmt.Sprintf("new-tab-session-%d", s.id), label: label, detail: fmt.Sprintf("已打开 · 会话 %d", s.id), accessible: fmt.Sprintf("切换到会话：%s · 会话 %d", label, s.id), kind: "session", sessionID: s.id, enabled: d.canSwitchSession()})
		}
	}
	profiles := d.profiles
	if query == "" {
		profiles = append([]profile(nil), d.profiles...)
		sort.SliceStable(profiles, func(i, j int) bool { return profiles[i].LastConnected > profiles[j].LastConnected })
	}
	for _, p := range profiles {
		name := p.Label
		if name == "" {
			name = p.Settings.User + "@" + p.Settings.Host
		}
		address := p.Settings.User + "@" + net.JoinHostPort(p.Settings.Host, p.Settings.Port)
		if newTabMatches(query, name+" "+address+" "+p.Settings.User+"@"+p.Settings.Host+" "+p.Settings.Host+" "+p.Settings.User+" "+p.Group+" "+strings.Join(p.Tags, " ")) {
			results = append(results, newTabResult{key: "new-tab-profile-" + string(p.ID), label: name, detail: address + " · 本机", accessible: "打开主机：" + name + " · 本机", kind: "profile", id: string(p.ID), enabled: !openDisabled})
		}
	}
	if d.cloud != nil {
		for _, a := range d.accounts {
			name := a.Label
			if name == "" {
				name = a.Username + "@" + a.Host
			}
			address := a.Username + "@" + net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
			if newTabMatches(query, name+" "+address+" "+a.Username+"@"+a.Host+" "+a.Host+" "+a.Username+" "+strings.Join(a.Tags, " ")) {
				results = append(results, newTabResult{key: "new-tab-cloud-" + a.ID, label: name, detail: address + " · 云端", accessible: "打开主机：" + name + " · 云端", kind: "cloud", id: a.ID, enabled: !openDisabled})
			}
		}
	}
	if !explicitSSH && looksLikeQuickConnection(query) {
		results = append(results, quick)
	}
	return results
}

func (d *desktop) activateNewTabResult(key string) {
	for _, result := range d.visibleNewTabResults(d.newTabQuery) {
		if result.key != key || !result.enabled {
			continue
		}
		if d.showWorkspaceHostSelection {
			d.toggleWorkspaceResult(result)
			return
		}
		switch result.kind {
		case "quick":
			d.newTabQuickConnect()
		case "local":
			before := d.nextSession
			d.openLocalTerminal()
			if d.nextSession != before {
				d.newTabOpen, d.newTabQuery = false, ""
				d.newTabSelected, d.newTabSelectionQuery = "", ""
			} else {
				d.newTabStatus = d.status
			}
		case "session":
			d.selectSession(result.sessionID)
		case "profile":
			if !d.prepareConnectionNavigation() {
				d.newTabStatus = d.status
				return
			}
			for _, p := range d.profiles {
				if string(p.ID) == result.id {
					d.newTabOpen, d.newTabQuery = false, ""
					d.newTabSelected, d.newTabSelectionQuery = "", ""
					d.openProfileConnection(p)
					return
				}
			}
		case "cloud":
			if !d.prepareConnectionNavigation() {
				d.newTabStatus = d.status
				return
			}
			for _, a := range d.accounts {
				if a.ID == result.id {
					d.newTabOpen, d.newTabQuery = false, ""
					d.newTabSelected, d.newTabSelectionQuery = "", ""
					d.loadCloudAccount(a)
					return
				}
			}
		}
		return
	}
}

func (d *desktop) visibleNewTabResults(query string) []newTabResult {
	results := d.newTabResults(query)
	if !d.showWorkspaceHostSelection {
		return results
	}
	filtered := results[:0]
	for _, result := range results {
		if d.workspaceResultKey(result) != "" {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func (d *desktop) newTabSyncSelection(query string) {
	if d.newTabSelectionQuery != query {
		d.newTabSelectionQuery, d.newTabSelected, d.newTabStatus = query, "", ""
	}
}

func (d *desktop) newTabTextKey(k ui.TextKey) bool {
	if k.Mods != 0 {
		return false
	}
	query := k.Text()
	d.newTabQuery = query
	d.newTabSyncSelection(query)
	var eligible []newTabResult
	for _, result := range d.visibleNewTabResults(query) {
		if result.enabled {
			eligible = append(eligible, result)
		}
	}
	index := -1
	for i, result := range eligible {
		if result.key == d.newTabSelected {
			index = i
			break
		}
	}
	if index < 0 && strings.TrimSpace(query) != "" && len(eligible) > 0 {
		index = 0
	}
	switch k.Key {
	case ui.KeyDown, ui.KeyUp:
		if len(eligible) == 0 {
			return true
		}
		if k.Key == ui.KeyDown {
			index = (index + 1) % len(eligible)
		} else if index < 0 {
			index = len(eligible) - 1
		} else {
			index = (index - 1 + len(eligible)) % len(eligible)
		}
		d.newTabSelected = eligible[index].key
		return true
	case ui.KeyEnter:
		if index >= 0 {
			d.activateNewTabResult(eligible[index].key)
		}
		return true
	case ui.KeyEscape:
		if query != "" {
			k.SetText("")
			d.newTabSelected, d.newTabSelectionQuery, d.newTabStatus = "", "", ""
		} else if d.showWorkspaceHostSelection {
			d.showWorkspaceHostSelection = false
			d.workspaceHostSelected = nil
			d.workspaceTemplatesStatus = ""
		} else {
			d.closeNewTab()
		}
		return true
	}
	return false
}

func (d *desktop) newTabView(c *ui.Context) {
	d.newTabSyncSelection(d.newTabQuery)
	results := d.visibleNewTabResults(d.newTabQuery)
	highlight := d.newTabSelected
	if highlight != "" {
		found := false
		for _, result := range results {
			if result.key == highlight && result.enabled {
				found = true
				break
			}
		}
		if !found {
			highlight = ""
		}
	}
	if highlight == "" && strings.TrimSpace(d.newTabQuery) != "" {
		for _, result := range results {
			if result.enabled {
				highlight = result.key
				break
			}
		}
	}
	width, _ := c.Size()
	if d.sidebarVisible() {
		width -= workspaceSidebarWidth(width)
	}
	contentWidth := min(float32(760), max(float32(1), width-48))
	ui.Row(c).FillWidth().Padding(40, 24).Justify(ui.Center).Children(func() {
		ui.Column(c).Width(contentWidth).Gap(16).Children(func() {
			ui.TextInput(c.Key("new-tab-search"), &d.newTabQuery).Label("搜索主机或会话").Placeholder("搜索主机、会话或 SSH 地址").FillWidth().AutoFocus().OnTextKey(d.newTabTextKey)
			if !d.showWorkspaceHostSelection || d.workspaceRestore != nil {
				d.workspaceTemplatesView(c)
			}
			if d.newTabStatus != "" {
				ui.Text(c, d.newTabStatus).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			}
			if d.connectionNavigationBlocked() {
				ui.Text(c, "主机详情有未保存的更改；保存或关闭后可打开其他主机。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			}
			if len(results) == 0 && strings.TrimSpace(d.newTabQuery) != "" {
				ui.Text(c, "没有匹配的主机或会话").FontSize(12).TextColor(c.Theme().TextMuted)
			}
			if d.workspaceTemplatesStatus != "" && d.showWorkspaceHostSelection && d.workspaceRestore == nil {
				ui.Text(c, d.workspaceTemplatesStatus).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			}
			if d.showWorkspaceHostSelection {
				ui.TextInput(c.Key("workspace-template-name"), &d.workspaceTemplateName).Label("新工作区名称").Placeholder("工作区名称（可选）").FillWidth()
			}
			ui.Column(c).FillWidth().Padding(12).Gap(4).Background(c.Theme().Surface).Radius(12).Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Padding(4, 8).Wrap().Children(func() {
					title := "会话与主机"
					if d.showWorkspaceHostSelection {
						title = fmt.Sprintf("选择主机 · %d", len(d.workspaceHostSelected))
					}
					ui.Text(c, title).Bold().FontSize(13)
					ui.Box(c).Grow(1)
					d.workspaceHostSelectionControls(c)
				})
				for _, result := range results {
					r := result
					label := r.accessible
					if d.showWorkspaceHostSelection {
						label = "选择工作区主机：" + r.label
						if r.kind == "profile" {
							label += " · 本机 · " + r.id
						}
						if r.kind == "cloud" {
							label += " · 云端 · " + r.id
						}
					}
					button := ui.Button(c.Key(r.key), "").Label(label).FillWidth().Padding(10, 12).Gap(12).BorderWidth(0).Background(c.Theme().Surface).Radius(8).Justify(ui.Start).Disabled(!r.enabled)
					selected := d.showWorkspaceHostSelection && d.workspaceHostSelected[d.workspaceResultKey(r)]
					if selected || (!d.showWorkspaceHostSelection && highlight == r.key) {
						button = button.Selected(true).Background(c.Theme().SurfacePressed).TextColor(c.Theme().Text)
					} else if d.showWorkspaceHostSelection && highlight == r.key {
						button = button.Background(c.Theme().SurfaceHover).TextColor(c.Theme().Text)
					}
					button.Children(func() {
						icon := serverIcon
						if r.kind == "local" || r.kind == "session" {
							icon = terminalIcon
						} else if r.kind == "cloud" {
							icon = cloudIcon
						} else if r.kind == "quick" {
							icon = searchIcon
						}
						ui.Icon(c, icon).Width(18).Height(18).TextColor(c.Theme().TextMuted)
						ui.Text(c, r.label).Grow(1).SingleLine()
						if r.detail != "" && width >= 700 {
							ui.Text(c, r.detail).FontSize(12).TextColor(c.Theme().TextMuted).MaxWidth(260).SingleLine().Tooltip(r.detail)
						}
						if selected {
							ui.Text(c, "✓").TextColor(c.Theme().Accent)
						}
					})
					if button.Clicked() {
						d.activateNewTabResult(r.key)
					}
				}
			})
		})
	})
}
