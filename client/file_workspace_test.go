package main

import (
	"errors"
	"path/filepath"
	"strings"
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
	if err := view.RightClick("local.txt"); err != nil {
		t.Fatal(err)
	}
	menu := strings.Join(view.Menu(), "|")
	if !strings.Contains(menu, "复制到会话当前目录") || !strings.Contains(menu, "复制路径") {
		t.Fatalf("local file context actions missing: %s", menu)
	}
	view.CloseMenu()
	if err := view.RightClick("remote.txt"); err != nil {
		t.Fatal(err)
	}
	menu = strings.Join(view.Menu(), "|")
	if !strings.Contains(menu, "复制到本机当前目录") || !strings.Contains(menu, "下载到…") {
		t.Fatalf("remote file transfer actions missing: %s", menu)
	}
	view.CloseMenu()
	if err := view.Click("📁 folder"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("打开本机所选目录") {
		t.Fatal("a selected directory needs a keyboard-accessible open action")
	}
}

func TestDualPaneNavigationAndListsAlign(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{workspace: "files", shell: &remoteShell{}, term: &terminal.Terminal{}, currentSession: 1, fileDir: "/remote", files: []remoteFile{{Name: "remote.txt"}}, localPane: localFilePane{initialized: true, directory: dir, input: dir, files: []remoteFile{{Name: "local.txt"}}}}
	view := ui.NewTester(d.view, 1280, 760)
	localPath, a := view.Find("本机目录")
	remotePath, b := view.Find("当前目录")
	localList, c := view.Find("本机文件列表")
	remoteList, e := view.Find("会话文件列表")
	tools, f := view.Find("文件操作")
	if !a || !b || !c || !e || !f || localPath.Y != remotePath.Y || localList.H != remoteList.H || tools.Y+tools.H > 760 {
		t.Fatalf("dual-pane controls are not aligned or visible: local=%+v remote=%+v lists=%+v %+v tools=%+v", localPath, remotePath, localList, remoteList, tools)
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

func TestSessionFileSelectionDoesNotNavigateOrExpandTools(t *testing.T) {
	d := &desktop{workspace: "files", shell: &remoteShell{}, term: &terminal.Terminal{}, fileDir: "/home", files: []remoteFile{{Name: "folder", IsDir: true}, {Name: "log.txt"}}}
	view := ui.NewTester(d.view, 1280, 900)
	_, filterVisible := view.Find("筛选文件")
	if view.HasText("按名称 ↑") || filterVisible {
		t.Fatal("filter and sort controls should start collapsed")
	}
	if err := view.Click("📁 folder"); err != nil {
		t.Fatal(err)
	}
	if d.fileDir != "/home" || d.selectedFile != "folder" || d.showFileTools || !view.HasText("打开会话所选目录") {
		t.Fatal("single click should select the directory without navigating")
	}
	if err := view.Click("log.txt"); err != nil {
		t.Fatal(err)
	}
	if d.selectedFile != "log.txt" || d.showFileTools || view.HasText("打开会话所选目录") {
		t.Fatal("single click should select the file without expanding tools")
	}
	if err := view.Click("筛选文件列表"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("筛选文件") {
		t.Fatal("file filter could not be opened")
	}
}

func TestSessionPaneAcceptsExternalFileDrop(t *testing.T) {
	d := &desktop{workspace: "files", term: &terminal.Terminal{}, fileDir: t.TempDir(), localPane: localFilePane{initialized: true}}
	view := ui.NewTester(d.view, 1280, 900)
	pane, ok := view.Find("会话文件面板")
	if !ok {
		t.Fatal("session pane missing")
	}
	files := make([]string, maxBatchFiles+1)
	for i := range files {
		files[i] = "file"
	}
	if !view.DropFiles(pane.X+pane.W-8, pane.Y+pane.H-8, files...) {
		t.Fatal("session pane did not accept an external file drop")
	}
	if !strings.Contains(d.fileStatus, "最多传输 100") || d.busy {
		t.Fatal("pane drop did not apply the existing batch limit")
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
	d.fileHistory.visit("/remote")
	upload := d.localPaneDrag("local.txt")
	if got, err := d.paneUploadSource(upload); err != nil || got != filepath.Join(dir, "local.txt") {
		t.Fatalf("valid upload: %q %v", got, err)
	}
	upload.generation--
	if _, err := d.paneUploadSource(upload); err == nil {
		t.Fatal("accepted a stale local selection")
	}
	upload.generation++
	upload = d.localPaneDrag("folder")
	if _, err := d.paneUploadSource(upload); err != nil {
		t.Fatal("rejected a directory upload")
	}
	upload.shell = nil
	d.shell = nil
	if _, err := d.paneUploadSource(upload); err != nil {
		t.Fatal("rejected a local-to-local directory copy")
	}
	upload.shell = shell
	d.shell = shell
	download := d.rightPaneDrag("remote.txt")
	if got, err := d.paneDownloadTarget(download); err != nil || got != filepath.Join(dir, "remote.txt") {
		t.Fatalf("valid download: %q %v", got, err)
	}
	download = d.rightPaneDrag("folder")
	if _, err := d.paneDownloadTarget(download); err != nil {
		t.Fatal("rejected a directory download")
	}
	for _, name := range []string{"../bad", "link", "C:bad"} {
		download = d.rightPaneDrag(name)
		if _, err := d.paneDownloadTarget(download); err == nil {
			t.Fatalf("accepted unsafe or non-file name %q", name)
		}
	}
	download = d.rightPaneDrag("remote.txt")
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
