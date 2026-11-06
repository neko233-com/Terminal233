package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestUnifiedChromeKeepsTabsAcrossWorkspacesAndWindowSizes(t *testing.T) {
	for _, size := range [][2]int{{640, 480}, {1000, 700}, {1400, 900}} {
		for _, workspace := range []string{"connections", "files", "cloud", "identities", "forward", "terminal"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], workspace), func(t *testing.T) {
				term := groupTestTerminals(t, 1)[0]
				d := &desktop{workspace: workspace, currentSession: 1, term: term, sessions: []*workspaceSession{{id: 1, label: "Production", term: term}}}
				view := ui.NewTester(d.view, size[0], size[1])
				for _, label := range []string{"主机", "SFTP 标签页", "会话标签：Production", "设置", "新建标签页"} {
					r, ok := view.Find(label)
					if !ok || r.X < 0 || r.X+r.W > float32(size[0]) || r.Y < 0 || r.Y+r.H > 52 {
						t.Fatalf("global tab %q is missing or outside the header: %+v", label, r)
					}
				}
				if err := view.Click("会话标签：Production"); err != nil || d.workspace != "terminal" || !view.Focused("Terminal") {
					t.Fatalf("session tab did not restore terminal focus: %v", err)
				}
				if err := view.Click("SFTP 标签页"); err != nil || d.workspace != "files" || d.currentSession != 1 {
					t.Fatalf("file tab lost the active session: %v", err)
				}
				if err := view.Click("主机"); err != nil || d.workspace != "connections" {
					t.Fatalf("hosts tab did not return to the library: %v", err)
				}
			})
		}
	}
}

func TestUnifiedTabsPreserveHostDraftAndBackgroundSessionState(t *testing.T) {
	terms := groupTestTerminals(t, 2)
	d := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Host: "draft.test", Port: "22", User: "alice"}, currentSession: 1, term: terms[0], fileDir: "/keep", selectedFile: "keep.txt", sessions: []*workspaceSession{{id: 1, label: "Active", term: terms[0]}, {id: 2, label: "Background", term: terms[1]}}}
	d.captureConnectionDraft()
	d.settings.User = "unfinished"
	view := ui.NewTester(d.view, 1200, 760)
	for _, label := range []string{"SFTP 标签页", "会话标签：Active", "主机"} {
		if err := view.Click(label); err != nil {
			t.Fatal(err)
		}
		if !d.hasUnsavedConnection() || d.settings.User != "unfinished" {
			t.Fatal("workspace navigation discarded the host draft")
		}
	}
	if err := view.Click("关闭会话：Background"); err != nil {
		t.Fatal(err)
	}
	if len(d.sessions) != 1 || d.currentSession != 1 || d.fileDir != "/keep" || d.selectedFile != "keep.txt" || !d.hasUnsavedConnection() {
		t.Fatal("closing a background tab changed active state or discarded the draft")
	}
}

func TestUnifiedTabsRevealKeyboardSelectedSessionWithoutLockingScroll(t *testing.T) {
	terms := groupTestTerminals(t, 8)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0]}
	for i, term := range terms {
		d.sessions = append(d.sessions, &workspaceSession{id: i + 1, label: fmt.Sprintf("Server %d", i+1), term: term})
	}
	view := ui.NewTester(d.view, 640, 480)
	view.Key(ui.Cmd|ui.Shift, ui.KeyTab)
	view.Frame()
	selected, ok := view.Find("会话标签：Server 8")
	strip, stripOK := view.Find("会话标签栏")
	if d.currentSession != 8 || !ok || !stripOK || selected.X < strip.X || selected.X+selected.W > strip.X+strip.W {
		t.Fatalf("keyboard-selected tab was not revealed: selected=%+v strip=%+v", selected, strip)
	}
	view.Scroll(strip.X+strip.W/2, strip.Y+strip.H/2, -2000, 0)
	view.Frame()
	first, ok := view.Find("会话标签：Server 1")
	if !ok || first.X < strip.X || first.X+first.W > strip.X+strip.W {
		t.Fatalf("automatic reveal prevented manual tab scrolling: %+v", first)
	}
}

func TestUnifiedTabsCloseShortcutRespectsBusyStateAndHostDraft(t *testing.T) {
	term := groupTestTerminals(t, 1)[0]
	d := &desktop{workspace: "terminal", currentSession: 1, term: term, sessions: []*workspaceSession{{id: 1, label: "Production", term: term}}}
	view := ui.NewTester(d.view, 900, 600)
	d.busy = true
	view.Key(ui.Cmd, ui.KeyW)
	if len(d.sessions) != 1 {
		t.Fatal("close shortcut interrupted an operation")
	}
	d.busy = false
	d.openNewTab()
	view.Frame()
	view.Key(ui.Cmd, ui.KeyW)
	if d.newTabOpen || d.workspace != "terminal" || len(d.sessions) != 1 || !view.Focused("Terminal") {
		t.Fatal("closing the launcher did not restore its terminal")
	}
	d.workspace, d.editConnection = "connections", true
	d.captureConnectionDraft()
	d.settings.Host = "unsaved.test"
	view.Frame()
	view.Key(ui.Cmd, ui.KeyW)
	if !d.hasUnsavedConnection() || len(d.sessions) != 1 {
		t.Fatal("close shortcut changed the host inspector or background session")
	}
	d.workspace = "terminal"
	view.Frame()
	view.Key(ui.Cmd, ui.KeyW)
	if len(d.sessions) != 0 || d.term != nil || !d.hasUnsavedConnection() {
		t.Fatal("closing a terminal changed the saved host draft")
	}
}

func TestUnifiedGroupTabKeepsCloseConfirmationReachable(t *testing.T) {
	terms := groupTestTerminals(t, 2)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "API", term: terms[0]}, {id: 2, label: "Database", term: terms[1]}}}
	name := "Production infrastructure workspace"
	if err := d.createTerminalGroup(name, []int{1, 2}, 1, true); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 640, 480)
	if err := view.Click("关闭工作区：" + name); err != nil {
		t.Fatal(err)
	}
	view.Frame()
	strip, _ := view.Find("会话标签栏")
	for _, label := range []string{"确认关闭工作区：" + name, "取消关闭工作区：" + name} {
		r, ok := view.Find(label)
		if !ok || r.X < strip.X || r.X+r.W > strip.X+strip.W || r.Y+r.H > 52 {
			t.Fatalf("group close action is outside the strip: %s %+v strip=%+v", label, r, strip)
		}
	}
	if err := view.Click("取消关闭工作区：" + name); err != nil || len(d.sessions) != 2 || d.confirmTerminalGroupClose != 0 {
		t.Fatalf("group close cancellation changed sessions: %v", err)
	}
}
