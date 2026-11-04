package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"path"
)

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
	ui.Text(c, "远程文件（测试中）").Bold()
	ui.TextInput(c.Key("remote-dir"), &d.fileDir).Label("远程目录").Placeholder("远程目录").OnChange(func() { d.selectedFile = ""; d.confirmDelete = ""; d.files = nil })
	if ui.Button(c.Key("file-refresh"), "打开远程目录").Disabled(d.busy || d.shell == nil).Clicked() {
		d.selectedFile = ""
		d.confirmDelete = ""
		d.refreshFiles()
	}
	for _, entry := range d.files {
		f := entry
		label := f.Name
		if f.IsDir {
			label = "📁 " + label
		}
		if ui.Button(c.Key("file-"+f.Name), label).Disabled(d.busy).Clicked() {
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
