package main

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestFileSessionPickerKeepsWorkspaceAndDirectoryState(t *testing.T) {
	leftA, rightA := net.Pipe()
	defer rightA.Close()
	a, err := terminal.New(terminal.Options{Conn: leftA, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	leftB, rightB := net.Pipe()
	defer rightB.Close()
	b, err := terminal.New(terminal.Options{Conn: leftB, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	localDir := t.TempDir()
	d := &desktop{workspace: "files", currentSession: 1, term: a, fileDir: "/one", files: []remoteFile{{Name: "first"}}, localPane: localFilePane{initialized: true, directory: localDir, input: localDir}, sessions: []*workspaceSession{{id: 1, label: "one", term: a}, {id: 2, label: "two", term: b, fileDir: "/two", files: []remoteFile{{Name: "second"}}}}}
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("选择文件会话"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("搜索文件会话或主机"); err != nil {
		t.Fatal(err)
	}
	view.Type("two")
	if view.HasText("文件会话：one · 会话 1") || !view.HasText("文件会话：two · 会话 2") {
		t.Fatal("session search did not filter the picker")
	}
	if err := view.Click("文件会话：two · 会话 2"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "files" || d.currentSession != 2 || d.fileDir != "/two" || d.localPane.directory != localDir || !view.HasText("second") {
		t.Fatal("file picker did not preserve workspace and independent pane state")
	}
	if err := view.Click("选择文件会话"); err != nil {
		t.Fatal(err)
	}
	d.fileSessionSearch = ""
	view.Frame()
	if err := view.Click("文件会话：one · 会话 1"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "files" || d.fileDir != "/one" || !view.HasText("first") {
		t.Fatal("returning to a session lost its directory")
	}
}

func TestFileHostPickerPreservesDraftAndDoesNotTrustCloudKey(t *testing.T) {
	host := profile{ID: "one", Label: "one", Settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	d := &desktop{workspace: "files", profiles: []profile{host}, cloud: &cloudClient{}, accounts: []cloudAccount{{ID: "cloud", Label: "cloud", Host: "cloud.test", Port: 22, Username: "bob", HostKeyFingerprint: "SHA256:untrusted"}}, localPane: localFilePane{initialized: true, directory: t.TempDir()}}
	d.loadProfile(host)
	d.settings.Host = "draft.test"
	d.workspace = "files"
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("选择文件会话"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("文件主机：cloud · 云端"); err != nil {
		t.Fatal(err)
	}
	if d.settings.Host != "draft.test" || d.workspace != "files" {
		t.Fatal("file picker discarded an unsaved host draft")
	}
	d.settings.Host = host.Settings.Host
	view.Frame()
	if err := view.Click("文件主机：cloud · 云端"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "connections" || !d.fileReturnAfterConnect || d.fingerprint != "" || d.trustedProfileFingerprint() != "" || d.expectedFingerprint != "SHA256:untrusted" {
		t.Fatal("cloud selection should return through host verification")
	}
	if err := view.Click("返回连接列表"); err != nil {
		t.Fatal(err)
	}
	if d.fileReturnAfterConnect {
		t.Fatal("closing the connection editor retained the return intent")
	}
}

func TestFileEmptyHostNavigationRetainsDraft(t *testing.T) {
	d := &desktop{workspace: "files", narrowFilePane: "session", localPane: localFilePane{initialized: true, directory: filepath.Clean(t.TempDir())}}
	d.editConnection = true
	d.settings = connectionSettings{Host: "draft.test", Port: "22", User: "alice"}
	d.captureConnectionDraft()
	d.settings.Host = "changed.test"
	view := ui.NewTester(d.view, 640, 480)
	if err := view.Click("选择 SSH 连接"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "connections" || !d.editConnection || d.settings.Host != "changed.test" || !d.fileReturnAfterConnect {
		t.Fatal("file navigation discarded the unfinished connection")
	}
	d.workspace = "files"
	view.Frame()
	if d.fileReturnAfterConnect {
		t.Fatal("leaving the host workflow should cancel the return intent")
	}
}

func TestFileSessionPickerFitsNarrowWindowAndDismisses(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "files", narrowFilePane: "session", term: term, currentSession: 1, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}}
	view := ui.NewTester(d.view, 640, 480)
	if err := view.Click("选择文件会话"); err != nil {
		t.Fatal(err)
	}
	r, ok := view.Find("搜索文件会话或主机")
	if !ok || r.X < 0 || r.X+r.W > 640 || r.Y < 0 || r.Y+r.H > 480 {
		t.Fatalf("picker is not accessible at minimum size: %+v", r)
	}
	view.Key(0, ui.KeyEscape)
	if d.showFileSessionPicker {
		t.Fatal("Escape did not close the session picker")
	}
}
