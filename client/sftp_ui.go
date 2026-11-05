package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"path"
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
	if d.busy || d.shell == nil {
		return
	}
	d.busy = true
	shell, dir := d.shell, d.fileDir
	go func() {
		files, err := listRemote(shell, dir)
		d.win.Update(func() {
			d.busy = false
			if shell != d.shell {
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
	if d.busy || d.shell == nil || d.fileLocal == "" {
		return
	}
	shell, dir, local, selected := d.shell, d.fileDir, d.fileLocal, d.selectedFile
	if !upload && selected == "" {
		return
	}
	d.busy = true
	d.fileStatus = "正在传输…"
	go func() {
		var err error
		if upload {
			err = uploadFile(shell, local, dir)
		} else {
			err = downloadFile(shell, path.Join(dir, selected), local)
		}
		d.win.Update(func() {
			d.busy = false
			if shell != d.shell {
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
	if d.busy || d.shell == nil || d.selectedFile == "" {
		return
	}
	if d.confirmDelete != d.selectedFile {
		d.confirmDelete = d.selectedFile
		d.fileStatus = "再次点击确认删除：" + d.selectedFile
		return
	}
	shell, dir, name := d.shell, d.fileDir, d.selectedFile
	d.busy = true
	d.confirmDelete = ""
	go func() {
		err := deleteRemote(shell, path.Join(dir, name))
		d.win.Update(func() {
			d.busy = false
			if shell != d.shell {
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
func (d *desktop) fileView(c *ui.Context) {
	ui.Text(c, "远程文件").FontSize(16).Bold()
	ui.Text(c, "选择目录浏览，拖入单个文件上传；删除前需要再次确认。").TextColor(c.Theme().TextMuted).Wrap()
	ui.TextInput(c.Key("remote-dir"), &d.fileDir).Label("远程目录").Placeholder("远程目录").OnChange(func() { d.selectedFile = ""; d.confirmDelete = ""; d.files = nil })
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c.Key("file-parent"), "上一级").Disabled(d.busy || d.shell == nil).Clicked() {
			d.fileDir = path.Join(d.fileDir, "..")
			d.selectedFile, d.confirmDelete, d.fileStatus = "", "", ""
			d.files = nil
			d.refreshFiles()
		}
		if ui.Button(c.Key("file-refresh"), "打开远程目录").Disabled(d.busy || d.shell == nil).Clicked() {
			d.selectedFile = ""
			d.confirmDelete = ""
			d.refreshFiles()
		}
	})
	if d.shell == nil {
		ui.Text(c, "连接 SSH 后可浏览远程文件。").TextColor(c.Theme().TextMuted).Wrap()
	}
	ui.TextInput(c.Key("file-filter"), &d.fileFilter).Label("筛选文件").Placeholder("按名称筛选当前目录")
	if len(d.files) > 0 {
		ui.Row(c).FillWidth().Gap(12).Padding(8, 12).Background(c.Theme().Surface).Children(func() {
			ui.Text(c, "名称").Grow(1).TextColor(c.Theme().TextMuted)
			ui.Text(c, "类型").Width(60).TextColor(c.Theme().TextMuted)
			ui.Text(c, "大小").Width(90).TextColor(c.Theme().TextMuted)
			ui.Text(c, "修改时间").Width(150).TextColor(c.Theme().TextMuted)
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
					d.fileDir = path.Join(d.fileDir, f.Name)
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
			ui.Text(c, kind).Width(60).TextColor(c.Theme().TextMuted)
			ui.Text(c, fileSizeLabel(f)).Width(90).TextColor(c.Theme().TextMuted)
			ui.Text(c, fileModifiedLabel(f)).Width(150).TextColor(c.Theme().TextMuted)
		})
	}
	if len(d.files) > 0 && shown == 0 {
		ui.Text(c, "没有匹配的文件").TextColor(c.Theme().TextMuted)
	}
	drop := ui.Box(c.Key("file-drop")).Height(48).Border(1, c.Theme().Border).Padding(8).Label("拖入一个文件上传")
	drop.Children(func() { ui.Text(c, "拖入一个文件上传").FontSize(12) })
	if files := drop.DroppedFiles(); len(files) > 0 {
		if d.busy || d.shell == nil {
			d.fileStatus = "请先连接后再上传"
		} else if len(files) != 1 {
			d.fileStatus = "每次只能拖入一个文件"
		} else {
			d.fileLocal = files[0]
			d.transfer(true)
		}
	}
	ui.TextInput(c.Key("local-file"), &d.fileLocal).Label("本地文件路径").Placeholder("本地文件完整路径")
	if ui.Button(c.Key("upload"), "上传本地文件").Disabled(d.busy || d.shell == nil).Clicked() {
		d.transfer(true)
	}
	if ui.Button(c.Key("download"), "下载选中文件").Disabled(d.busy || d.shell == nil || d.selectedFile == "").Clicked() {
		d.transfer(false)
	}
	if ui.Button(c.Key("delete"), "删除选中文件（需确认）").Disabled(d.busy || d.shell == nil || d.selectedFile == "").Clicked() {
		d.removeSelected()
	}
	ui.Text(c, d.fileStatus).FontSize(12)
}
