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
	files                              []remoteFile
	history                            fileHistory
	initialized, loading               bool
	generation                         uint64
}

type localPaneDrag struct {
	directory, name string
	generation      uint64
	sessionID       int
	shell           *remoteShell
	remoteDir       string
}

type remotePaneDrag struct {
	directory, name string
	sessionID       int
	shell           *remoteShell
}

func sameLocalDirectory(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (d *desktop) fileWorkspace(c *ui.Context, width, height float32) {
	if !d.sidebarCollapsed {
		width -= workspaceSidebarWidth(width)
	}
	if width < 860 {
		if d.narrowFilePane == "" {
			if d.term == nil && len(d.files) == 0 {
				d.narrowFilePane = "local"
			} else {
				d.narrowFilePane = "session"
			}
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if workspaceButton(c, "narrow-file-local", "本机文件", d.narrowFilePane == "local").Clicked() {
				d.narrowFilePane = "local"
			}
			if workspaceButton(c, "narrow-file-session", "会话文件", d.narrowFilePane == "session").Clicked() {
				d.narrowFilePane = "session"
			}
		})
		if d.narrowFilePane == "local" {
			listHeight := height - 280
			if listHeight < 160 {
				listHeight = 160
			}
			if listHeight > 520 {
				listHeight = 520
			}
			ui.Column(c).FillWidth().Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() { d.localFilePaneView(c, listHeight) })
		} else {
			d.fileView(c, false)
		}
		return
	}
	paneWidth := (width - 48 - 12) / 2
	listHeight := height - 340
	if listHeight < 180 {
		listHeight = 180
	}
	if listHeight > 520 {
		listHeight = 520
	}
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).Children(func() {
		local := ui.Column(c.Key("local-file-pane")).Label("本机文件面板").Width(paneWidth).MinHeight(listHeight + 210).Shrink(0).Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8)
		local.Children(func() { d.localFilePaneView(c, listHeight) })
		if item, ok := ui.Drop[remotePaneDrag](local); ok {
			d.downloadPaneFile(item)
		}
		remote := ui.Column(c.Key("session-file-pane")).Label("会话文件面板").Width(paneWidth).MinHeight(listHeight + 210).Shrink(0).Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8)
		remote.Children(func() { d.fileView(c, true) })
		if files := remote.DroppedFiles(); len(files) > 0 {
			d.handleFileDrop(files)
		}
		if item, ok := ui.Drop[localPaneDrag](remote); ok {
			d.uploadPaneFile(item)
		}
	})
}

func (d *desktop) initializeLocalPane() {
	if d.localPane.initialized || d.win == nil {
		return
	}
	d.localPane.initialized = true
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
	d.localPane.input, d.localPane.loading = dir, true
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
	if err != nil {
		d.localPane.input = d.localPane.directory
		d.localPane.status = "读取本机目录失败：" + err.Error()
		return
	}
	if historyIndex < 0 || !d.localPane.history.commitNavigation(historyIndex, dir) {
		d.localPane.history.visit(dir)
	}
	d.localPane.directory, d.localPane.input = dir, dir
	d.localPane.files, d.localPane.selected = files, ""
	d.localPane.status = fmt.Sprintf("%d 个条目", len(files))
}

func (d *desktop) navigateLocalPane(offset int) {
	index, dir, ok := d.localPane.history.target(offset)
	if ok {
		d.requestLocalPane(dir, index)
	}
}

func (d *desktop) localFilePaneView(c *ui.Context, listHeight float32) {
	d.initializeLocalPane()
	ui.Text(c, "本机").FontSize(16).Bold()
	ui.TextInput(c.Key("local-pane-path"), &d.localPane.input).Label("本机目录").Placeholder("本机绝对目录路径").OnChange(func() {
		d.localPane.selected = ""
		if d.localPane.loading {
			d.localPane.generation++
			d.localPane.loading = false
		}
		if !sameLocalDirectory(d.localPane.input, d.localPane.directory) {
			d.localPane.status = "打开输入的目录后才能传输。"
		}
	}).OnSubmit(func() { d.requestLocalPane(d.localPane.input, -1) }).FillWidth()
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		_, _, back := d.localPane.history.target(-1)
		_, _, forward := d.localPane.history.target(1)
		if ui.Button(c.Key("local-pane-back"), "‹").Label("本机目录后退").Tooltip("后退").Width(36).Disabled(!back).Clicked() {
			d.navigateLocalPane(-1)
		}
		if ui.Button(c.Key("local-pane-forward"), "›").Label("本机目录前进").Tooltip("前进").Width(36).Disabled(!forward).Clicked() {
			d.navigateLocalPane(1)
		}
		if ui.Button(c.Key("local-pane-parent"), "↑").Label("本机上一级目录").Tooltip("上一级").Width(36).Disabled(d.localPane.directory == "").Clicked() {
			d.requestLocalPane(filepath.Dir(d.localPane.directory), -1)
		}
		if ui.Button(c.Key("local-pane-open"), "打开目录").Clicked() {
			d.requestLocalPane(d.localPane.input, -1)
		}
	})
	if d.localPane.status != "" {
		ui.Text(c, d.localPane.status).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	}
	ui.Row(c).FillWidth().Padding(6, 8).Children(func() {
		ui.Text(c, "名称").Grow(1).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Text(c, "大小").Width(70).FontSize(12).TextColor(c.Theme().TextMuted)
	})
	ui.Scroll(c.Key("local-pane-list")).Label("本机文件列表").Height(listHeight).FillWidth().Children(func() {
		for _, entry := range d.localPane.files {
			f := entry
			label := f.Name
			if f.IsDir {
				label = "📁 " + label
			}
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				row := ui.Button(c.Key("local-pane-file-"+f.Name), "").Label(label).BorderWidth(0).Justify(ui.Start).Grow(1).Gap(8)
				if d.localPane.selected == f.Name {
					row = row.Background(c.Theme().SurfacePressed)
				}
				row.Children(func() {
					icon, color := documentIcon, c.Theme().TextMuted
					if f.IsDir {
						icon, color = fileIcon, c.Theme().AccentHover
					}
					ui.Icon(c, icon).Width(16).Height(16).Shrink(0).TextColor(color)
					ui.Text(c, f.Name)
				})
				if eligibleBatchFile(f) {
					row = row.Drag(localPaneDrag{directory: d.localPane.directory, name: f.Name, generation: d.localPane.generation, sessionID: d.currentSession, shell: d.shell, remoteDir: d.fileDir})
				}
				row.ContextMenu(func(menu *ui.Menu) {
					if f.IsDir && menu.Item("打开目录").Disabled(d.localPane.loading).Chosen() {
						d.requestLocalPane(filepath.Join(d.localPane.directory, f.Name), -1)
					}
					if eligibleBatchFile(f) && menu.Item("上传到会话").Disabled(d.busy || d.shell == nil || d.fileSessionUnavailable() || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory)).Chosen() {
						d.uploadPaneFile(localPaneDrag{directory: d.localPane.directory, name: f.Name, generation: d.localPane.generation, sessionID: d.currentSession, shell: d.shell, remoteDir: d.fileDir})
					}
					if menu.Item("复制路径").Chosen() {
						if full, err := localItemPath(d.localPane.directory, f.Name); err == nil {
							mygo.Clipboard.WriteText(full)
							d.localPane.status = "已复制路径"
						}
					}
				})
				if row.DoubleClicked() && f.IsDir {
					d.requestLocalPane(filepath.Join(d.localPane.directory, f.Name), -1)
				} else if row.Clicked() {
					d.localPane.selected = f.Name
				}
				ui.Text(c, fileSizeLabel(f)).Width(70).FontSize(12).TextColor(c.Theme().TextMuted)
			})
		}
	})
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		localEligible, remoteEligible := false, false
		for _, entry := range d.localPane.files {
			if entry.Name == d.localPane.selected && eligibleBatchFile(entry) {
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
			if entry.Name == d.selectedFile && eligibleBatchFile(entry) {
				remoteEligible = true
				break
			}
		}
		origin := localPaneDrag{directory: d.localPane.directory, name: d.localPane.selected, generation: d.localPane.generation, sessionID: d.currentSession, shell: d.shell, remoteDir: d.fileDir}
		pathPending := d.localPane.directory == "" || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory)
		if ui.Button(c.Key("local-pane-upload"), "上传到会话 →").Disabled(d.busy || d.shell == nil || d.fileSessionUnavailable() || !localEligible || pathPending).Clicked() {
			d.uploadPaneFile(origin)
		}
		source := remotePaneDrag{directory: d.fileDir, name: d.selectedFile, sessionID: d.currentSession, shell: d.shell}
		if ui.Button(c.Key("local-pane-download"), "← 下载到本机").Disabled(d.busy || d.shell == nil || d.fileSessionUnavailable() || !remoteEligible || pathPending).Clicked() {
			d.downloadPaneFile(source)
		}
	})
}

func (d *desktop) paneUploadSource(item localPaneDrag) (string, error) {
	if d.busy || d.win == nil || d.shell == nil || d.fileSessionUnavailable() || item.shell != d.shell || item.sessionID != d.currentSession || item.remoteDir != d.fileDir || item.directory != d.localPane.directory || item.generation != d.localPane.generation || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory) {
		return "", errors.New("会话或目录已改变，请重新选择文件")
	}
	for _, file := range d.localPane.files {
		if file.Name == item.name && eligibleBatchFile(file) {
			return localItemPath(item.directory, item.name)
		}
	}
	return "", errors.New("请选择本机普通文件")
}

func (d *desktop) uploadPaneFile(item localPaneDrag) {
	local, err := d.paneUploadSource(item)
	if err != nil {
		d.fileStatus = err.Error()
		return
	}
	d.fileLocal = local
	d.transfer(true)
}

func (d *desktop) paneDownloadTarget(item remotePaneDrag) (string, error) {
	if d.busy || d.win == nil || d.shell == nil || d.fileSessionUnavailable() || item.shell != d.shell || item.sessionID != d.currentSession || item.directory != d.fileDir || d.localPane.directory == "" || d.localPane.loading || !sameLocalDirectory(d.localPane.input, d.localPane.directory) {
		return "", errors.New("会话或目录已改变，请重新选择文件")
	}
	if !validDirectoryName(item.name) {
		return "", errors.New("文件名不能用于本机目标")
	}
	for _, file := range d.files {
		if file.Name == item.name && eligibleBatchFile(file) {
			return localItemPath(d.localPane.directory, item.name)
		}
	}
	return "", errors.New("请选择会话中的普通文件")
}

func (d *desktop) downloadPaneFile(item remotePaneDrag) {
	local, err := d.paneDownloadTarget(item)
	if err != nil {
		d.fileStatus = err.Error()
		return
	}
	d.fileLocal, d.selectedFile = local, item.name
	d.transfer(false)
}
