package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWorkspaceMemberRenameIsScopedAndPersisted(t *testing.T) {
	terms := groupTestTerminals(t, 2)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "alpha", term: terms[0]}, {id: 2, label: "bravo", term: terms[1]}}}
	if err := d.createTerminalGroup("project", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	g := d.terminalGroupFor(1)
	g.savedLocal = &savedWorkspace{}
	g.savedSignature = d.workspaceGroupSignature(g)
	view := ui.NewTester(d.view, 1400, 900)
	if err := view.RightClick("切换到终端：bravo"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("重命名终端…"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("终端新名称"); err != nil {
		t.Fatal(err)
	}
	view.TypeKey(ui.Cmd, ui.KeyA, "")
	view.Type("数据库")
	if err := view.Click("保存终端名称"); err != nil {
		t.Fatal(err)
	}
	if d.sessions[1].label != "bravo" || g.tabNames[2] != "数据库" || !d.workspaceGroupDirty(g) {
		t.Fatal("alias changed host identity or did not mark workspace dirty")
	}
	if _, ok := view.Find("切换到终端：数据库"); !ok {
		t.Fatal("renamed tab not displayed")
	}
	w, err := d.workspaceSnapshot("project")
	if err != nil || w.Entries[1].Label != "bravo" || w.Entries[1].TabName != "数据库" {
		t.Fatal("alias not captured separately", err)
	}
	path := filepath.Join(t.TempDir(), "workspaces.json")
	file, err := updateWorkspaceTemplates(path, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, w); return nil })
	if err != nil || file.Version != 5 {
		t.Fatal("alias format not upgraded", err)
	}
	file, err = readWorkspaceTemplates(path)
	if err != nil || file.Items[0].Entries[1].TabName != "数据库" {
		t.Fatal("alias did not persist", err)
	}
	// Older formats must not be allowed to hide aliases from older applications.
	file.Version = 4
	data, _ := json.Marshal(file)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readWorkspaceTemplates(path); err == nil {
		t.Fatal("alias accepted in old format")
	}
	d.detachGroupSession(2)
	if d.sessions[1].label != "bravo" || g.tabNames[2] != "" {
		t.Fatal("alias leaked into detached session")
	}
}

func TestWorkspaceMemberRenameValidatesAndRestoresDefault(t *testing.T) {
	terms := groupTestTerminals(t, 1)
	d := &desktop{sessions: []*workspaceSession{{id: 1, label: "original", term: terms[0]}}}
	if err := d.createTerminalGroup("one", []int{1}, 1, false); err != nil {
		t.Fatal(err)
	}
	g := d.terminalGroupFor(1)
	s := d.sessions[0]
	g.memberRenameID = 1
	g.memberRenameOpen = true
	g.memberRenameDraft = " \n "
	d.renameGroupMember(g, s)
	if g.memberRenameStatus == "" || !g.memberRenameOpen {
		t.Fatal("invalid name accepted")
	}
	g.memberRenameDraft = "new"
	d.renameGroupMember(g, s)
	if groupSessionName(g, s) != "new" {
		t.Fatal("alias not set")
	}
	g.memberRenameID = 1
	g.memberRenameOpen = true
	g.memberRenameDraft = "original"
	d.renameGroupMember(g, s)
	if groupSessionName(g, s) != "original" || len(g.tabNames) != 0 {
		t.Fatal("default name not restored")
	}
}
