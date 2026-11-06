package main

import (
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func multiTestSessions(t *testing.T, count int) ([]*workspaceSession, []net.Conn) {
	t.Helper()
	var sessions []*workspaceSession
	var peers []net.Conn
	for i := 0; i < count; i++ {
		left, right := net.Pipe()
		term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = term.Close(); _ = right.Close() })
		sessions = append(sessions, &workspaceSession{id: i + 1, label: "同名", term: term})
		peers = append(peers, right)
	}
	return sessions, peers
}

func TestQuickMultiSendsOnlyExplicitTargets(t *testing.T) {
	sessions, peers := multiTestSessions(t, 3)
	item, err := newQuickCommand("检查", "pwd\nwhoami\n")
	if err != nil {
		t.Fatal(err)
	}
	d := &desktop{workspace: "snippets", currentSession: 1, term: sessions[0].term, sessions: sessions, quickCommands: []quickCommand{item}, quickSelected: item.ID}
	view := ui.NewTester(d.view, 1200, 950)
	if err := view.Click("多终端运行"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"选择目标：同名 · 会话 1", "选择目标：同名 · 会话 3"} {
		if err := view.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if err := view.Click("向所选终端发送并回车：检查"); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 2} {
		_ = peers[i].SetReadDeadline(time.Now().Add(3 * time.Second))
		data := make([]byte, len("pwd\rwhoami\r"))
		if _, err := io.ReadFull(peers[i], data); err != nil || string(data) != "pwd\rwhoami\r" {
			t.Fatalf("target %d: %q %v", i+1, data, err)
		}
	}
	_ = peers[1].SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := peers[1].Read(make([]byte, 1)); err == nil {
		t.Fatal("sent to unselected terminal")
	}
	if d.quickMulti != nil || d.workspace != "terminal" || !strings.Contains(d.status, "2 个终端提交") {
		t.Fatal("result or return to terminal missing")
	}
}

func TestQuickMultiRejectsChangedSnippetAndStaleTargetsBeforeSending(t *testing.T) {
	sessions, peers := multiTestSessions(t, 2)
	item, err := newQuickCommand("检查", "pwd")
	if err != nil {
		t.Fatal(err)
	}
	d := &desktop{sessions: sessions, quickCommands: []quickCommand{item}}
	d.beginQuickMulti(item)
	d.quickMulti.selected[1], d.quickMulti.selected[2] = sessions[0].term, sessions[1].term
	d.quickCommands[0].Command = "whoami"
	d.executeQuickMulti()
	if !strings.Contains(d.quickStatus, "已修改") {
		t.Fatal("accepted changed snippet")
	}
	d.quickCommands[0] = item
	d.sessions[1].term = sessions[0].term
	d.executeQuickMulti()
	if !strings.Contains(d.quickStatus, "已断开或改变") {
		t.Fatal("accepted a replacement terminal with the same session ID")
	}
	d.sessions[1].term = d.quickMulti.selected[2]
	d.sessions = d.sessions[:1]
	d.executeQuickMulti()
	if !strings.Contains(d.quickStatus, "已关闭") {
		t.Fatal("accepted stale session")
	}
	_ = peers[0].SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := peers[0].Read(make([]byte, 1)); err == nil {
		t.Fatal("sent to a valid target before all targets were checked")
	}
}

func TestQuickMultiRejectsChangesFromAnotherWindow(t *testing.T) {
	sessions, _ := multiTestSessions(t, 2)
	item, err := newQuickCommand("检查", "pwd")
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "quick-commands.json")
	if _, err = updateQuickCommands(filename, func(items []quickCommand) ([]quickCommand, error) { return append(items, item), nil }); err != nil {
		t.Fatal(err)
	}
	d := &desktop{sessions: sessions, quickCommands: []quickCommand{item}, quickPath: filename}
	d.beginQuickMulti(item)
	d.quickMulti.selected[1], d.quickMulti.selected[2] = sessions[0].term, sessions[1].term
	if _, err = updateQuickCommands(filename, func(items []quickCommand) ([]quickCommand, error) { items[0].Command = "whoami"; return items, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = d.quickMultiTargets(); err == nil || !strings.Contains(err.Error(), "已修改") {
		t.Fatal("accepted a snippet changed in another window")
	}
}
