package main

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestSSHReconnectRechecksHostKeyBeforeAuthentication(t *testing.T) {
	settings, fingerprint, attempts := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, term, err := openSSHSession(settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	_ = term.Close()
	_ = shell.Close()
	if attempts.Load() != 1 {
		t.Fatal("initial authentication missing")
	}
	settings.Fingerprint = "SHA256:incorrect"
	_, _, err = openSSHSession(settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1})
	if err == nil || !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("changed host key accepted: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatal("credentials sent before fingerprint validation")
	}
	settings.Fingerprint = fingerprint
	shell, term, err = openSSHSession(settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	defer shell.Close()
	if attempts.Load() != 2 {
		t.Fatal("verified reconnect did not authenticate")
	}
}

func TestEndedSessionIsVisibleAndFileOperationsAreUnavailable(t *testing.T) {
	left, right := net.Pipe()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	_ = right.Close()
	select {
	case <-term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not exit")
	}
	d := &desktop{workspace: "terminal", status: "已连接", currentSession: 1, term: term, shell: &remoteShell{}, sessions: []*workspaceSession{{id: 1, label: "test host", term: term, shell: &remoteShell{}}}}
	view := ui.NewTester(d.view, 1200, 1200)
	if !view.HasText("test host · 已断开") || !view.HasText("重新连接") || view.HasText("已连接") || !d.fileSessionUnavailable() {
		t.Fatal("disconnected state not shown or file operations remain enabled")
	}
	d.workspace = "files"
	view.Frame()
	if !view.HasText("当前会话已结束，文件操作不可用；请重新连接或打开终端。") {
		t.Fatal("file workspace did not identify ended session")
	}
}
