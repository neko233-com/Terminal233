package main

import (
	"net"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestTerminalRemainsVisibleWithPanelsAtMinimumWindow(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, currentSession: 1, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}, quickOpen: true, terminalSearchOpen: true, showFilePanel: true, quickCommands: []quickCommand{{ID: "one", Name: "test", Command: "pwd"}}}
	for _, size := range [][2]int{{640, 480}, {640, 600}, {640, 650}, {640, 800}, {640, 1000}, {900, 700}, {1200, 760}} {
		view := ui.NewTester(d.view, size[0], size[1])
		r, ok := view.Find("Terminal")
		if !ok || r.H < 80 || r.Y < 0 || r.Y+r.H > float32(size[1]) {
			t.Errorf("terminal unusable at %dx%d: found=%v rect=%+v", size[0], size[1], ok, r)
		}
	}
	view := ui.NewTester(d.view, 640, 480)
	panel, ok := view.Find("快速命令面板")
	if !ok || panel.H != 90 {
		t.Fatalf("compact quick-command panel not found: %+v", panel)
	}
	view.Scroll(panel.X+panel.W/2, panel.Y+panel.H/2, 0, 350)
	filter, ok := view.Find("搜索快速命令")
	if !ok || filter.Y+filter.H/2 < panel.Y || filter.Y+filter.H/2 > panel.Y+panel.H {
		t.Fatalf("quick-command controls cannot be reached by scrolling: panel=%+v filter=%+v", panel, filter)
	}
}

func TestSplitTerminalsRemainVisibleWithPanels(t *testing.T) {
	firstConn, firstPeer := net.Pipe()
	defer firstPeer.Close()
	secondConn, secondPeer := net.Pipe()
	defer secondPeer.Close()
	first, err := terminal.New(terminal.Options{Conn: firstConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := terminal.New(terminal.Options{Conn: secondConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := &desktop{workspace: "terminal", term: first, currentSession: 1, splitSession: 2, splitFirstSession: 1, sessions: []*workspaceSession{{id: 1, label: "first", term: first}, {id: 2, label: "second", term: second}}, quickOpen: true, terminalSearchOpen: true, showFilePanel: true, quickCommands: []quickCommand{{ID: "one", Name: "test", Command: "pwd"}}}
	for _, size := range [][2]int{{640, 480}, {900, 650}, {900, 700}, {940, 480}, {1024, 480}, {1200, 480}, {1200, 760}, {1500, 900}} {
		view := ui.NewTester(d.view, size[0], size[1])
		for _, label := range []string{"终端：first", "终端：second"} {
			r, ok := view.Find(label)
			if !ok || r.H < 40 || r.W < 100 || r.X < 0 || r.X+r.W > float32(size[0]) || r.Y < 0 || r.Y+r.H > float32(size[1]) {
				t.Errorf("%s unusable at %dx%d: found=%v rect=%+v", label, size[0], size[1], ok, r)
			}
		}
		if size[1] < 650 {
			a, _ := view.Find("终端：first")
			b, _ := view.Find("终端：second")
			if a.X+a.W > b.X+1 && b.X+b.W > a.X+1 {
				t.Errorf("shallow-window panes overlap at %dx%d: %+v %+v", size[0], size[1], a, b)
			}
		}
	}
}
