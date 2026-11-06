package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestDualPaneUsesIndependentLocalAndSessionDirectories(t *testing.T) {
	dir := t.TempDir()
	shell := &remoteShell{}
	d := &desktop{
		workspace: "files", shell: shell, term: &terminal.Terminal{}, currentSession: 1, fileDir: "/remote",
		files:     []remoteFile{{Name: "remote.txt", Size: 10}},
		localPane: localFilePane{initialized: true, directory: dir, input: dir, files: []remoteFile{{Name: "local.txt", Size: 20}, {Name: "folder", IsDir: true}}},
	}
	view := ui.NewTester(d.view, 1280, 900)
	local, localOK := view.Find("本机文件面板")
	remote, remoteOK := view.Find("会话文件面板")
	if !localOK || !remoteOK || local.X+local.W > remote.X || !view.HasText("local.txt") || !view.HasText("remote.txt") {
		t.Fatalf("panes or contents missing: local=%+v remote=%+v", local, remote)
	}
	if err := view.Click("local.txt"); err != nil {
		t.Fatal(err)
	}
	if d.localPane.selected != "local.txt" || d.fileDir != "/remote" || d.selectedFile != "" {
		t.Fatal("local selection changed the remote pane")
	}
	if err := view.Click("📁 folder"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("打开所选目录") {
		t.Fatal("a selected directory needs a keyboard-accessible open action")
	}
}

func TestNarrowFileWorkspaceSwitchesBetweenLocalAndSession(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{workspace: "files", localPane: localFilePane{initialized: true, directory: dir, input: dir, files: []remoteFile{{Name: "local.txt"}}}}
	view := ui.NewTester(d.view, 640, 480)
	if !view.HasText("本机目录") || !view.HasText("local.txt") || view.HasText("选择 SSH 连接") {
		t.Fatal("narrow workspace should open on local files")
	}
	if err := view.Click("会话文件"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("选择 SSH 连接") || view.HasText("local.txt") {
		t.Fatal("session pane did not replace local pane")
	}
	if err := view.Click("本机文件"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("local.txt") || d.localPane.directory != dir {
		t.Fatal("switching panes lost local state")
	}
}

func TestPaneTransferRejectsStaleSelectionsAndUnsafeNames(t *testing.T) {
	dir := t.TempDir()
	shell := &remoteShell{}
	d := &desktop{
		win: &mygo.Window{}, shell: shell, term: &terminal.Terminal{}, currentSession: 7, fileDir: "/remote",
		files:     []remoteFile{{Name: "remote.txt"}, {Name: "folder", IsDir: true}, {Name: "link", IsLink: true}},
		localPane: localFilePane{directory: dir, input: dir, generation: 3, files: []remoteFile{{Name: "local.txt"}, {Name: "folder", IsDir: true}}},
	}
	upload := localPaneDrag{directory: dir, name: "local.txt", generation: 3, sessionID: 7, shell: shell, remoteDir: "/remote"}
	if got, err := d.paneUploadSource(upload); err != nil || got != filepath.Join(dir, "local.txt") {
		t.Fatalf("valid upload: %q %v", got, err)
	}
	upload.generation--
	if _, err := d.paneUploadSource(upload); err == nil {
		t.Fatal("accepted a stale local selection")
	}
	upload.generation++
	upload.name = "folder"
	if _, err := d.paneUploadSource(upload); err == nil {
		t.Fatal("accepted a directory upload")
	}
	download := remotePaneDrag{directory: "/remote", name: "remote.txt", sessionID: 7, shell: shell}
	if got, err := d.paneDownloadTarget(download); err != nil || got != filepath.Join(dir, "remote.txt") {
		t.Fatalf("valid download: %q %v", got, err)
	}
	for _, name := range []string{"../bad", "folder", "link", "C:bad"} {
		download.name = name
		if _, err := d.paneDownloadTarget(download); err == nil {
			t.Fatalf("accepted unsafe or non-file name %q", name)
		}
	}
	download.name = "remote.txt"
	d.localPane.input = filepath.Join(dir, "unopened")
	if _, err := d.paneDownloadTarget(download); err == nil {
		t.Fatal("accepted a changed but unopened target path")
	}
	d.localPane.input = dir
	download.sessionID++
	if _, err := d.paneDownloadTarget(download); err == nil {
		t.Fatal("accepted a file from another session")
	}
}

func TestLocalPaneIgnoresLateReadsAndPreservesLastGoodDirectory(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{localPane: localFilePane{directory: dir, input: dir, generation: 2, loading: true, files: []remoteFile{{Name: "previous"}}}}
	d.finishLocalPaneLoad(1, "stale", -1, []remoteFile{{Name: "stale"}}, nil)
	if d.localPane.directory != dir || !d.localPane.loading || d.localPane.files[0].Name != "previous" {
		t.Fatal("late result replaced the current directory")
	}
	d.localPane.input = filepath.Join(dir, "missing")
	d.finishLocalPaneLoad(2, d.localPane.input, -1, nil, errors.New("not found"))
	if d.localPane.directory != dir || d.localPane.input != dir || d.localPane.loading || d.localPane.files[0].Name != "previous" {
		t.Fatal("failed navigation lost the last good directory")
	}
}
