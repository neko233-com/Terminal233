package main

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPickedPathsOnlyUpdateTheirTarget(t *testing.T) {
	d := &desktop{fileLocal: "previous", adminBackupPath: "old-backup", adminConfirmRestore: true}
	d.applyPickedPath("upload", "", nil)
	if d.fileLocal != "previous" || !d.adminConfirmRestore {
		t.Fatal("cancel changed existing state")
	}
	d.applyPickedPath("upload", "source", nil)
	if d.fileLocal != "source" || d.adminBackupPath != "old-backup" {
		t.Fatal("source picker changed the wrong path")
	}
	d.applyPickedPath("key", "private-key", nil)
	if d.settings.KeyPath != "private-key" || d.fileLocal != "source" {
		t.Fatal("key picker changed the wrong path")
	}
	d.applyPickedPath("backup-open", "backup", nil)
	if d.adminBackupPath != "backup" || d.adminConfirmRestore {
		t.Fatal("changing the backup did not reset restore confirmation")
	}
	d.applyPickedPath("download", "", errors.New("dialog failed"))
	if d.fileLocal != "source" || d.fileStatus == "" {
		t.Fatal("dialog error changed the selected path or was hidden")
	}
}

func TestConnectionFormExposesPrivateKeyPicker(t *testing.T) {
	d := &desktop{workspace: "connections", editConnection: true}
	view := ui.NewTester(d.view, 1200, 1600)
	if !view.HasText("选择私钥文件…") {
		t.Fatal("private key picker is missing")
	}
}
