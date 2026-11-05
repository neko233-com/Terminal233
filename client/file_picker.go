package main

import (
	"path/filepath"

	"github.com/egoist/mygo"
)

// pickLocalPath opens a native picker without starting a transfer or restore.
// Operations still create files exclusively and never overwrite a destination.
func (d *desktop) pickLocalPath(kind string) {
	if d.busy || d.win == nil {
		return
	}
	var title, defaultPath string
	save := false
	switch kind {
	case "upload":
		title, defaultPath = "选择要上传或复制的文件", d.fileLocal
	case "download":
		title, defaultPath, save = "选择下载或复制目标（不覆盖已有文件）", d.fileLocal, true
		if defaultPath == "" && d.selectedFile != "" {
			defaultPath = filepath.Base(d.selectedFile)
		}
	case "key":
		title, defaultPath = "选择 SSH 私钥", d.settings.KeyPath
	case "backup-open":
		title, defaultPath = "选择加密备份", d.adminBackupPath
	case "backup-save":
		title, defaultPath, save = "选择备份目标（不覆盖已有文件）", d.adminBackupPath, true
	default:
		return
	}
	d.busy = true
	win := d.win
	go func() {
		var selected string
		var err error
		if save {
			selected, err = mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: title, DefaultPath: defaultPath})
		} else {
			var paths []string
			paths, err = mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: title, DefaultPath: defaultPath})
			if len(paths) == 1 {
				selected = paths[0]
			}
		}
		win.Update(func() {
			d.busy = false
			d.applyPickedPath(kind, selected, err)
		})
	}()
}

func (d *desktop) applyPickedPath(kind, selected string, err error) {
	if err != nil {
		message := "选择文件失败：" + err.Error()
		switch kind {
		case "upload", "download":
			d.fileStatus = message
		case "backup-open", "backup-save":
			d.adminStatus = message
		default:
			d.status = message
		}
		return
	}
	if selected == "" { // Cancel keeps the existing path and confirmation state.
		return
	}
	switch kind {
	case "upload", "download":
		d.fileLocal = selected
	case "key":
		d.settings.KeyPath = selected
	case "backup-open", "backup-save":
		d.adminBackupPath, d.adminConfirmRestore = selected, false
	}
}
