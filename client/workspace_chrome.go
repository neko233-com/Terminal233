package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The same strip owns navigation and sessions at every window width. Keeping
// it outside the workspace prevents tabs disappearing when changing tools.
func (d *desktop) workspaceHeader(c *ui.Context, width, height float32) {
	bar := c.TitleBar()
	usableWidth := width - bar.Left - bar.Right
	compact := usableWidth < 700
	ui.Row(c.Key("workspace-header")).Label("全局标签栏").FillWidth().Height(max(float32(52), bar.Height)).Shrink(0).Padding(8, 12+bar.Right, 8, 12+bar.Left).Gap(6).AlignItems(ui.Center).Background(workspaceChrome(c)).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).DragWindow().Children(func() {
		sidebarLabel := "收起侧栏"
		if !d.sidebarVisible() {
			sidebarLabel = "展开侧栏"
		}
		if terminalActionButton(c, "toggle-sidebar", sidebarLabel, menuIcon, false, false).Clicked() {
			if (d.workspace == "terminal" && d.term != nil) || d.workspace == "new-tab" {
				d.terminalNavigationOpen = !d.terminalNavigationOpen
			} else {
				d.sidebarCollapsed = !d.sidebarCollapsed
				d.savePreference("sidebar", d.sidebarCollapsed)
			}
		}
		if usableWidth >= 1000 {
			ui.Text(c, "NekoSSH").FontSize(15).Bold().Margin(0, 10, 0, 2)
		}
		libraryActive := d.workspace != "terminal" && d.workspace != "files" && d.workspace != "new-tab"
		if workspacePinnedTabButton(c, "workspace-hosts-tab", "主机", serverIcon, libraryActive, compact).Clicked() {
			// Returning to the library resumes any host draft rather than
			// silently closing its inspector.
			d.workspace = "connections"
		}
		if workspacePinnedTabButton(c, "workspace-files-tab", "SFTP", fileIcon, d.workspace == "files", compact).Label("SFTP 标签页").Clicked() {
			d.workspace = "files"
			if d.term != nil && d.shell == nil && d.files == nil && !d.busy && d.win != nil {
				d.refreshFiles()
			}
		}
		ui.ScrollHorizontal(c.Key("workspace-session-tabs")).Label("会话标签栏").Grow(1).MinWidth(40).Height(36).Scrollbars(false).Gap(6).Children(func() { d.sessionTabs(c) })
		if d.workspaceRestore != nil && d.workspace != "new-tab" {
			label := "取消恢复"
			if d.workspaceRestore.groupQuick {
				label = "取消连接"
			}
			if terminalActionButton(c, "workspace-restore-cancel-header", label, closeIcon, false, d.workspaceRestore.cancelling).Clicked() {
				d.cancelWorkspaceRestore()
			}
		}
		if !compact {
			themeLabel := d.appearanceActionLabel()
			if terminalActionButton(c, "theme-toggle", themeLabel, appearanceIcon, false, false).Tooltip("切换" + themeLabel + "主题").Clicked() {
				d.toggleAppearance()
			}
		}
		settings := terminalActionButton(c, "settings-toggle", d.settingsButtonLabel(), settingsIcon, d.showSettings || d.updateAvailable != nil, false)
		if settings.Clicked() {
			d.showSettings = !d.showSettings
		}
		ui.PopoverBase(c, settings, &d.showSettings, func(panel ui.Element) {
			panel.AttachTo(settings, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(4, 0, 0, 0).Children(func() { d.settingsView(c, height) })
		})
		terminalActionButton(c, "local-terminal", "新建标签页", plusIcon, d.workspace == "new-tab", d.busy && !d.canSwitchSession()).OnClick(d.openNewTab)
	})
}

func workspaceTabButton(c *ui.Context, key, label string, icon *ui.SVG, active bool) ui.Element {
	width, _ := c.Size()
	bar := c.TitleBar()
	width -= bar.Left + bar.Right
	labelWidth := float32(160)
	if width < 800 {
		labelWidth = 100
	}
	button := ui.Button(c.Key(key), "").Label(label).Tooltip(label).Height(32).Shrink(0).Padding(6, 10).Gap(8).BorderWidth(0).Radius(7).Selected(active).Background(c.Theme().Surface)
	color := c.Theme().TextMuted
	if active {
		button = button.Background(c.Theme().SurfacePressed)
		color = c.Theme().Text
	} else if button.Hovered() {
		button = button.Background(c.Theme().SurfaceHover)
		color = c.Theme().Text
	}
	button.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Width(16).Height(16).TextColor(color)
		}
		ui.Text(c, label).FontSize(13).MaxWidth(labelWidth).SingleLine().TextColor(color)
	})
	return button
}

func workspacePinnedTabButton(c *ui.Context, key, label string, icon *ui.SVG, active, compact bool) ui.Element {
	if compact {
		return railButton(c, key, label, icon, active).Height(32).Shrink(0)
	}
	return workspaceTabButton(c, key, label, icon, active)
}

func (d *desktop) appearanceActionLabel() string {
	if d.lightTheme {
		return "深色"
	}
	return "浅色"
}

func (d *desktop) appearanceSource() mygo.ThemeSource {
	if d.lightTheme {
		return mygo.ThemeLight
	}
	return mygo.ThemeDark
}

func (d *desktop) toggleAppearance() {
	d.lightTheme = !d.lightTheme
	if d.win != nil {
		mygo.Theme.SetSource(d.appearanceSource())
		d.win.SetWindowButtonsTheme(d.appearanceSource())
	}
	d.savePreference("light", d.lightTheme)
}

// Closing a file tool which borrows a terminal must not terminate that shell.
// An independent SFTP tab owns its transport and can close it normally.
func (d *desktop) closeActiveWorkspaceTab() {
	if d.busy {
		d.status = "请等待当前操作完成后关闭标签页"
		return
	}
	switch d.workspace {
	case "new-tab":
		d.closeNewTab()
	case "terminal":
		if group := d.terminalGroupFor(d.currentSession); group != nil {
			d.closeTerminalGroup(group.id)
		} else {
			d.closeCurrentSession()
		}
	case "files":
		if d.currentSFTPSession() != nil {
			d.closeCurrentSession()
		} else if d.term != nil {
			d.selectSession(d.currentSession)
		} else {
			d.workspace = "connections"
		}
	}
}

func workspaceTabClose(c *ui.Context, key, label string, disabled bool) ui.Element {
	button := ui.Button(c.Key(key), "").Label(label).Tooltip(label).Width(26).Height(28).Padding(6).BorderWidth(0).Radius(5).Background(ui.Transparent).Disabled(disabled)
	color := c.Theme().TextMuted
	if button.Hovered() {
		button = button.Background(c.Theme().SurfaceHover)
		color = c.Theme().Text
	}
	button.Children(func() { ui.Icon(c, closeIcon).Width(12).Height(12).TextColor(color) })
	return button
}

func workspaceTabSurface(c *ui.Context, active bool) ui.Color {
	if active {
		return c.Theme().SurfacePressed
	}
	return c.Theme().Surface
}
