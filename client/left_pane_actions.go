package main

import (
	"errors"

	"os"
	"path"
	"strings"

	"github.com/egoist/mygo/ui"
)

// A pending edit belongs to one pane, connection and directory generation.
// Navigating or switching the left host invalidates it before it can run.
type leftPaneScope struct {
	sessionID  int
	shell      *remoteShell
	dir        string
	generation uint64
}
type leftPaneAction struct {
	scope             leftPaneScope
	kind, name, value string
	original          remoteFile
}

func (d *desktop) leftScope() (leftPaneScope, []remoteFile, string, bool) {
	if d.secondaryPane.sessionID == 0 {
		p := &d.localPane
		return leftPaneScope{dir: p.directory, generation: p.generation}, p.files, p.selected,
			p.initialized && !p.loading && p.directory != "" && sameLocalDirectory(p.input, p.directory)
	}
	p := &d.secondaryPane
	return leftPaneScope{sessionID: p.sessionID, shell: p.shell, dir: p.directory, generation: p.generation}, p.files, p.selected,
		d.secondarySession() != nil && !p.loading && p.directory != "" && p.input == p.directory
}

func (d *desktop) leftScopeMatches(scope leftPaneScope) bool {
	current, _, _, ready := d.leftScope()
	return ready && current == scope
}

func validExistingPaneName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func (d *desktop) beginLeftAction(kind, name string) {
	scope, files, _, ready := d.leftScope()
	if !ready || d.busy {
		return
	}
	action := leftPaneAction{scope: scope, kind: kind, name: name}
	if kind == "rename" || kind == "delete" {
		if !validExistingPaneName(name) {
			return
		}
		found := false
		for _, file := range files {
			if file.Name == name {
				action.original, found = file, true
				break
			}
		}
		if !found {
			return
		}
		action.value = name
	} else if kind != "mkdir" {
		return
	}
	d.leftPaneAction = action
}

func (d *desktop) leftPaneToolbar(c *ui.Context) {
	_, _, selected, ready := d.leftScope()
	count := len(d.localPane.paneSelection.names)
	show, filter := &d.localPane.showFilter, d.localPane.filter
	if d.secondaryPane.sessionID != 0 {
		count = len(d.secondaryPane.paneSelection.names)
		show, filter = &d.secondaryPane.showFilter, d.secondaryPane.filter
	}
	ui.Box(c).Grow(1)
	if terminalActionButton(c, "left-pane-filter-toggle", "筛选左侧文件", searchIcon, *show || filter != "", false).Clicked() {
		*show = !*show
	}
	if s := d.secondarySession(); s != nil && s.fileOnly {
		if terminalActionButton(c, "left-pane-open-terminal", "打开左侧主机终端", terminalIcon, false, d.busy || d.terminalLimitReached()).Clicked() {
			d.openTerminalFromSFTP(s)
		}
	}
	d.leftPaneOptions(c)
	mkdir := terminalActionButton(c, "left-pane-mkdir", "在左侧新建目录", plusIcon, false, d.busy || !ready)
	if mkdir.Clicked() {
		d.beginLeftAction("mkdir", "")
	}
	anchor := mkdir
	if selected != "" && count <= 1 {
		rename := terminalActionButton(c, "left-pane-rename", "重命名左侧所选项", editIcon, false, d.busy || !ready)
		if rename.Clicked() {
			d.beginLeftAction("rename", selected)
		}
		remove := terminalActionButton(c, "left-pane-delete", "删除左侧所选项", trashIcon, false, d.busy || !ready)
		if remove.Clicked() {
			d.beginLeftAction("delete", selected)
		}
		if d.leftPaneAction.kind == "rename" {
			anchor = rename
		} else if d.leftPaneAction.kind == "delete" {
			anchor = remove
		}
	}
	open := d.leftPaneAction.kind != ""
	ui.PopoverBase(c, anchor, &open, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(270).Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() { d.leftPaneActionView(c) })
		})
	})
	if !open {
		d.leftPaneAction = leftPaneAction{}
	}
}

// The two left-pane modes keep their own ordering and visibility settings.
func (d *desktop) leftPaneOptions(c *ui.Context) {
	order, descending, hidden := &d.localPane.sortOrder, &d.localPane.sortDescending, &d.localPane.hideDotfiles
	selected, selection := &d.localPane.selected, &d.localPane.paneSelection
	if d.secondaryPane.sessionID != 0 {
		order, descending, hidden = &d.secondaryPane.sortOrder, &d.secondaryPane.sortDescending, &d.secondaryPane.hideDotfiles
		selected, selection = &d.secondaryPane.selected, &d.secondaryPane.paneSelection
	}
	button := terminalActionButton(c, "left-pane-options", "左侧文件选项", moreIcon, d.showLeftPaneOptions, false)
	if button.Clicked() {
		d.showLeftPaneOptions = !d.showLeftPaneOptions
	}
	ui.PopoverBase(c, button, &d.showLeftPaneOptions, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(180).Padding(8).Gap(4).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
				for _, column := range []struct{ key, label string }{{"name", "按名称"}, {"size", "按大小"}, {"time", "按修改时间"}} {
					label := column.label
					if *order == column.key || (*order == "" && column.key == "name") {
						if *descending {
							label += " ↓"
						} else {
							label += " ↑"
						}
					}
					if ui.Button(c.Key("left-pane-sort-"+column.key), label).Label("左侧" + label).FillWidth().Height(32).BorderWidth(0).Justify(ui.Start).Clicked() {
						if *order == column.key || (*order == "" && column.key == "name") {
							*descending = !*descending
						} else {
							*order, *descending = column.key, false
						}
						d.showLeftPaneOptions = false
					}
				}
				label := "隐藏点文件"
				if *hidden {
					label = "显示点文件"
				}
				if ui.Button(c.Key("left-pane-toggle-dotfiles"), label).Label("左侧" + label).FillWidth().Height(32).BorderWidth(0).Justify(ui.Start).Clicked() {
					*hidden = !*hidden
					*selection = paneSelection{}
					if *hidden && strings.HasPrefix(*selected, ".") {
						*selected = ""
					}
					if *hidden && strings.HasPrefix(d.leftPaneAction.name, ".") {
						d.leftPaneAction = leftPaneAction{}
					}
					d.showLeftPaneOptions = false
				}
			})
		})
	})
}

func (d *desktop) leftPaneContextMenu(menu *ui.Menu, name string) {
	_, _, _, ready := d.leftScope()
	if menu.Item("重命名").Disabled(d.busy || !ready).Chosen() {
		d.beginLeftAction("rename", name)
	}
	if menu.Item("删除…").Disabled(d.busy || !ready).Chosen() {
		d.beginLeftAction("delete", name)
	}
}

func (d *desktop) leftPaneActionView(c *ui.Context) {
	a := &d.leftPaneAction
	if a.kind == "" {
		return
	}
	if !d.leftScopeMatches(a.scope) {
		d.leftPaneAction = leftPaneAction{}
		return
	}
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		if a.kind == "delete" {
			ui.Text(c, "删除「"+a.name+"」？目录必须为空。").FontSize(12).Wrap()
		} else {
			label := "新目录名称"
			if a.kind == "rename" {
				label = "重命名：" + a.name
			}
			ui.TextInput(c.Key("left-action-name"), &a.value).Label(label).Placeholder(label).FillWidth().OnSubmit(func() { d.runLeftPaneAction() })
		}
		ui.Row(c).Gap(6).Children(func() {
			label := "创建"
			if a.kind == "rename" {
				label = "重命名"
			}
			if a.kind == "delete" {
				label = "确认删除"
			}
			if ui.Button(c.Key("left-action-confirm"), label).Disabled(d.busy).Clicked() {
				d.runLeftPaneAction()
			}
			if ui.Button(c.Key("left-action-cancel"), "取消").Disabled(d.busy).Clicked() {
				d.leftPaneAction = leftPaneAction{}
			}
		})
	})
}

// Check the selected item's type again, and never recurse or overwrite.
// As with ordinary SFTP operations, a remote server can race this check.
func executeLeftPaneAction(a leftPaneAction) error {
	if a.scope.dir == "" {
		return errors.New("目录无效")
	}
	if a.kind == "mkdir" {
		return makeDirectory(a.scope.shell, a.scope.dir, a.value)
	}
	if !validExistingPaneName(a.name) {
		return errors.New("文件名称无效")
	}
	fs, err := openPaneFilesystem(a.scope.shell)
	if err != nil {
		return err
	}
	info, err := fs.stat(fs.join(a.scope.dir, a.name))
	fs.close()
	if err != nil {
		return err
	}
	if info.IsDir() != a.original.IsDir || (info.Mode()&os.ModeSymlink != 0) != a.original.IsLink {
		return errors.New("文件类型已改变，请刷新后重试")
	}
	switch a.kind {
	case "rename":
		return renameFile(a.scope.shell, a.scope.dir, a.name, a.value)
	case "delete":
		if a.scope.shell == nil {
			return deleteLocalItem(a.scope.dir, a.name)
		}
		return deleteRemote(a.scope.shell, path.Join(a.scope.dir, a.name))
	default:
		return errors.New("操作无效")
	}
}

func (d *desktop) runLeftPaneAction() {
	a := d.leftPaneAction
	if a.kind == "" || d.busy || d.win == nil || !d.leftScopeMatches(a.scope) {
		return
	}
	if a.kind != "delete" && !validDirectoryName(a.value) {
		d.leftPaneStatus("请输入单个有效名称")
		return
	}
	d.busy = true
	d.leftPaneStatus("正在处理…")
	go func() {
		err := executeLeftPaneAction(a)
		d.win.Update(func() {
			d.busy = false
			if !d.leftScopeMatches(a.scope) {
				d.status = "先前目录的文件操作已结束，请刷新核对"
				return
			}
			if err != nil {
				d.leftPaneStatus("操作失败：" + transferErrorText(err))
				return
			}
			d.leftPaneAction = leftPaneAction{}
			if a.scope.sessionID == 0 {
				d.requestLocalPane(a.scope.dir, -1)
			} else {
				d.requestSecondaryPane(a.scope.dir, -1)
			}
		})
	}()
}

func (d *desktop) leftPaneStatus(status string) {
	if d.secondaryPane.sessionID == 0 {
		d.localPane.status = status
	} else {
		d.secondaryPane.status = status
	}
}
