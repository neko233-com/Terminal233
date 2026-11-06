package main

import (
	"net"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestNewTabSearchSwitchAndRestore(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, currentSession: 1,
		sessions: []*workspaceSession{{id: 1, label: "生产终端", term: term}},
		profiles: []profile{{ID: "1", Label: "开发主机", Group: "work", Settings: connectionSettings{Host: "dev.example", User: "alice", Port: "22"}}}}
	view := ui.NewTester(d.view, 1280, 900)
	view.Key(ui.Cmd, ui.KeyT)
	if !d.newTabOpen || d.workspace != "new-tab" || !view.HasText("搜索主机或会话") || !view.HasText("打开本机终端") {
		t.Fatal("new tab did not open with search and local terminal")
	}
	if d.currentSession != 1 || d.term != term {
		t.Fatal("opening the new tab replaced the active session")
	}
	d.newTabQuery = "dev.example"
	view.Frame()
	if !view.HasText("打开主机：开发主机 · 本机") || view.HasText("切换到会话：生产终端 · 会话 1") {
		t.Fatal("new-tab search did not filter hosts and sessions")
	}
	d.newTabQuery = "生产"
	view.Frame()
	if err := view.Click("切换到会话：生产终端 · 会话 1"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "terminal" || !d.newTabOpen || d.currentSession != 1 {
		t.Fatal("selecting the existing session lost the new tab")
	}
	if err := view.Click("切换到新标签页"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "new-tab" || d.newTabQuery != "生产" {
		t.Fatal("returning to the new tab lost search state")
	}
	if err := view.Click("关闭新标签页"); err != nil {
		t.Fatal(err)
	}
	if d.newTabOpen || d.workspace != "terminal" || d.currentSession != 1 || !view.Focused("Terminal") {
		t.Fatal("closing the new tab did not restore and focus the previous session")
	}
}

func TestNewTabProtectsHostDraftAndQuickConnect(t *testing.T) {
	d := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Host: "before", Port: "22", User: "alice"},
		profiles: []profile{{ID: "1", Label: "other", Settings: connectionSettings{Host: "other.test", Port: "22", User: "bob"}}}}
	d.captureConnectionDraft()
	d.settings.Host = "unsaved"
	d.openNewTab()
	d.newTabQuery = "bob@other.test"
	d.newTabQuickConnect()
	if d.workspace != "new-tab" || d.settings.Host != "unsaved" {
		t.Fatal("quick connect discarded an unsaved draft")
	}
	d.newTabQuery = "other"
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("打开主机：other · 本机"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "new-tab" || d.settings.Host != "unsaved" {
		t.Fatal("saved host discarded an unsaved draft")
	}
	d.settings.Host = "before"
	d.newTabQuery = "ssh bob@other.test -p 2222"
	d.newTabQuickConnect()
	if d.workspace != "connections" || d.newTabOpen || d.settings.Host != "other.test" || d.settings.Port != "2222" || d.fingerprint != "" {
		t.Fatal("quick connect did not open a verification-first host form")
	}
}

func TestNewTabFitsMinimumWindow(t *testing.T) {
	d := &desktop{workspace: "connections"}
	d.openNewTab()
	view := ui.NewTester(d.view, 640, 480)
	for _, label := range []string{"搜索主机或会话", "打开本机终端"} {
		r, ok := view.Find(label)
		if !ok || r.Y < 0 || r.Y+r.H > 480 {
			t.Fatalf("%s is not visible at minimum window size: %+v", label, r)
		}
	}
}
