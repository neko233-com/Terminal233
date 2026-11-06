package main

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestSecondaryPaneSelectsIndependentSessionAndRejectsStaleCopies(t *testing.T) {
	leftShell, rightShell := &remoteShell{}, &remoteShell{}
	leftTerm, rightTerm := &terminal.Terminal{}, &terminal.Terminal{}
	d := &desktop{workspace: "files", currentSession: 2, shell: rightShell, term: rightTerm, fileDir: "/right", files: []remoteFile{{Name: "right.txt"}}, localPane: localFilePane{initialized: true}, sessions: []*workspaceSession{{id: 1, label: "left", shell: leftShell, term: leftTerm, fileDir: "/left"}, {id: 2, label: "right", shell: rightShell, term: rightTerm, fileDir: "/right"}}}
	d.fileHistory.visit("/right")
	view := ui.NewTester(d.view, 1280, 800)
	if err := view.Click("选择左侧文件主机"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("左侧文件会话：left · 会话 1"); err != nil {
		t.Fatal(err)
	}
	if d.secondaryPane.sessionID != 1 || d.secondaryPane.directory != "/left" || d.currentSession != 2 || d.fileDir != "/right" {
		t.Fatal("left selection changed the right session or directory")
	}
	p := &d.secondaryPane
	d.finishSecondaryPaneLoad(p.generation, 1, leftShell, "/left", -1, []remoteFile{{Name: "left.txt"}, {Name: "folder", IsDir: true}, {Name: "link", IsLink: true}}, nil)
	d.win = &mygo.Window{}
	view.Frame()
	if !view.HasText("左侧远程文件列表") || !view.HasText("left.txt") || !view.HasText("right.txt") {
		t.Fatal("both remote panes were not shown")
	}
	left, right := d.leftPaneDrag("left.txt"), d.rightPaneDrag("right.txt")
	if !d.canCopySecondaryToRight(left) || !d.canCopyRightToSecondary(right) {
		t.Fatal("valid remote pair was rejected")
	}
	history := d.secondaryPane.history
	d.secondaryPane.history = fileHistory{}
	if d.canCopyRightToSecondary(right) {
		t.Fatal("accepted an unreadable or never-opened left directory")
	}
	d.secondaryPane.history = history
	d.shell = nil
	if !d.canCopySecondaryToRight(d.leftPaneDrag("left.txt")) || !d.canCopyRightToSecondary(d.rightPaneDrag("right.txt")) {
		t.Fatal("local right pane was rejected")
	}
	d.shell = rightShell
	oldGeneration := d.secondaryPane.generation
	d.secondaryPane.generation++
	if d.canCopySecondaryToRight(left) || d.canCopyRightToSecondary(right) {
		t.Fatal("accepted a drag from before a left directory change")
	}
	d.secondaryPane.generation = oldGeneration
	d.fileDir = "/unopened"
	if d.canCopySecondaryToRight(d.leftPaneDrag("left.txt")) || d.canCopyRightToSecondary(right) {
		t.Fatal("accepted an unconfirmed or changed right directory")
	}
	d.fileDir = "/right"
	d.secondaryPane.input = "/unopened"
	if d.canCopySecondaryToRight(d.leftPaneDrag("left.txt")) || d.canCopyRightToSecondary(d.rightPaneDrag("right.txt")) {
		t.Fatal("accepted an unconfirmed left directory")
	}
	d.secondaryPane.input = "/left"
	if d.canCopySecondaryToRight(d.leftPaneDrag("folder")) || d.canCopySecondaryToRight(d.leftPaneDrag("link")) {
		t.Fatal("accepted a directory or symlink")
	}
	d.sessions = d.sessions[1:]
	if d.canCopySecondaryToRight(d.leftPaneDrag("left.txt")) || d.canCopyRightToSecondary(d.rightPaneDrag("right.txt")) {
		t.Fatal("accepted a closed left session")
	}
}

func TestSecondaryPaneIgnoresLateDirectoryReads(t *testing.T) {
	shell := &remoteShell{}
	term := &terminal.Terminal{}
	d := &desktop{sessions: []*workspaceSession{{id: 1, shell: shell, term: term}}, secondaryPane: secondaryRemotePane{sessionID: 1, shell: shell, generation: 2, directory: "/previous", input: "/new", loading: true, files: []remoteFile{{Name: "previous"}}}}
	d.finishSecondaryPaneLoad(1, 1, shell, "/old", -1, []remoteFile{{Name: "stale"}}, nil)
	if !d.secondaryPane.loading || d.secondaryPane.directory != "/previous" || d.secondaryPane.files[0].Name != "previous" {
		t.Fatal("stale directory result replaced the active pane")
	}
	d.finishSecondaryPaneLoad(2, 1, shell, "/new", -1, []remoteFile{{Name: "new"}}, nil)
	if d.secondaryPane.loading || d.secondaryPane.directory != "/new" || d.secondaryPane.files[0].Name != "new" {
		t.Fatal("current directory result was not applied")
	}
}
