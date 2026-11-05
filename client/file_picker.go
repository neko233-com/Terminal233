package main

import (
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo"
)

type batchPickerOrigin struct {
	sessionID int
	shell     *remoteShell
	directory string
}

// The label makes clear that confirming the picker immediately starts upload.
func (d *desktop) pickBatchUpload() {
	if d.busy || d.win == nil || d.fileSessionUnavailable() {
		return
	}
	origin := batchPickerOrigin{d.currentSession, d.shell, d.fileDir}
	d.busy = true
	win := d.win
	go func() {
		files, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "选择要上传或复制的文件（最多 100 个）", Multiple: true})
		win.Update(func() {
			d.busy = false
			d.applyPickedBatch(origin, files, err)
		})
	}()
}

func (d *desktop) applyPickedBatch(origin batchPickerOrigin, files []string, err error) {
	if err != nil {
		d.fileStatus = "选择文件失败：" + err.Error()
		return
	}
	if len(files) == 0 {
		return
	}
	if len(files) > maxBatchFiles {
		d.fileStatus = fmt.Sprintf("每次最多传输 %d 个文件", maxBatchFiles)
		return
	}
	if d.busy || d.fileSessionUnavailable() || d.currentSession != origin.sessionID || d.shell != origin.shell || d.fileDir != origin.directory {
		d.fileStatus = "文件会话或目录已改变，请重新选择文件。"
		return
	}
	d.transferBatch(files)
}

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
	case "archive-open":
		title, defaultPath = "选择加密连接备份", d.archivePath
	case "archive-save":
		title, defaultPath, save = "选择连接备份目标（不覆盖已有文件）", d.archivePath, true
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
		case "archive-open", "archive-save":
			d.archiveStatus = message
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
	case "archive-open", "archive-save":
		d.archivePath, d.archiveConfirm = selected, false
	}
}
