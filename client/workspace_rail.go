package main

import "github.com/egoist/mygo/ui"

func (d *desktop) setRailExpanded(expanded bool) {
	d.railExpanded = expanded
	d.savePreference("rail", expanded)
}

func railButton(c *ui.Context, key, label string, icon *ui.SVG, active bool) ui.Element {
	button := ui.Button(c.Key(key), "").Label(label).Tooltip(label).Width(36).Height(36).Padding(8).BorderWidth(0).Radius(7).Selected(active)
	color := c.Theme().TextMuted
	if active {
		button = button.Background(c.Theme().SurfacePressed)
		color = c.Theme().Accent
	}
	if !active {
		button = button.Background(ui.Transparent)
		if button.Hovered() {
			button = button.Background(c.Theme().Surface)
		}
	}
	button.Children(func() { ui.Icon(c, icon).Width(18).Height(18).TextColor(color) })
	return button
}

// Compact navigation keeps the same accessible names and destinations as the
// expanded navigation. The filter action reveals the full group tree.
func (d *desktop) compactWorkspaceNavigation(c *ui.Context) {
	ui.Column(c).FillWidth().Padding(12, 14).Gap(7).AlignItems(ui.Center).Children(func() {
		if railButton(c, "nav-connections", "SSH 连接", serverIcon, d.workspace == "" || d.workspace == "connections").Clicked() {
			d.workspace = "connections"
			if d.editConnection {
				d.closeConnectionEditor()
			}
		}
		if railButton(c, "nav-terminal", "终端", terminalIcon, d.workspace == "terminal").Clicked() {
			d.workspace = "terminal"
		}
		if railButton(c, "nav-files", "远程文件", fileIcon, d.workspace == "files").Clicked() {
			d.workspace = "files"
			if d.term != nil && d.shell == nil && d.files == nil && !d.busy {
				d.refreshFiles()
			}
		}
		if railButton(c, "nav-forward", "端口转发", forwardIcon, d.workspace == "forward").Clicked() {
			d.workspace = "forward"
		}
		if railButton(c, "nav-snippets", "命令片段", snippetIcon, d.workspace == "snippets").Clicked() {
			d.workspace = "snippets"
		}
		if railButton(c, "nav-identities", "身份", identityIcon, d.workspace == "identities").Clicked() {
			if d.closeConnectionForNavigation() {
				d.workspace = "identities"
			}
		}
		if railButton(c, "nav-cloud", "云同步与团队", cloudIcon, d.workspace == "cloud").Clicked() {
			d.workspace = "cloud"
		}
		ui.Box(c).Width(28).Height(1).Background(c.Theme().Border)
		if railButton(c, "connection-navigation-toggle", "连接筛选", searchIcon, d.showConnectionNavigation).Clicked() {
			d.showConnectionNavigation = true
			d.setRailExpanded(true)
		}
		if railButton(c, "workspace-rail-expand", "展开文字导航", railExpandIcon, false).Clicked() {
			d.setRailExpanded(true)
		}
	})
}
