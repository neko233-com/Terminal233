package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestDragTerminalTabsToSplitAndReorder(t *testing.T) {
	terms := groupTestTerminals(t, 3)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "alpha", term: terms[0]}, {id: 2, label: "bravo", term: terms[1]}, {id: 3, label: "charlie", term: terms[2]}}}
	view := ui.NewTester(d.view, 1500, 1000)
	drag := func(from, to string, fraction float32) {
		t.Helper()
		a, ok := view.Find(from)
		if !ok {
			t.Fatalf("source tab %s missing", from)
		}
		b, ok := view.Find(to)
		if !ok {
			t.Fatalf("target tab %s missing", to)
		}
		sx, sy := a.X+a.W/2, a.Y+a.H/2
		tx, ty := b.X+b.W*fraction, b.Y+b.H/2
		view.Move(sx, sy)
		view.Press(sx, sy)
		view.Move(tx, ty)
		view.Release(tx, ty)
	}
	drag("会话标签：bravo", "会话标签：alpha", 0.1)
	if d.terminalGroupFor(1) != nil || d.sessions[0].id != 2 {
		t.Fatalf("edge drag did not reorder: %v group=%v", d.sessions[0].id, d.terminalGroupFor(1))
	}
	drag("会话标签：bravo", "会话标签：alpha", 0.5)
	g := d.terminalGroupFor(1)
	if g == nil || !g.grid || d.terminalGroupFor(2) != g || len(g.members) != 2 {
		t.Fatal("center drag did not create split workspace")
	}
	drag("会话标签：charlie", "工作区：工作区", 0.5)
	if d.terminalGroupFor(3) != g || len(g.members) != 3 || !g.grid {
		t.Fatal("center drag did not add to workspace")
	}
}
