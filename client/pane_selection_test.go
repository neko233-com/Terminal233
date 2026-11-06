package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestPaneSelectionSupportsControlShiftAndSnapshotValidation(t *testing.T) {
	files := []remoteFile{{Name: "one"}, {Name: "folder", IsDir: true}, {Name: "three"}, {Name: "link", IsLink: true}}
	var s paneSelection
	if name, err := s.choose("one", files, 0); err != nil || name != "one" {
		t.Fatal("single selection failed")
	}
	if _, err := s.choose("folder", files, ui.Cmd); err != nil || len(s.names) != 2 {
		t.Fatal("control selection failed")
	}
	items := s.itemsFor("one", files)
	if len(items) != 2 || !paneItemsMatch(items, files) || (!items[0].directory && !items[1].directory) {
		t.Fatal("selected file and directory snapshot incorrect")
	}
	changed := append([]remoteFile(nil), files...)
	changed[1].IsDir = false
	if paneItemsMatch(items, changed) {
		t.Fatal("snapshot accepted a changed file type")
	}
	if _, err := s.choose("three", files, ui.Shift); err != nil || len(s.names) != 2 || !s.names["folder"] || !s.names["three"] {
		t.Fatal("shift range did not follow its anchor")
	}
	if _, err := s.choose("link", files, ui.Cmd); err != nil || s.names["link"] {
		t.Fatal("symlink became transferable")
	}
	if got := s.itemsFor("one", files); len(got) != 1 || got[0].name != "one" {
		t.Fatal("dragging an unselected item should copy only that item")
	}
}

func TestPaneSelectionLimitLeavesPreviousChoiceIntact(t *testing.T) {
	files := make([]remoteFile, maxBatchFiles+1)
	for i := range files {
		files[i].Name = fmt.Sprintf("f%03d", i)
	}
	var s paneSelection
	if _, err := s.choose(files[0].Name, files, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.choose(files[len(files)-1].Name, files, ui.Shift); err == nil {
		t.Fatal("oversized range was accepted")
	}
	if len(s.names) != 1 || !s.names[files[0].Name] {
		t.Fatal("oversized range changed the selection")
	}
}

func TestDualPaneControlSelectionAndClear(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{workspace: "files", shell: &remoteShell{}, term: &terminal.Terminal{}, currentSession: 1, fileDir: "/remote", files: []remoteFile{{Name: "right-a"}, {Name: "right-b", IsDir: true}}, localPane: localFilePane{initialized: true, directory: dir, input: dir, files: []remoteFile{{Name: "left-a"}, {Name: "left-b", IsDir: true}}}}
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("left-a"); err != nil {
		t.Fatal(err)
	}
	if err := view.ClickWith(ui.Cmd, "📁 left-b"); err != nil {
		t.Fatal(err)
	}
	if len(d.localPaneDrag("left-b").items) != 2 || !view.HasText("清除本机文件选择") {
		t.Fatal("local pane did not expose a multi-selection")
	}
	if err := view.Click("right-a"); err != nil {
		t.Fatal(err)
	}
	if err := view.ClickWith(ui.Cmd, "📁 right-b"); err != nil {
		t.Fatal(err)
	}
	if len(d.rightPaneDrag("right-b").items) != 2 || !view.HasText("清除会话文件选择") {
		t.Fatal("right pane did not expose a multi-selection")
	}
	if err := view.Click("清除本机文件选择"); err != nil {
		t.Fatal(err)
	}
	if len(d.localPane.paneSelection.names) != 0 || len(d.filePaneSelection.names) != 2 {
		t.Fatal("clearing one pane changed the other pane")
	}
}
