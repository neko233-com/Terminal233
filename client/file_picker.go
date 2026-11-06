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

type downloadPickerOrigin struct {
	batchPickerOrigin
	name string
}

// The context action promises a transfer after the user chooses a new path.
func (d *desktop) pickDownloadAndTransfer(name string) {
	if d.busy || d.win == nil || d.fileSessionUnavailable() {
		return
	}
	origin := downloadPickerOrigin{batchPickerOrigin{d.currentSession, d.shell, d.fileDir}, name}
	d.busy = true
	win := d.win
	go func() {
		selected, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: "下载或复制到新文件（不覆盖已有文件）", DefaultPath: filepath.Base(name)})
		win.Update(func() {
			d.busy = false
			if d.applyDownloadPicker(origin, selected, err) {
				d.transfer(false)
			}
		})
	}()
}

func (d *desktop) applyDownloadPicker(origin downloadPickerOrigin, selected string, err error) bool {
	if err != nil {
		d.fileStatus = "选择目标失败：" + err.Error()
		return false
	}
	if selected == "" {
		return false
	}
	if d.busy || d.fileSessionUnavailable() || d.currentSession != origin.sessionID || d.shell != origin.shell || d.fileDir != origin.directory {
		d.fileStatus = "文件会话或目录已改变，请重新选择文件。"
		return false
	}
	found := false
	for _, f := range d.files {
		if f.Name == origin.name && eligibleBatchFile(f) {
			found = true
			break
		}
	}
	if !found {
		d.fileStatus = "文件列表已改变，请重新选择文件。"
		return false
	}
	d.selectedFile, d.fileRename, d.confirmDelete, d.fileLocal = origin.name, origin.name, "", selected
	return true
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
	directory := false
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
	case "identity-key":
		title, defaultPath = "选择身份私钥", d.identityDraft.KeyPath
	case "backup-open":
		title, defaultPath = "选择加密备份", d.adminBackupPath
	case "backup-save":
		title, defaultPath, save = "选择备份目标（不覆盖已有文件）", d.adminBackupPath, true
	case "archive-open":
		title, defaultPath = "选择加密连接备份", d.archivePath
	case "archive-save":
		title, defaultPath, save = "选择连接备份目标（不覆盖已有文件）", d.archivePath, true
	case "local-archive-open":
		title, defaultPath = "选择本机加密备份", d.localArchivePath
	case "local-archive-save":
		title, defaultPath, save = "选择本机备份目标（不覆盖已有文件）", d.localArchivePath, true
	case "batch-target":
		title, defaultPath, directory = "选择批量下载或复制的目标目录", d.fileBatchTarget, true
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
			paths, err = mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: title, DefaultPath: defaultPath, Directory: directory})
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
		case "upload", "download", "batch-target":
			d.fileStatus = message
		case "backup-open", "backup-save":
			d.adminStatus = message
		case "archive-open", "archive-save":
			d.archiveStatus = message
		case "local-archive-open", "local-archive-save":
			d.localArchiveStatus = message
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
	case "batch-target":
		d.fileBatchTarget = selected
	case "key":
		if d.settings.KeyPath != selected {
			d.profileAutoDirty = true
		}
		d.settings.KeyPath = selected
		d.confirmDiscardConnection = false
	case "identity-key":
		if d.identityEditing != "" {
			d.identityDraft.KeyPath = selected
		}
	case "backup-open", "backup-save":
		d.adminBackupPath, d.adminConfirmRestore = selected, false
	case "archive-open", "archive-save":
		d.archivePath, d.archiveConfirm = selected, false
	case "local-archive-open", "local-archive-save":
		d.localArchivePath, d.localArchivePending = selected, nil
	}
}
