package main

import (
	"fmt"
	"net"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func groupTestTerminals(t *testing.T, n int) []*terminal.Terminal {
	t.Helper()
	terms := make([]*terminal.Terminal, 0, n)
	for i := 0; i < n; i++ {
		left, right := net.Pipe()
		term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = term.Close(); _ = right.Close() })
		terms = append(terms, term)
	}
	return terms
}

func TestTerminalGroupsKeepTabOrderAndFocus(t *testing.T) {
	terms := groupTestTerminals(t, 4)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "one", term: terms[0]}, {id: 2, label: "two", term: terms[1]}, {id: 3, label: "three", term: terms[2]}, {id: 4, label: "four", term: terms[3]}}}
	if err := d.createTerminalGroup("project", []int{1, 3}, 1, false); err != nil {
		t.Fatal(err)
	}
	if len(d.terminalTopTabs()) != 3 || d.terminalGroupFor(3) != d.terminalGroupFor(1) {
		t.Fatal("sessions were not grouped")
	}
	d.moveSession(1, 4)
	want := []int{2, 4, 1, 3}
	for i, id := range want {
		if d.sessions[i].id != id {
			t.Fatalf("group reorder split members: %+v", d.sessions)
		}
	}
	d.selectAdjacentSession(1)
	if d.currentSession != 2 {
		t.Fatalf("tab navigation did not skip grouped members: %d", d.currentSession)
	}
	d.selectAdjacentSession(-1)
	if d.currentSession != 1 {
		t.Fatal("tab navigation did not return to focused member")
	}
	d.selectSession(3)
	d.terminalGroupFor(3).grid = true
	if d.terminalGroupFor(3).focused != 3 {
		t.Fatal("group did not remember focus")
	}
	d.closeSession(3)
	if d.currentSession != 1 || len(d.terminalGroupFor(1).members) != 1 || d.terminalGroupFor(1).grid {
		t.Fatal("closing group member lost sibling focus or retained an invalid grid")
	}
	group := d.terminalGroupFor(1)
	d.closeTerminalGroup(group.id)
	if d.terminalGroupFor(1) == nil || len(d.sessions) != 3 {
		t.Fatal("first group close did not require confirmation")
	}
	d.closeTerminalGroup(group.id)
	if d.terminalGroupFor(1) != nil || len(d.sessions) != 2 {
		t.Fatal("confirmed group close did not close its members")
	}
}

func TestTerminalGroupFocusAndGridShowEverySession(t *testing.T) {
	terms := groupTestTerminals(t, 3)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "one", term: terms[0]}, {id: 2, label: "two", term: terms[1]}, {id: 3, label: "three", term: terms[2]}}}
	if err := d.createTerminalGroup("project", []int{1, 2, 3}, 1, false); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1400, 900)
	if _, ok := view.Find("工作区：project"); !ok {
		t.Fatal("group tab missing")
	}
	if _, ok := view.Find("终端：one"); !ok {
		t.Fatal("focused terminal missing")
	}
	if _, ok := view.Find("终端：two"); ok {
		t.Fatal("focus view showed another terminal")
	}
	if err := view.Click("切换到终端：two"); err != nil {
		t.Fatal(err)
	}
	if d.currentSession != 2 {
		t.Fatal("focus view did not switch terminal")
	}
	if err := view.Click("分屏查看"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"终端：one", "终端：two", "终端：three"} {
		if _, ok := view.Find(label); !ok {
			t.Fatalf("grid omitted %s", label)
		}
	}
	if err := view.Click("关闭分屏"); err != nil {
		t.Fatal(err)
	}
	if d.terminalGroupFor(2).grid {
		t.Fatal("focus view did not return")
	}
}

func TestWorkspaceTemplateSavesOnlyActiveGroupAndView(t *testing.T) {
	terms := groupTestTerminals(t, 3)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "one", term: terms[0]}, {id: 2, label: "two", term: terms[1]}, {id: 3, label: "outside", term: terms[2]}}}
	if err := d.createTerminalGroup("project", []int{1, 2}, 1, true); err != nil {
		t.Fatal(err)
	}
	d.terminalGroupFor(1).rowPerc[0] = 33
	d.terminalGroupFor(1).columnPerc[0] = 65
	w, err := d.workspaceSnapshot("saved")
	if err != nil {
		t.Fatal(err)
	}
	invalid := w
	invalid.SplitFirst, invalid.SplitSecond = 0, 1
	if validateSavedWorkspace(invalid) == nil {
		t.Fatal("accepted conflicting legacy and group layouts")
	}
	if len(w.Entries) != 2 || w.ViewMode != "grid" || w.Active != 0 || w.SplitFirst != -1 {
		t.Fatalf("incorrect group snapshot: %+v", w)
	}
	filename := filepath.Join(t.TempDir(), "workspaces.json")
	file, err := updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, w); return nil })
	if err != nil || file.Version != 2 {
		t.Fatalf("group schema not upgraded: %v", err)
	}
	read, err := readWorkspaceTemplates(filename)
	if err != nil || len(read.Items) != 1 || read.Items[0].ViewMode != "grid" || read.Items[0].RowPerc[0] != 33 || read.Items[0].ColumnPerc[0] != 65 {
		t.Fatalf("group template not restored: %v", err)
	}
}

func TestTerminalGroupSelectAddAndDetach(t *testing.T) {
	terms := groupTestTerminals(t, 3)
	d := &desktop{workspace: "new-tab", newTabOpen: true, currentSession: 1, term: terms[0], workspaceTemplateName: "project", sessions: []*workspaceSession{{id: 1, label: "one", term: terms[0]}, {id: 2, label: "two", term: terms[1]}, {id: 3, label: "three", term: terms[2]}}}
	view := ui.NewTester(d.view, 1400, 900)
	if err := view.Click("工作区"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("选择终端"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("选择终端：two · 2"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("组成工作区"); err != nil {
		t.Fatal(err)
	}
	group := d.terminalGroupFor(1)
	if group == nil || d.terminalGroupFor(3) != group || d.terminalGroupFor(2) != nil {
		t.Fatal("selected subset was not grouped")
	}
	if err := view.RightClick("工作区：project"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("重命名工作区…"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("工作区新名称"); err != nil {
		t.Fatal(err)
	}
	view.TypeKey(ui.Cmd, ui.KeyA, "")
	view.Type("renamed")
	if err := view.Click("重命名"); err != nil {
		t.Fatal(err)
	}
	if group.name != "renamed" {
		t.Fatal("group name was not updated")
	}
	if err := view.RightClick("切换到终端：three"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("向前移动"); err != nil {
		t.Fatal(err)
	}
	if group.members[0] != 3 || group.members[1] != 1 {
		t.Fatal("group member order did not change")
	}
	if err := view.Click("添加工作区终端"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("添加终端：two"); err != nil {
		t.Fatal(err)
	}
	if d.terminalGroupFor(2) != group || d.currentSession != 2 {
		t.Fatal("existing session was not added and focused")
	}
	if err := view.RightClick("切换到终端：two"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("移出工作区"); err != nil {
		t.Fatal(err)
	}
	if d.terminalGroupFor(2) != nil || d.currentSession != 2 || len(d.sessions) != 3 {
		t.Fatal("detaching a terminal closed or changed it")
	}
}

func TestTerminalGroupGridKeepsTwelveTerminalsUsable(t *testing.T) {
	terms := groupTestTerminals(t, 12)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0]}
	ids := make([]int, 0, 12)
	for i, term := range terms {
		id := i + 1
		ids = append(ids, id)
		d.sessions = append(d.sessions, &workspaceSession{id: id, label: fmt.Sprintf("host-%d", id), term: term})
	}
	if err := d.createTerminalGroup("all", ids, 1, true); err != nil {
		t.Fatal(err)
	}
	wide := ui.NewTester(d.view, 1280, 850)
	for _, id := range ids {
		label := fmt.Sprintf("终端：host-%d", id)
		rect, ok := wide.Find(label)
		if !ok || rect.W < 100 || rect.H < 100 {
			t.Fatalf("wide grid terminal %s is too small: %+v", label, rect)
		}
	}
	narrow := ui.NewTester(d.view, 640, 480)
	for _, id := range ids[:4] {
		rect, ok := narrow.Find(fmt.Sprintf("终端：host-%d", id))
		if !ok || rect.W < 75 || rect.H < 100 {
			t.Fatalf("narrow grid terminal %d is too small: %+v", id, rect)
		}
	}
	grid, ok := narrow.Find("工作区分屏")
	if !ok {
		t.Fatal("grid scroll container missing")
	}
	narrow.Scroll(grid.X+grid.W-2, grid.Y+20, 0, 900)
	for _, id := range ids[8:] {
		rect, ok := narrow.Find(fmt.Sprintf("终端：host-%d", id))
		if !ok || rect.W < 75 || rect.H < 100 {
			t.Fatalf("narrow grid terminal %d is not reachable: %+v", id, rect)
		}
	}
}
