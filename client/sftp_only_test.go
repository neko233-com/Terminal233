package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestSFTPOnlyConnectsWithoutPTYAndVerifiesHost(t *testing.T) {
	var interactive atomic.Int32
	settings, fingerprint, attempts := testSSHServerWithExecPolicy(t, nil, nil, nil, nil, func(kind string) bool {
		if kind == "pty-req" || kind == "shell" {
			interactive.Add(1)
			return false
		}
		return true
	}, nil)
	settings.Fingerprint = "SHA256:wrong"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := openSFTPTransport(ctx, settings); err == nil || attempts.Load() != 0 {
		t.Fatal("untrusted host received authentication")
	}
	settings.Fingerprint = fingerprint
	shell, err := openSFTPTransport(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	if interactive.Load() != 0 {
		t.Fatal("SFTP connection requested an interactive shell")
	}
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "transfer.txt")
	if err := os.WriteFile(source, []byte("sftp-only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := uploadFile(shell, source, filepath.ToSlash(dir)); err != nil {
		t.Fatal(err)
	}
	files, err := listRemote(shell, filepath.ToSlash(dir))
	if err != nil || len(files) != 1 || files[0].Name != "transfer.txt" {
		t.Fatalf("SFTP operation failed: %v, %+v", err, files)
	}
	if interactive.Load() != 0 {
		t.Fatal("file operation requested an interactive shell")
	}
	if err := shell.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shell.done:
	case <-time.After(3 * time.Second):
		t.Fatal("transport did not close")
	}
}

func TestSFTPOnlySubsystemFailureAndCancellation(t *testing.T) {
	settings, fingerprint, _ := testSSHServerWithExecPolicy(t, nil, nil, nil, nil, func(kind string) bool { return kind != "subsystem" }, nil)
	settings.Fingerprint = fingerprint
	if _, err := openSFTPTransport(context.Background(), settings); err == nil {
		t.Fatal("server without SFTP was accepted")
	}
	release := make(chan struct{})
	defer close(release)
	settings, fingerprint, _ = testSSHServerWithExecPolicy(t, nil, nil, nil, nil, func(kind string) bool {
		if kind == "subsystem" {
			<-release
		}
		return true
	}, nil)
	settings.Fingerprint = fingerprint
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := openSFTPTransport(ctx, settings); err == nil {
		t.Fatal("stalled SFTP connection ignored cancellation")
	}
}

func TestSFTPOnlySessionSelectionAndDisconnect(t *testing.T) {
	done := make(chan struct{})
	shell := &remoteShell{done: done}
	s := &workspaceSession{id: 1, label: "files.example", fileOnly: true, shell: shell, fileDir: "/home"}
	d := &desktop{sessions: []*workspaceSession{s}, workspace: "connections"}
	d.selectSession(1)
	if d.workspace != "files" || d.fileSessionUnavailable() || d.term != nil || d.pendingTerminalFocus != 0 {
		t.Fatal("SFTP-only session selected as a terminal")
	}
	v := ui.NewTester(d.view, 1280, 800)
	if !v.HasText("SFTP · files.example") || !v.HasText("当前目录") {
		t.Fatal("SFTP session is not usable in the file workspace")
	}
	close(done)
	v.Frame()
	if !d.fileSessionUnavailable() || !strings.Contains(sessionDisplayLabel(s), "已断开") {
		t.Fatal("disconnected SFTP transport remained usable")
	}
	if !v.HasText("file-reconnect") && !v.HasText("重新连接") {
		t.Fatal("disconnected SFTP session has no reconnect action")
	}
	d.closeSession(1)
	if len(d.sessions) != 0 || d.workspace != "files" {
		t.Fatal("SFTP session was not closed")
	}
}

func TestSFTPOnlyTabsSwitchAndCloseInFileWorkspace(t *testing.T) {
	a, b := &remoteShell{done: make(chan struct{})}, &remoteShell{done: make(chan struct{})}
	d := &desktop{workspace: "files", currentSession: 1, shell: a, fileDir: "/one", sessions: []*workspaceSession{{id: 1, label: "one", shell: a, fileOnly: true}, {id: 2, label: "two", shell: b, fileOnly: true, fileDir: "/two"}}}
	v := ui.NewTester(d.view, 1280, 800)
	v.Key(ui.Cmd, ui.KeyTab)
	if d.currentSession != 2 || d.workspace != "files" || d.fileDir != "/two" {
		t.Fatal("tab switch did not select the SFTP workspace")
	}
	d.closeSession(2)
	if d.currentSession != 1 || d.workspace != "files" || d.fileDir != "/one" {
		t.Fatal("closing SFTP changed workspace or lost its directory")
	}
}

func TestWorkspaceSnapshotExcludesSFTPOnlySessions(t *testing.T) {
	first, second := &terminal.Terminal{}, &terminal.Terminal{}
	shell := &remoteShell{done: make(chan struct{})}
	d := &desktop{currentSession: 4, term: second, splitFirstSession: 2, splitSession: 4, sessions: []*workspaceSession{{id: 1, label: "file", shell: shell, fileOnly: true}, {id: 2, label: "local one", term: first}, {id: 3, label: "file two", shell: shell, fileOnly: true}, {id: 4, label: "local two", term: second}}}
	w, err := d.workspaceSnapshot("work")
	if err != nil || len(w.Entries) != 2 || w.Active != 1 || w.SplitFirst != 0 || w.SplitSecond != 1 {
		t.Fatalf("terminal workspace indexes changed: %+v %v", w, err)
	}
	d.currentSession, d.term, d.shell = 1, nil, shell
	w, err = d.workspaceSnapshot("work")
	if err != nil || w.Active != 0 || w.SplitFirst != -1 || w.SplitSecond != -1 {
		t.Fatalf("SFTP selection produced an invalid terminal workspace: %+v %v", w, err)
	}
	d.sessions = d.sessions[:1]
	if _, err := d.workspaceSnapshot("work"); err == nil || !strings.Contains(err.Error(), "SFTP") {
		t.Fatal("SFTP-only workspace did not explain the missing terminal")
	}
}

func TestDisconnectedSecondarySFTPOffersReconnect(t *testing.T) {
	done := make(chan struct{})
	close(done)
	shell := &remoteShell{done: done}
	s := &workspaceSession{id: 3, label: "left", shell: shell, fileOnly: true}
	d := &desktop{workspace: "files", sessions: []*workspaceSession{s}, secondaryPane: secondaryRemotePane{sessionID: 3, shell: shell, status: "SFTP 已断开"}}
	v := ui.NewTester(d.view, 1280, 800)
	if !v.HasText("重新连接") || !v.HasText("SFTP 已断开") {
		t.Fatal("left pane did not offer reconnect")
	}
}

func TestSFTPOnlyLeftActionsFitMinimumWindow(t *testing.T) {
	shell := &remoteShell{done: make(chan struct{})}
	s := &workspaceSession{id: 3, label: "left", shell: shell, fileOnly: true}
	d := &desktop{workspace: "files", narrowFilePane: "local", sessions: []*workspaceSession{s}, secondaryPane: secondaryRemotePane{sessionID: 3, shell: shell, directory: "/", input: "/"}}
	v := ui.NewTester(d.view, 640, 480)
	for _, label := range []string{"选择左侧文件主机", "打开左侧主机终端", "左侧文件选项", "删除左侧所选项"} {
		r, ok := v.Find(label)
		if !ok || r.X < 0 || r.X+r.W > 640 || r.Y < 0 || r.Y+r.H > 480 {
			t.Errorf("%s outside the window: %+v", label, r)
		}
	}
}
