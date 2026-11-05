package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"path"
	"path/filepath"
	"strings"
	"time"
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

func (d *desktop) refreshFiles() {
	if d.busy || d.term == nil {
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
				return
			}
			d.files = files
			d.fileStatus = fmt.Sprintf("%d 个条目", len(files))
		})
	}()
}
func (d *desktop) transfer(upload bool) {
	if d.busy || d.term == nil || d.fileLocal == "" {
		return
	}
	shell, dir, local, selected, sessionID := d.shell, d.fileDir, d.fileLocal, d.selectedFile, d.currentSession
	if !upload && selected == "" {
		return
	}
	d.busy = true
	d.fileStatus = "正在传输…"
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelTransfer = cancel
	go func() {
		defer cancel()
		progress := func(read int64) {
			d.win.Update(func() {
				if ctx.Err() == nil && d.cancelTransfer != nil && sessionID == d.currentSession && shell == d.shell && dir == d.fileDir {
					d.fileStatus = "正在传输，已读取 " + fileSizeLabel(remoteFile{Size: read})
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
			d.busy = false
			d.cancelTransfer = nil
			if sessionID != d.currentSession || shell != d.shell {
				return
			}
			if dir != d.fileDir {
				if err != nil {
					d.fileStatus = "先前目录的传输失败：" + err.Error()
				} else {
					d.fileStatus = "先前目录的传输已完成；当前目录已改变，请刷新"
				}
				return
			}
			if errors.Is(err, context.Canceled) {
				d.fileStatus = "传输已取消；未完成的目标文件已清理"
				return
			}
			if err != nil {
				d.fileStatus = fmt.Sprintf("传输失败：%v", err)
				return
			}
			d.fileStatus = "传输完成"
			if upload {
				d.refreshFiles()
			}
		})
	}()
}
func (d *desktop) removeSelected() {
	if d.busy || d.term == nil || d.selectedFile == "" {
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
func (d *desktop) fileView(c *ui.Context, compact bool) {
	local := d.term != nil && d.shell == nil
	title, help := "远程文件", "选择目录浏览，拖入单个文件上传；删除前需要再次确认。"
	if local {
		title, help = "本机文件", "选择目录浏览，拖入单个文件复制；删除前需要再次确认。"
	}
	ui.Text(c, title).FontSize(16).Bold()
	ui.Text(c, help).TextColor(c.Theme().TextMuted).Wrap()
	ui.TextInput(c.Key("remote-dir"), &d.fileDir).Label("当前目录").Placeholder("目录路径").OnChange(func() { d.selectedFile = ""; d.confirmDelete = ""; d.files = nil })
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c.Key("file-parent"), "上一级").Disabled(d.busy || d.term == nil).Clicked() {
			if local {
				d.fileDir = filepath.Dir(d.fileDir)
			} else {
				d.fileDir = path.Join(d.fileDir, "..")
			}
			d.selectedFile, d.confirmDelete, d.fileStatus = "", "", ""
			d.files = nil
			d.refreshFiles()
		}
		if ui.Button(c.Key("file-refresh"), "打开目录").Disabled(d.busy || d.term == nil).Clicked() {
			d.selectedFile = ""
			d.confirmDelete = ""
			d.refreshFiles()
		}
	})
	if d.term == nil {
		ui.Text(c, "连接 SSH 或打开本机终端后可浏览文件。").TextColor(c.Theme().TextMuted).Wrap()
	}
	d.fileActionsView(c, local, compact)
	ui.TextInput(c.Key("file-filter"), &d.fileFilter).Label("筛选文件").Placeholder("按名称筛选当前目录")
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
	query := strings.ToLower(strings.TrimSpace(d.fileFilter))
	shown := 0
	for _, entry := range d.files {
		f := entry
		if query != "" && !strings.Contains(strings.ToLower(f.Name), query) {
			continue
		}
		shown++
		label := f.Name
		if f.IsDir {
			label = "📁 " + label
		}
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			row := ui.Button(c.Key("file-"+f.Name), label).Grow(1).Disabled(d.busy)
			row.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("选择").Chosen() {
					d.selectedFile = f.Name
					d.confirmDelete = ""
					d.fileStatus = "已选择：" + f.Name
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
					if local {
						d.fileDir = filepath.Join(d.fileDir, f.Name)
					} else {
						d.fileDir = path.Join(d.fileDir, f.Name)
					}
					d.selectedFile = ""
					d.refreshFiles()
				} else {
					d.selectedFile = f.Name
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
	if len(d.files) > 0 && shown == 0 {
		ui.Text(c, "没有匹配的文件").TextColor(c.Theme().TextMuted)
	}
}

func (d *desktop) fileActionsView(c *ui.Context, local, compact bool) {
	dropLabel := "拖入一个文件上传"
	if local {
		dropLabel = "拖入一个文件复制到当前目录"
	}
	drop := ui.Box(c.Key("file-drop")).Height(48).Border(1, c.Theme().Border).Padding(8).Label(dropLabel)
	drop.Children(func() { ui.Text(c, dropLabel).FontSize(12) })
	if files := drop.DroppedFiles(); len(files) > 0 {
		if d.busy || d.term == nil {
			d.fileStatus = "请先连接后再上传"
		} else if len(files) != 1 {
			d.fileStatus = "每次只能拖入一个文件"
		} else {
			d.fileLocal = files[0]
			d.transfer(true)
		}
	}
	ui.TextInput(c.Key("local-file"), &d.fileLocal).Label("本地文件路径").Placeholder("本地文件完整路径")
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
		if ui.Button(c.Key("upload"), uploadShort).Label(uploadLabel).Disabled(d.busy || d.term == nil).Clicked() {
			d.transfer(true)
		}
		if ui.Button(c.Key("download"), downloadShort).Label(downloadLabel).Disabled(d.busy || d.term == nil || d.selectedFile == "").Clicked() {
			d.transfer(false)
		}
		if ui.Button(c.Key("delete"), deleteShort).Label("删除选中文件（需确认）").Disabled(d.busy || d.term == nil || d.selectedFile == "").Clicked() {
			d.removeSelected()
		}
		if d.cancelTransfer != nil && ui.Button(c.Key("cancel-transfer"), "取消传输").Clicked() {
			d.cancelTransfer()
			d.fileStatus = "正在取消传输…"
		}
	})
	ui.Text(c, d.fileStatus).FontSize(12)
}
