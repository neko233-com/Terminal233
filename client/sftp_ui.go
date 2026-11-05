package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
)

func fileSizeLabel(f remoteFile) string {
	if f.IsDir || f.IsLink || f.Size < 0 {
		return "—"
	}
	if f.Size < 1024 {
		return fmt.Sprintf("%d B", f.Size)
	}
	size := float64(f.Size)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := 0
	for size >= 1024 && i < len(units)-1 {
		size /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", size, units[i])
}

func fileModifiedLabel(f remoteFile) string {
	if f.Modified <= 0 {
		return "—"
	}
	return time.Unix(f.Modified, 0).Local().Format("2006-01-02 15:04")
}

func terminalDirectory(reported string, local bool) (string, error) {
	if reported == "" || strings.IndexFunc(reported, unicode.IsControl) >= 0 {
		return "", errors.New("终端尚未报告有效目录（需要 shell 支持 OSC 7）")
	}
	if !local {
		if !path.IsAbs(reported) {
			return "", errors.New("终端报告的远程目录不是绝对路径")
		}
		return path.Clean(reported), nil
	}
	if runtime.GOOS == "windows" && len(reported) >= 4 && reported[0] == '/' && reported[2] == ':' && reported[3] == '/' {
		reported = reported[1:]
	}
	reported = filepath.FromSlash(reported)
	if !filepath.IsAbs(reported) || (runtime.GOOS == "windows" && strings.HasPrefix(reported, `\\`)) {
		return "", errors.New("终端报告的本机目录不是有效的本地绝对路径")
	}
	return filepath.Clean(reported), nil
}

// Follow only changes reported by the shell, so a manual browse stays put
// until the shell itself reports a different working directory.
func (d *desktop) applyTerminalDirectory(reported string, force bool) bool {
	if d.busy || d.fileSessionUnavailable() {
		return false
	}
	dir, err := terminalDirectory(reported, d.shell == nil)
	if err != nil {
		if force {
			d.fileStatus = err.Error()
		}
		return false
	}
	if !force && dir == d.lastTerminalDir {
		return false
	}
	d.lastTerminalDir = dir
	if !force && dir == d.fileDir {
		return false
	}
	d.fileDir, d.fileStatus = dir, "正在定位终端目录…"
	d.selectedFile, d.confirmDelete, d.fileRename, d.fileNewDirectory = "", "", "", ""
	d.files = nil
	if d.win != nil {
		d.refreshFiles()
	}
	return true
}

func (d *desktop) followReportedDirectory(force bool) {
	if d.term != nil {
		d.applyTerminalDirectory(d.term.Dir(), force)
	}
}

func (d *desktop) refreshFiles() { d.refreshFilesAfter("") }

func (d *desktop) refreshFilesAfter(previousStatus string) {
	d.refreshFilesWithHistory(previousStatus, -1)
}

func (d *desktop) navigateFileHistory(offset int) {
	if d.busy || d.fileSessionUnavailable() {
		return
	}
	index, dir, ok := d.fileHistory.target(offset)
	if !ok {
		return
	}
	d.fileDir, d.files = dir, nil
	d.selectedFile, d.confirmDelete, d.fileRename, d.fileNewDirectory = "", "", "", ""
	d.refreshFilesWithHistory("", index)
}

func (d *desktop) refreshFilesWithHistory(previousStatus string, historyIndex int) {
	if d.busy || d.fileSessionUnavailable() {
		return
	}
	d.busy = true
	shell, dir, sessionID := d.shell, d.fileDir, d.currentSession
	go func() {
		var files []remoteFile
		var err error
		if shell == nil {
			files, err = listLocal(dir)
		} else {
			files, err = listRemote(shell, dir)
		}
		d.win.Update(func() {
			d.busy = false
			if sessionID != d.currentSession || shell != d.shell {
				return
			}
			if dir != d.fileDir {
				d.fileStatus = "目录已改变，请重新打开目录"
				return
			}
			if err != nil {
				d.fileStatus = fmt.Sprintf("读取文件失败：%v", err)
				if previousStatus != "" {
					d.fileStatus = previousStatus + "；" + d.fileStatus
				}
				return
			}
			if historyIndex < 0 || !d.fileHistory.commitNavigation(historyIndex, dir) {
				d.fileHistory.visit(dir)
			}
			d.files = files
			d.fileStatus = fmt.Sprintf("%d 个条目", len(files))
			if previousStatus != "" {
				d.fileStatus = previousStatus + "；" + d.fileStatus
			}
		})
	}()
}

func (d *desktop) transferProgress(sessionID int, shell *remoteShell, dir string, read int64) {
	status := "正在传输，已读取 " + fileSizeLabel(remoteFile{Size: read})
	if sessionID == d.currentSession {
		if shell == d.shell && dir == d.fileDir {
			d.fileStatus = status
		}
		return
	}
	for _, session := range d.sessions {
		if session.id == sessionID && session.shell == shell && session.fileDir == dir {
			session.fileStatus = status
			return
		}
	}
}

func transferErrorText(err error) string {
	// Only translate a direct filesystem error. Wrapped errors can describe a
	// failed cleanup and must retain that important context.
	if pathErr, ok := err.(*os.PathError); ok {
		switch {
		case errors.Is(pathErr, os.ErrExist):
			return "目标文件已存在，不会覆盖"
		case errors.Is(pathErr, os.ErrNotExist):
			return "文件或目标目录不存在"
		case errors.Is(pathErr, os.ErrPermission):
			return "没有权限访问文件或目标目录"
		}
	}
	return err.Error()
}

func transferUpdatesDirectory(shell *remoteShell, dir, local string, upload bool) bool {
	if upload {
		return true
	}
	if shell != nil || local == "" {
		return false
	}
	directory, e1 := filepath.Abs(dir)
	target, e2 := filepath.Abs(filepath.Dir(local))
	if e1 != nil || e2 != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(directory, target)
	}
	return directory == target
}

func (d *desktop) finishTransfer(sessionID int, shell *remoteShell, dir string, refresh bool, err error) {
	status := "传输完成"
	if errors.Is(err, context.Canceled) {
		status = "传输已取消；未完成的目标文件已清理"
	} else if err != nil {
		status = "传输失败：" + transferErrorText(err)
	}
	d.finishTransferStatus(sessionID, shell, dir, status, refresh && err == nil)
}

func (d *desktop) finishTransferStatus(sessionID int, shell *remoteShell, dir, status string, refresh bool) {
	d.busy = false
	d.cancelTransfer = nil
	d.transferSessionID = 0
	if sessionID == d.currentSession && shell == d.shell {
		if dir != d.fileDir {
			d.fileStatus = "先前目录的" + status + "；当前目录未更改"
			return
		}
		d.fileStatus = status
		if refresh && d.win != nil {
			d.refreshFilesAfter(status)
		}
		return
	}
	for _, session := range d.sessions {
		if session.id != sessionID || session.shell != shell {
			continue
		}
		if dir != session.fileDir {
			session.fileStatus = "先前目录的" + status + "；当前目录未更改"
		} else {
			session.fileStatus = status
			if refresh {
				session.files = nil
				session.refreshPending = true
			}
		}
		return
	}
}

func (d *desktop) transferBatch(files []string) {
	if d.busy || d.fileSessionUnavailable() || len(files) == 0 {
		return
	}
	if len(files) > maxBatchFiles {
		d.fileStatus = fmt.Sprintf("每次最多传输 %d 个文件", maxBatchFiles)
		return
	}
	if strings.TrimSpace(d.fileDir) == "" {
		d.fileStatus = "请先填写当前目录"
		return
	}
	files = append([]string(nil), files...)
	shell, dir, sessionID := d.shell, d.fileDir, d.currentSession
	d.busy = true
	d.fileStatus = fmt.Sprintf("正在传输 0/%d…", len(files))
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelTransfer, d.transferSessionID = cancel, sessionID
	go func() {
		defer cancel()
		result := runBatchUpload(ctx, files, func(local string, progress func(int64)) error {
			if shell != nil {
				return uploadFileContext(ctx, shell, local, dir, progress)
			}
			target, err := localItemPath(dir, filepath.Base(local))
			if err != nil {
				return err
			}
			return copyLocalFileContext(ctx, local, target, progress)
		}, func(index, total int, name string, read int64) {
			d.win.Update(func() {
				if ctx.Err() != nil || d.cancelTransfer == nil || d.transferSessionID != sessionID {
					return
				}
				status := fmt.Sprintf("正在传输 %d/%d · %s · 已读取 %s", index, total, name, fileSizeLabel(remoteFile{Size: read}))
				if sessionID == d.currentSession && d.shell == shell && d.fileDir == dir {
					d.fileStatus = status
					return
				}
				for _, s := range d.sessions {
					if s.id == sessionID && s.shell == shell && s.fileDir == dir {
						s.fileStatus = status
						return
					}
				}
			})
		})
		d.win.Update(func() { d.finishTransferStatus(sessionID, shell, dir, result.status(), result.succeeded > 0) })
	}()
}

func (d *desktop) transfer(upload bool) {
	if d.busy || d.fileSessionUnavailable() || d.fileLocal == "" {
		return
	}
	if strings.TrimSpace(d.fileDir) == "" {
		d.fileStatus = "请先填写当前目录"
		return
	}
	shell, dir, local, selected, sessionID := d.shell, d.fileDir, d.fileLocal, d.selectedFile, d.currentSession
	refresh := transferUpdatesDirectory(shell, dir, local, upload)
	if !upload && selected == "" {
		return
	}
	d.busy = true
	d.fileStatus = "正在传输…"
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelTransfer = cancel
	d.transferSessionID = sessionID
	go func() {
		defer cancel()
		progress := func(read int64) {
			d.win.Update(func() {
				if ctx.Err() == nil && d.cancelTransfer != nil && d.transferSessionID == sessionID {
					d.transferProgress(sessionID, shell, dir, read)
				}
			})
		}
		var err error
		if shell == nil && upload {
			var target string
			target, err = localItemPath(dir, filepath.Base(local))
			if err == nil {
				err = copyLocalFileContext(ctx, local, target, progress)
			}
		} else if shell == nil {
			var source string
			source, err = localItemPath(dir, selected)
			if err == nil {
				err = copyLocalFileContext(ctx, source, local, progress)
			}
		} else if upload {
			err = uploadFileContext(ctx, shell, local, dir, progress)
		} else {
			err = downloadFileContext(ctx, shell, path.Join(dir, selected), local, progress)
		}
		d.win.Update(func() {
			d.finishTransfer(sessionID, shell, dir, refresh, err)
		})
	}()
}
func (d *desktop) removeSelected() {
	if d.busy || d.fileSessionUnavailable() || d.selectedFile == "" {
		return
	}
	if d.confirmDelete != d.selectedFile {
		d.confirmDelete = d.selectedFile
		d.fileStatus = "再次点击确认删除：" + d.selectedFile
		return
	}
	shell, dir, name, sessionID := d.shell, d.fileDir, d.selectedFile, d.currentSession
	d.busy = true
	d.confirmDelete = ""
	go func() {
		var err error
		if shell == nil {
			err = deleteLocalItem(dir, name)
		} else {
			err = deleteRemote(shell, path.Join(dir, name))
		}
		d.win.Update(func() {
			d.busy = false
			if sessionID != d.currentSession || shell != d.shell {
				return
			}
			if dir != d.fileDir {
				if err != nil {
					d.fileStatus = "先前目录的删除失败：" + err.Error()
				} else {
					d.fileStatus = "先前目录的删除已完成；当前目录已改变，请刷新"
				}
				return
			}
			if err != nil {
				d.fileStatus = fmt.Sprintf("删除失败：%v", err)
				return
			}
			d.selectedFile = ""
			d.refreshFiles()
		})
	}()
}

func (d *desktop) renameSelected() {
	if d.busy || d.fileSessionUnavailable() || d.selectedFile == "" {
		return
	}
	oldName, newName, shell, dir, sessionID := d.selectedFile, d.fileRename, d.shell, d.fileDir, d.currentSession
	if !validDirectoryName(newName) {
		d.fileStatus = "请输入单个有效文件名称"
		return
	}
	d.busy = true
	d.fileStatus = "正在重命名…"
	go func() {
		err := renameFile(shell, dir, oldName, newName)
		d.win.Update(func() {
			d.busy = false
			if sessionID != d.currentSession || shell != d.shell {
				return
			}
			if err != nil {
				d.fileStatus = "重命名失败：" + transferErrorText(err)
				return
			}
			if dir != d.fileDir {
				d.fileStatus = "先前目录中的文件已重命名；当前目录未更改"
				return
			}
			d.selectedFile, d.fileRename, d.confirmDelete = newName, "", ""
			d.refreshFiles()
		})
	}()
}

func (d *desktop) createDirectory() {
	if d.busy || d.fileSessionUnavailable() {
		return
	}
	name, shell, dir, sessionID := d.fileNewDirectory, d.shell, d.fileDir, d.currentSession
	if !validDirectoryName(name) {
		d.fileStatus = "请输入单个有效目录名称"
		return
	}
	d.busy = true
	d.fileStatus = "正在创建目录…"
	go func() {
		err := makeDirectory(shell, dir, name)
		d.win.Update(func() {
			d.busy = false
			if sessionID != d.currentSession || shell != d.shell {
				return
			}
			if err != nil {
				d.fileStatus = "创建目录失败：" + transferErrorText(err)
				return
			}
			if dir != d.fileDir {
				d.fileStatus = "目录已在先前位置创建；当前目录未更改"
				return
			}
			if d.fileNewDirectory == name {
				d.fileNewDirectory = ""
			}
			d.fileStatus = "目录已创建：" + name
			d.refreshFiles()
		})
	}()
}

// displayedFiles sorts a copy so a view change cannot mutate a session's snapshot.
func displayedFiles(files []remoteFile, query, order string, descending, hideDotfiles bool) []remoteFile {
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]remoteFile, 0, len(files))
	for _, f := range files {
		if hideDotfiles && strings.HasPrefix(f.Name, ".") {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(f.Name), query) {
			continue
		}
		result = append(result, f)
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		if order == "size" || order == "time" {
			av, bv := a.Size, b.Size
			if order == "time" {
				av, bv = a.Modified, b.Modified
			}
			unknownA, unknownB := av < 0, bv < 0
			if order == "time" {
				unknownA, unknownB = av <= 0, bv <= 0
			}
			if order == "size" {
				unknownA, unknownB = unknownA || a.IsDir || a.IsLink, unknownB || b.IsDir || b.IsLink
			}
			if unknownA != unknownB {
				return !unknownA
			}
			if !unknownA && av != bv {
				if descending {
					return av > bv
				}
				return av < bv
			}
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an == bn {
			return a.Name < b.Name
		}
		if order == "" || order == "name" {
			if descending {
				return an > bn
			}
		}
		return an < bn
	})
	return result
}

func (d *desktop) setFileSort(order string) {
	if d.fileSort == order || (d.fileSort == "" && order == "name") {
		d.fileSort = order
		d.fileSortDescending = !d.fileSortDescending
	} else {
		d.fileSort, d.fileSortDescending = order, false
	}
}

func (d *desktop) fileView(c *ui.Context, compact bool) {
	local := d.term != nil && d.shell == nil
	title, help := "远程文件", "选择目录浏览，拖入文件上传；删除前需要再次确认。"
	if local {
		title, help = "本机文件", "选择目录浏览，拖入文件复制；删除前需要再次确认。"
	}
	ui.Text(c, title).FontSize(16).Bold()
	for _, session := range d.sessions {
		if session.id == d.currentSession {
			ui.Text(c, "所属会话："+session.label).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			break
		}
	}
	ui.Text(c, help).TextColor(c.Theme().TextMuted).Wrap()
	ui.TextInput(c.Key("remote-dir"), &d.fileDir).Label("当前目录").Placeholder("目录路径").OnChange(func() {
		d.selectedFile = ""
		d.confirmDelete = ""
		d.fileNewDirectory = ""
		d.fileRename = ""
		d.files = nil
	})
	ui.Row(c).Gap(8).Wrap().Children(func() {
		_, _, canBack := d.fileHistory.target(-1)
		_, _, canForward := d.fileHistory.target(1)
		if ui.Button(c.Key("file-back"), "后退").Disabled(d.busy || d.fileSessionUnavailable() || !canBack).Clicked() {
			d.navigateFileHistory(-1)
		}
		if ui.Button(c.Key("file-forward"), "前进").Disabled(d.busy || d.fileSessionUnavailable() || !canForward).Clicked() {
			d.navigateFileHistory(1)
		}
		if ui.Button(c.Key("file-parent"), "上一级").Disabled(d.busy || d.fileSessionUnavailable()).Clicked() {
			if local {
				d.fileDir = filepath.Dir(d.fileDir)
			} else {
				d.fileDir = path.Join(d.fileDir, "..")
			}
			d.selectedFile, d.confirmDelete, d.fileStatus, d.fileNewDirectory, d.fileRename = "", "", "", "", ""
			d.files = nil
			d.refreshFiles()
		}
		if ui.Button(c.Key("file-terminal-dir"), "定位终端目录").Disabled(d.busy || d.fileSessionUnavailable()).Clicked() {
			d.followReportedDirectory(true)
		}
		if ui.Button(c.Key("file-refresh"), "打开目录").Disabled(d.busy || d.fileSessionUnavailable()).Clicked() {
			d.selectedFile, d.confirmDelete, d.fileRename = "", "", ""
			d.refreshFiles()
		}
	})
	label := "跟随终端目录：关"
	if d.fileFollowTerminal {
		label = "跟随终端目录：开"
	}
	if ui.Button(c.Key("file-follow-terminal"), label).Disabled(d.busy || d.fileSessionUnavailable()).Clicked() {
		d.fileFollowTerminal = !d.fileFollowTerminal
		d.lastTerminalDir = ""
		if d.fileFollowTerminal {
			d.followReportedDirectory(true)
		}
	}
	if d.term == nil {
		ui.Text(c, "连接 SSH 或打开本机终端后可浏览文件。").TextColor(c.Theme().TextMuted).Wrap()
	} else if terminalEnded(d.term) {
		ui.Text(c, "当前会话已结束，文件操作不可用；请重新连接或打开终端。").TextColor(c.Theme().TextMuted).Wrap()
	}
	d.fileDropView(c, local)
	if d.fileStatus != "" {
		ui.Text(c, d.fileStatus).FontSize(12).Wrap()
	}
	ui.TextInput(c.Key("file-filter"), &d.fileFilter).Label("筛选文件").Placeholder("按名称筛选当前目录").OnChange(func() {
		d.selectedFile, d.fileRename, d.confirmDelete = "", "", ""
	})
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		for _, column := range []struct{ key, label string }{{"name", "按名称"}, {"size", "按大小"}, {"time", "按修改时间"}} {
			label := column.label
			if d.fileSort == column.key || (d.fileSort == "" && column.key == "name") {
				if d.fileSortDescending {
					label += " ↓"
				} else {
					label += " ↑"
				}
			}
			if ui.Button(c.Key("file-sort-"+column.key), label).Clicked() {
				d.setFileSort(column.key)
			}
		}
		label := "隐藏点文件"
		if d.fileHideDotfiles {
			label = "显示点文件"
		}
		if ui.Button(c.Key("file-toggle-dotfiles"), label).Clicked() {
			d.fileHideDotfiles = !d.fileHideDotfiles
			if d.fileHideDotfiles && strings.HasPrefix(d.selectedFile, ".") {
				d.selectedFile, d.fileRename, d.confirmDelete = "", "", ""
			}
		}
	})
	if len(d.files) > 0 {
		ui.Row(c).FillWidth().Gap(12).Padding(8, 12).Background(c.Theme().Surface).Children(func() {
			ui.Text(c, "名称").Grow(1).TextColor(c.Theme().TextMuted)
			if !compact {
				ui.Text(c, "类型").Width(60).TextColor(c.Theme().TextMuted)
			}
			ui.Text(c, "大小").Width(90).TextColor(c.Theme().TextMuted)
			if !compact {
				ui.Text(c, "修改时间").Width(150).TextColor(c.Theme().TextMuted)
			}
		})
	}
	visible := displayedFiles(d.files, d.fileFilter, d.fileSort, d.fileSortDescending, d.fileHideDotfiles)
	if len(visible) > 0 {
		height := float32(len(visible) * 44)
		maxHeight := float32(320)
		if compact {
			maxHeight = 220
		}
		if height > maxHeight {
			height = maxHeight
		}
		ui.Scroll(c.Key("file-list")).Height(height).FillWidth().Children(func() {
			for _, entry := range visible {
				f := entry
				label := f.Name
				if f.IsDir {
					label = "📁 " + label
				}
				ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
					row := ui.Button(c.Key("file-"+f.Name), label).Grow(1).Disabled(d.busy || d.fileSessionUnavailable())
					row.ContextMenu(func(menu *ui.Menu) {
						if menu.Item("选择").Chosen() {
							d.selectedFile, d.fileRename = f.Name, f.Name
							d.confirmDelete = ""
							d.fileStatus = "已选择：" + f.Name
						}
						if menu.Item("重命名…").Chosen() {
							d.selectedFile, d.fileRename, d.confirmDelete = f.Name, f.Name, ""
							d.fileStatus = "请输入新名称，然后点击重命名：" + f.Name
						}
						if menu.Item("删除…").Chosen() {
							d.selectedFile = f.Name
							d.confirmDelete = ""
							d.removeSelected()
						}
					})
					if row.Clicked() {
						d.confirmDelete = ""
						if f.IsDir {
							d.fileNewDirectory, d.fileRename = "", ""
							if local {
								d.fileDir = filepath.Join(d.fileDir, f.Name)
							} else {
								d.fileDir = path.Join(d.fileDir, f.Name)
							}
							d.selectedFile = ""
							d.refreshFiles()
						} else {
							d.selectedFile = f.Name
							d.fileRename = f.Name
							d.fileStatus = "已选择：" + f.Name
						}
					}
					kind := "文件"
					if f.IsDir {
						kind = "目录"
					} else if f.IsLink {
						kind = "链接"
					}
					if !compact {
						ui.Text(c, kind).Width(60).TextColor(c.Theme().TextMuted)
					}
					ui.Text(c, fileSizeLabel(f)).Width(90).TextColor(c.Theme().TextMuted)
					if !compact {
						ui.Text(c, fileModifiedLabel(f)).Width(150).TextColor(c.Theme().TextMuted)
					}
				})
			}
		})
	}

	if len(d.files) > 0 && len(visible) == 0 {
		ui.Text(c, "没有匹配的文件").TextColor(c.Theme().TextMuted)
	}
	d.fileActionsView(c, local, compact)
	ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.End).Children(func() {
		ui.TextInput(c.Key("new-directory-name"), &d.fileNewDirectory).Label("新目录名称").Placeholder("输入目录名称").Grow(1)
		if ui.Button(c.Key("new-directory-create"), "创建目录").Disabled(d.busy || d.fileSessionUnavailable() || d.fileNewDirectory == "").Clicked() {
			d.createDirectory()
		}
	})
	ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.End).Children(func() {
		ui.TextInput(c.Key("rename-name"), &d.fileRename).Label("新名称").Placeholder("输入选中文件的新名称").Grow(1)
		if ui.Button(c.Key("rename-selected"), "重命名").Disabled(d.busy || d.fileSessionUnavailable() || d.selectedFile == "" || d.fileRename == "").Clicked() {
			d.renameSelected()
		}
	})
	if d.shell != nil {
		ui.Text(c, "远程重命名前会检查目标；并发写入时能否防覆盖取决于服务器。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
	}
}

func (d *desktop) fileDropView(c *ui.Context, local bool) {
	dropLabel := "拖入文件上传（最多 100 个）"
	if local {
		dropLabel = "拖入文件复制到当前目录（最多 100 个）"
	}
	drop := ui.Box(c.Key("file-drop")).Height(48).Border(1, c.Theme().Border).Padding(8).Label(dropLabel)
	drop.Children(func() { ui.Text(c, dropLabel).FontSize(12) })
	if files := drop.DroppedFiles(); len(files) > 0 {
		if d.busy || d.fileSessionUnavailable() {
			d.fileStatus = "请先连接后再上传"
		} else if len(files) == 1 {
			d.fileLocal = files[0]
			d.transfer(true)
		} else {
			d.transferBatch(files)
		}
	}
}

func (d *desktop) fileActionsView(c *ui.Context, local, compact bool) {
	ui.TextInput(c.Key("local-file"), &d.fileLocal).Label("本地文件路径").Placeholder("本地文件完整路径")
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		if ui.Button(c.Key("pick-upload"), "选择源文件…").Disabled(d.busy || d.fileSessionUnavailable()).Clicked() {
			d.pickLocalPath("upload")
		}
		if ui.Button(c.Key("pick-download"), "选择目标路径…").Disabled(d.busy || d.fileSessionUnavailable() || d.selectedFile == "").Clicked() {
			d.pickLocalPath("download")
		}
	})
	uploadLabel, downloadLabel := "上传本地文件", "下载选中文件"
	if local {
		uploadLabel, downloadLabel = "复制文件到当前目录", "复制选中文件到指定路径"
	}
	uploadShort, downloadShort, deleteShort := uploadLabel, downloadLabel, "删除选中文件（需确认）"
	if compact {
		if local {
			uploadShort, downloadShort = "复制到此", "复制选中"
		} else {
			uploadShort, downloadShort = "上传", "下载"
		}
		deleteShort = "删除"
	}
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		if ui.Button(c.Key("upload"), uploadShort).Label(uploadLabel).Disabled(d.busy || d.fileSessionUnavailable()).Clicked() {
			d.transfer(true)
		}
		if ui.Button(c.Key("download"), downloadShort).Label(downloadLabel).Disabled(d.busy || d.fileSessionUnavailable() || d.selectedFile == "").Clicked() {
			d.transfer(false)
		}
		if ui.Button(c.Key("delete"), deleteShort).Label("删除选中文件（需确认）").Disabled(d.busy || d.fileSessionUnavailable() || d.selectedFile == "").Clicked() {
			d.removeSelected()
		}
		if d.cancelTransfer != nil {
			cancelLabel := "取消传输"
			if d.transferSessionID != d.currentSession {
				cancelLabel = "取消后台传输"
			}
			if ui.Button(c.Key("cancel-transfer"), cancelLabel).Clicked() {
				d.cancelTransfer()
				if d.transferSessionID == d.currentSession {
					d.fileStatus = "正在取消传输…"
				} else {
					d.status = "正在取消后台传输…"
				}
			}
		}
	})
}
