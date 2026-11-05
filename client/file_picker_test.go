package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
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
	d.archiveConfirm = true
	d.applyPickedPath("archive-open", "archive.nekobackup", nil)
	if d.archivePath != "archive.nekobackup" || d.archiveConfirm {
		t.Fatal("archive selection did not reset confirmation")
	}
}

func TestBatchPickerRejectsStaleDestinationAndOversizedSelection(t *testing.T) {
	d := &desktop{term: &terminal.Terminal{}, currentSession: 1, fileDir: "old", fileStatus: "unchanged"}
	origin := batchPickerOrigin{sessionID: 1, directory: "old"}
	d.applyPickedBatch(origin, nil, nil)
	if d.fileStatus != "unchanged" || d.busy {
		t.Fatal("cancel changed the transfer state")
	}
	d.fileDir = "new"
	d.applyPickedBatch(origin, []string{"source"}, nil)
	if !strings.Contains(d.fileStatus, "已改变") || d.busy {
		t.Fatal("stale picker destination was accepted")
	}
	d.fileDir = "old"
	d.applyPickedBatch(origin, make([]string, maxBatchFiles+1), nil)
	if !strings.Contains(d.fileStatus, "最多传输 100") || d.busy {
		t.Fatal("oversized selection was accepted")
	}
	d.applyPickedBatch(origin, nil, errors.New("dialog failed"))
	if !strings.Contains(d.fileStatus, "dialog failed") {
		t.Fatal("picker error was hidden")
	}
}

func TestDownloadPickerBindsFileAndSession(t *testing.T) {
	d := &desktop{workspace: "files", term: &terminal.Terminal{}, currentSession: 1, fileDir: "old", files: []remoteFile{{Name: "one"}, {Name: "folder", IsDir: true}}, selectedFile: "other", fileLocal: "previous"}
	origin := downloadPickerOrigin{batchPickerOrigin: batchPickerOrigin{sessionID: 1, directory: "old"}, name: "one"}
	if d.applyDownloadPicker(origin, "", nil) || d.fileLocal != "previous" || d.selectedFile != "other" {
		t.Fatal("cancel changed selection")
	}
	d.fileDir = "new"
	if d.applyDownloadPicker(origin, "target", nil) || d.fileLocal != "previous" {
		t.Fatal("stale directory accepted")
	}
	d.fileDir = "old"
	d.currentSession = 2
	if d.applyDownloadPicker(origin, "target", nil) {
		t.Fatal("stale session accepted")
	}
	d.currentSession = 1
	d.files = []remoteFile{{Name: "one", IsLink: true}}
	if d.applyDownloadPicker(origin, "target", nil) {
		t.Fatal("changed file type accepted")
	}
	d.files = []remoteFile{{Name: "one"}, {Name: "folder", IsDir: true}}
	if !d.applyDownloadPicker(origin, "target", nil) || d.selectedFile != "one" || d.fileLocal != "target" {
		t.Fatal("valid target not bound to file")
	}
	view := ui.NewTester(d.view, 1200, 1400)
	if err := view.RightClick("one"); err != nil {
		t.Fatal(err)
	}
	if !containsMenuItem(view.Menu(), "复制到…") {
		t.Fatal("file context download action missing")
	}
	view.CloseMenu()
	if err := view.RightClick("📁 folder"); err != nil {
		t.Fatal(err)
	}
	if containsMenuItem(view.Menu(), "复制到…") {
		t.Fatal("directory exposed as a regular-file copy")
	}
}

func containsMenuItem(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

func TestFileWorkspaceExposesBatchPicker(t *testing.T) {
	d := &desktop{workspace: "files", term: &terminal.Terminal{}}
	view := ui.NewTester(d.view, 1200, 1400)
	if !view.HasText("选择多文件并复制到此…") {
		t.Fatal("batch file picker is missing")
	}
}

func TestConnectionFormExposesPrivateKeyPicker(t *testing.T) {
	d := &desktop{workspace: "connections", editConnection: true}
	view := ui.NewTester(d.view, 1200, 1600)
	if !view.HasText("选择私钥文件…") {
		t.Fatal("private key picker is missing")
	}
}
