package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The local pane is independent of the terminal session and never borrows
// the remote pane's path, selection, or directory history.
type localFilePane struct {
	directory, input, selected, status string
	filter                             string
	showFilter, sortDescending         bool
	sortOrder                          string
	hideDotfiles                       bool
	files                              []remoteFile
	history                            fileHistory
	initialized, loading               bool
	pathMenuOpen                       bool
	loadingDirectory                   string
	statusAfterLoad                    string
	generation                         uint64
	paneSelection                      paneSelection
}

type localPaneDrag struct {
	directory, name string
	generation      uint64
	sessionID       int
	shell           *remoteShell
	remoteDir       string
	items           []paneCopyItem
}

type remotePaneDrag struct {
	directory, name     string
	sessionID           int
	shell               *remoteShell
	secondaryID         int
	secondaryShell      *remoteShell
	secondaryDir        string
	secondaryGeneration uint64
	localDirectory      string
	localGeneration     uint64
	items               []paneCopyItem
}

func sameLocalDirectory(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (d *desktop) fileWorkspace(c *ui.Context, width float32) {
	if len(d.externalEdits) > 0 {
		ui.Column(c).FillWidth().Padding(6, 12).Shrink(0).Children(func() { d.externalEditJobsView(c) })
	}

	left := func() {
		label := "本机文件面板"
		if d.secondaryPane.sessionID != 0 {
			label = "第二会话文件面板"
		}
		local := ui.Column(c.Key("local-file-pane")).Label(label).Fill().Padding(10, 12).Gap(4)
		local.Children(func() {
			if d.secondaryPane.sessionID == 0 {
				d.localFilePaneView(c, -1)
			} else {
				d.secondaryFilePaneView(c, -1)
			}
		})
		if target, ready := d.paneFolderTarget(false, ""); ready {
			if files := local.DroppedFiles(); len(files) > 0 {
				d.copyExternalFilesToFolder(files, target)
			}
		}
		if item, ok := ui.Drop[remotePaneDrag](local); ok {
			if item.secondaryID != d.secondaryPane.sessionID {
				d.fileStatus = "目标面板已改变，请重新选择文件"
			} else if d.secondaryPane.sessionID == 0 {
				if item.localDirectory != d.localPane.directory || item.localGeneration != d.localPane.generation {
					d.fileStatus = "目标目录已改变，请重新选择文件"
				} else {
					d.downloadPaneFile(item)
				}
			} else {
				d.copyRightToSecondary(item)
			}
		}
	}
	right := func() {
		remote := ui.Column(c.Key("session-file-pane")).Label("会话文件面板").Fill().Padding(10, 12).Gap(4)
		remote.Children(func() { d.filePaneView(c, true, true) })
		if files := remote.DroppedFiles(); len(files) > 0 {
			d.handleFileDrop(files)
		}
		// MyGo accepts one typed value drop per element. Register only the
		// currently visible left pane's drag type.
		if d.secondaryPane.sessionID == 0 {
			if item, ok := ui.Drop[localPaneDrag](remote); ok {
				d.uploadPaneFile(item)
			}
		} else {
			if item, ok := ui.Drop[secondaryPaneDrag](remote); ok {
				d.copySecondaryToRight(item)
			}
		}
	}
	if width < 860 {
		if d.narrowFilePane == "" {
			if d.fileSessionUnavailable() && len(d.files) == 0 {
				d.narrowFilePane = "local"
			} else {
				d.narrowFilePane = "session"
			}
		}
		ui.Row(c.Key("file-pane-navigation")).FillWidth().Height(44).Shrink(0).Padding(6, 12).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			leftLabel := "本机文件"
			if d.secondaryPane.sessionID != 0 {
				leftLabel = "左侧文件"
			}
			if workspaceButton(c, "narrow-file-local", leftLabel, d.narrowFilePane == "local").Clicked() {
				d.narrowFilePane = "local"
			}
			if workspaceButton(c, "narrow-file-session", "会话文件", d.narrowFilePane == "session").Clicked() {
				d.narrowFilePane = "session"
			}
		})
		ui.Column(c).Grow(1).FillWidth().Children(func() {
			if d.narrowFilePane == "local" {
				left()
			} else {
				right()
			}
		})
		return
	}
	d.clampFileWorkspaceSplit(width)
	split := ui.SplitPercent(c.Key("file-workspace-split"), &d.fileWorkspacePercent, left, right).Grow(1).FillWidth()
	if split.Changed() {
		d.clampFileWorkspaceSplit(width)
		c.Invalidate()
	}
}

func (d *desktop) initializeLocalPane() {
	if d.localPane.initialized || d.win == nil {
		return
	}
	d.localPane.initialized = true
	d.localPane.hideDotfiles = true
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		d.localPane.status = "无法找到本机用户目录，请输入目录路径。"
		return
	}
	d.requestLocalPane(dir, -1)
}

func (d *desktop) requestLocalPane(dir string, historyIndex int) {
	if d.win == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		d.localPane.status = "请输入本机绝对目录路径。"
		return
	}
	dir = filepath.Clean(dir)
	d.localPane.input, d.localPane.loading, d.localPane.loadingDirectory = dir, true, dir
	d.localPane.statusAfterLoad = ""
	d.localPane.status = "正在读取本机目录…"
	d.localPane.generation++
	generation := d.localPane.generation
	win := d.win
	go func() {
		files, err := listLocal(dir)
		win.Update(func() { d.finishLocalPaneLoad(generation, dir, historyIndex, files, err) })
	}()
}

func (d *desktop) finishLocalPaneLoad(generation uint64, dir string, historyIndex int, files []remoteFile, err error) {
	if generation != d.localPane.generation {
		return
	}
	d.localPane.loading = false
	d.localPane.loadingDirectory = ""
	after := d.localPane.statusAfterLoad
	d.localPane.statusAfterLoad = ""
	if err != nil {
		d.localPane.input = d.localPane.directory
		d.localPane.status = "读取本机目录失败：" + err.Error()
		if after != "" {
			d.localPane.status = after + "；" + d.localPane.status
		}
		d.localPane.files, d.localPane.selected = nil, ""
		d.localPane.paneSelection = paneSelection{}
		if d.win != nil && d.localPane.directory != "" && !sameLocalDirectory(dir, d.localPane.directory) {
			status := d.localPane.status
			d.requestLocalPane(d.localPane.directory, -1)
			if d.localPane.loading {
				d.localPane.statusAfterLoad = status
			}
		}
		return
	}
	if historyIndex < 0 || !d.localPane.history.commitNavigation(historyIndex, dir) {
		d.localPane.history.visit(dir)
	}
	d.localPane.directory, d.localPane.input = dir, dir
	d.localPane.files, d.localPane.selected = files, ""
	d.localPane.paneSelection = paneSelection{}
	d.localPane.status = fmt.Sprintf("%d 个条目", len(files))
	if after != "" {
		d.localPane.status = after
	}
}

func (d *desktop) navigateLocalPane(offset int) {
	index, dir, ok := d.localPane.history.target(offset)
	if ok {
		d.requestLocalPane(dir, index)
	}
}

func (d *desktop) localPaneInputChanged() {
	p := &d.localPane
	p.selected = ""
	p.paneSelection = paneSelection{}
	if p.loading && !sameLocalDirectory(p.input, p.loadingDirectory) {
		p.generation++
		p.loading, p.loadingDirectory = false, ""
	}
	if !p.loading && !sameLocalDirectory(p.input, p.directory) {
		p.status = "打开输入的目录后才能传输。"
	}
}

func (d *desktop) localFilePaneView(c *ui.Context, listHeight float32) {
	d.initializeLocalPane()
	ui.Row(c).FillWidth().Height(36).AlignItems(ui.Center).Children(func() { d.secondaryFilePicker(c); d.leftPaneToolbar(c) })
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		_, _, back := d.localPane.history.target(-1)
		_, _, forward := d.localPane.history.target(1)
		if ui.Button(c.Key("local-pane-back"), "‹").Label("本机目录后退").Tooltip("后退").Width(36).Disabled(!back).Clicked() {
			d.navigateLocalPane(-1)
		}
		if ui.Button(c.Key("local-pane-forward"), "›").Label("本机目录前进").Tooltip("前进").Width(36).Disabled(!forward).Clicked() {
			d.navigateLocalPane(1)
		}
		if ui.Button(c.Key("local-pane-parent"), "↑").Label("本机上一级目录").Tooltip("上一级").Width(36).Disabled(d.busy || d.localPane.loading || d.localPane.directory == "").Clicked() {
			d.requestLocalPane(filepath.Dir(d.localPane.directory), -1)
		}
		ui.TextInput(c.Key("local-pane-path"), &d.localPane.input).Label("本机目录").Placeholder("本机绝对目录路径").OnChange(d.localPaneInputChanged).OnSubmit(func() { d.requestLocalPane(d.localPane.input, -1) }).Grow(1)
		dir, generation := d.localPane.directory, d.localPane.generation
		fileParentMenu(c, "local-path-parent-menu", dir, true, d.busy || d.localPane.loading || !paneHistoryAt(d.localPane.history, dir), &d.localPane.pathMenuOpen, func(target string) {
			if d.secondaryPane.sessionID == 0 && !d.busy && !d.localPane.loading && d.localPane.generation == generation && sameLocalDirectory(d.localPane.directory, dir) {
				d.requestLocalPane(target, -1)
			}
		})
		if ui.Button(c.Key("local-pane-open"), "↻").Label("打开本机目录").Tooltip("打开或刷新目录").Width(36).Clicked() {
			d.requestLocalPane(d.localPane.input, -1)
		}
	})
	if d.localPane.showFilter {
		ui.TextInput(c.Key("local-pane-filter"), &d.localPane.filter).Label("筛选左侧本机文件").Placeholder("筛选文件").OnChange(func() {
			d.localPane.selected = ""
			d.localPane.paneSelection = paneSelection{}
		})
	}
	if d.localPane.status != "" {
		ui.Text(c, d.localPane.status).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	}
	d.fileColumnHeader(c, true)

	visible := displayedFiles(d.localPane.files, d.localPane.filter, d.localPane.sortOrder, d.localPane.sortDescending, d.localPane.hideDotfiles)
	fileListRegion(c, "local-pane-list", "本机文件列表", listHeight).Children(func() {
		for _, entry := range visible {
			f := entry
			label := f.Name
			if f.IsDir {
				label = "📁 " + label
			}
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				row := ui.Button(c.Key("local-pane-file-"+f.Name), "").Label(label).BorderWidth(0).Justify(ui.Start).Grow(1).Gap(8).Background(ui.Transparent)
				if d.localPane.selected == f.Name || d.localPane.paneSelection.names[f.Name] {
					row = row.Selected(true).Background(c.Theme().SurfacePressed)
				}
				row.Children(func() {
					icon, color := documentIcon, c.Theme().TextMuted
					if f.IsDir {
						icon, color = fileIcon, c.Theme().AccentHover
					}
					ui.Icon(c, icon).Width(16).Height(16).Shrink(0).TextColor(color)
					ui.Text(c, f.Name).Grow(1).SingleLine().Tooltip(f.Name)
					d.fileMetadata(c, f, true)
				})
				item := d.localPaneDragForFile(f)
				if eligiblePaneItem(f) {
					row = row.Drag(item)
				}
				row.ContextMenu(func(menu *ui.Menu) {
					d.leftPaneContextMenu(menu, f.Name)
					if eligibleBatchFile(f) && menu.Item("用编辑器打开").Disabled(d.busy || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory)).Chosen() {
						d.openLocalExternalEdit(d.localPane.directory, f.Name)
					}
					if f.IsDir && menu.Item("打开目录").Disabled(d.localPane.loading).Chosen() {
						d.requestLocalPane(filepath.Join(d.localPane.directory, f.Name), -1)
					}
					if eligiblePaneItem(f) && menu.Item("复制到会话当前目录").Disabled(!d.canUploadPaneFile(item)).Chosen() {
						d.uploadPaneFile(item)
					}
					if menu.Item("复制路径").Chosen() {
						if full, err := localItemPath(d.localPane.directory, f.Name); err == nil {
							mygo.Clipboard.WriteText(full)
							d.localPane.status = "已复制路径"
						}
					}
				})
				if d.fileFolderDrop(c, row, false, f) {
					return
				}
				if row.DoubleClicked() {
					if f.IsDir {
						d.requestLocalPane(filepath.Join(d.localPane.directory, f.Name), -1)
					} else if eligibleBatchFile(f) && !d.localPane.loading && sameLocalDirectory(d.localPane.input, d.localPane.directory) {
						d.openLocalExternalEdit(d.localPane.directory, f.Name)
					}
				} else if row.Clicked() {
					selected, err := d.localPane.paneSelection.choose(f.Name, visible, row.ClickModifiers())
					if err != nil {
						d.localPane.status = err.Error()
					} else {
						d.localPane.selected = selected
					}
				}
			})
		}
	})
	if len(d.localPane.paneSelection.names) > 1 {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("已选择 %d 项", len(d.localPane.paneSelection.names))).FontSize(12).TextColor(c.Theme().TextMuted)
			if ui.Button(c.Key("local-pane-clear-selection"), "清除").Label("清除本机文件选择").Disabled(d.busy).Clicked() {
				d.localPane.paneSelection = paneSelection{}
				d.localPane.selected = ""
			}
		})
	}
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		localEligible, remoteEligible := false, false
		for _, entry := range d.localPane.files {
			if entry.Name == d.localPane.selected && eligiblePaneItem(entry) {
				localEligible = true
			}
			if entry.Name == d.localPane.selected && entry.IsDir {
				if ui.Button(c.Key("local-pane-open-selected"), "打开所选目录").Label("打开本机所选目录").Disabled(d.localPane.loading).Clicked() {
					d.requestLocalPane(filepath.Join(d.localPane.directory, entry.Name), -1)
				}
				break
			}
		}
		for _, entry := range d.files {
			if entry.Name == d.selectedFile && eligiblePaneItem(entry) {
				remoteEligible = true
				break
			}
		}
		origin := d.localPaneDrag(d.localPane.selected)
		pathPending := d.localPane.directory == "" || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory)
		uploadLabel := "复制到会话 →"
		if len(origin.items) > 1 {
			uploadLabel = fmt.Sprintf("复制 %d 项 →", len(origin.items))
		}
		if ui.Button(c.Key("local-pane-upload"), uploadLabel).Disabled(!localEligible || pathPending || !d.canUploadPaneFile(origin)).Clicked() {
			d.uploadPaneFile(origin)
		}
		source := d.rightPaneDrag(d.selectedFile)
		_, downloadErr := d.paneDownloadTarget(source)
		downloadLabel := "← 复制到本机"
		if len(source.items) > 1 {
			downloadLabel = fmt.Sprintf("← 复制 %d 项", len(source.items))
		}
		if ui.Button(c.Key("local-pane-download"), downloadLabel).Disabled(!remoteEligible || pathPending || downloadErr != nil).Clicked() {
			d.downloadPaneFile(source)
		}
	})
	if d.cancelTransfer != nil && ui.Button(c.Key("left-cancel-transfer"), "取消传输").Clicked() {
		d.cancelFileTransfer()
	}
}

func (d *desktop) localPaneDrag(name string) localPaneDrag {
	return localPaneDrag{directory: d.localPane.directory, name: name, generation: d.localPane.generation, sessionID: d.currentSession, shell: d.shell, remoteDir: d.fileDir, items: d.localPane.paneSelection.itemsFor(name, d.localPane.files)}
}

func (d *desktop) localPaneDragForFile(file remoteFile) localPaneDrag {
	return localPaneDrag{directory: d.localPane.directory, name: file.Name, generation: d.localPane.generation, sessionID: d.currentSession, shell: d.shell, remoteDir: d.fileDir, items: d.localPane.paneSelection.itemsForFile(file)}
}

func (d *desktop) paneUploadSource(item localPaneDrag) (string, error) {
	if d.busy || d.win == nil || d.fileSessionUnavailable() || item.shell != d.shell || item.sessionID != d.currentSession || item.remoteDir != d.fileDir || item.directory != d.localPane.directory || item.generation != d.localPane.generation || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory) || !d.rightDirectoryReady() {
		return "", errors.New("会话或目录已改变，请重新选择文件")
	}
	if !paneItemsInclude(item.items, item.name) || !paneItemsMatch(item.items, d.localPane.files) {
		return "", errors.New("文件列表已改变，请重新选择")
	}
	for _, file := range d.localPane.files {
		if file.Name == item.name && eligiblePaneItem(file) {
			return localItemPath(item.directory, item.name)
		}
	}
	return "", errors.New("请选择本机文件或目录")
}

func (d *desktop) canUploadPaneFile(item localPaneDrag) bool {
	_, err := d.paneUploadSource(item)
	return err == nil
}

func (d *desktop) uploadPaneFile(item localPaneDrag) {
	_, err := d.paneUploadSource(item)
	if err != nil {
		d.fileStatus = err.Error()
		return
	}
	d.startPaneCopy(nil, item.directory, item.shell, item.remoteDir, item.items, true)
}

func (d *desktop) paneDownloadTarget(item remotePaneDrag) (string, error) {
	if d.busy || d.win == nil || d.fileSessionUnavailable() || item.shell != d.shell || item.sessionID != d.currentSession || item.directory != d.fileDir || d.localPane.directory == "" || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory) {
		return "", errors.New("会话或目录已改变，请重新选择文件")
	}
	if !validDirectoryName(item.name) {
		return "", errors.New("文件名不能用于本机目标")
	}
	if !paneItemsInclude(item.items, item.name) || !paneItemsMatch(item.items, d.files) {
		return "", errors.New("文件列表已改变，请重新选择")
	}
	for _, file := range d.files {
		if file.Name == item.name && eligiblePaneItem(file) {
			return localItemPath(d.localPane.directory, item.name)
		}
	}
	return "", errors.New("请选择会话中的文件或目录")
}

func (d *desktop) downloadPaneFile(item remotePaneDrag) {
	_, err := d.paneDownloadTarget(item)
	if err != nil {
		d.fileStatus = err.Error()
		return
	}
	d.startPaneCopy(item.shell, item.directory, nil, d.localPane.directory, item.items, false)
}
