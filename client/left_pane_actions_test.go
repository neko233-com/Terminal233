package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestLeftPaneFileOperationsProtectExistingAndNonemptyTargets(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "ssh"}[remote], func(t *testing.T) {
			dir := t.TempDir()
			var shell *remoteShell
			if remote {
				settings, fingerprint, _ := testSSHServer(t)
				settings.Fingerprint = fingerprint
				var err error
				shell, err = connectShell(settings)
				if err != nil {
					t.Fatal(err)
				}
				defer shell.Close()
				dir = filepath.ToSlash(dir)
			}
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
			if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
				t.Fatal(err)
			}
			scope := leftPaneScope{shell: shell, dir: dir}
			a := leftPaneAction{scope: scope, kind: "rename", name: "source", value: "target", original: remoteFile{Name: "source"}}
			if err := executeLeftPaneAction(a); err == nil {
				t.Fatal("rename overwrote existing target")
			}
			if got, _ := os.ReadFile(target); string(got) != "target" {
				t.Fatal("target changed")
			}
			a.value = "renamed"
			if err := executeLeftPaneAction(a); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "renamed")); string(got) != "source" {
				t.Fatal("rename lost data")
			}
			create := leftPaneAction{scope: scope, kind: "mkdir", value: "folder"}
			if err := executeLeftPaneAction(create); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "folder", "inside"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			remove := leftPaneAction{scope: scope, kind: "delete", name: "folder", original: remoteFile{Name: "folder", IsDir: true}}
			if err := executeLeftPaneAction(remove); err == nil {
				t.Fatal("deleted a nonempty directory")
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "folder", "inside")); string(got) != "keep" {
				t.Fatal("directory data lost")
			}
			remove.name = "renamed"
			remove.original = remoteFile{Name: "renamed", IsDir: true}
			if err := executeLeftPaneAction(remove); err == nil {
				t.Fatal("accepted a changed file type")
			}
			if _, err := os.Stat(filepath.Join(dir, "renamed")); err != nil {
				t.Fatal("changed file was deleted")
			}
		})
	}
}

func TestLeftPanePendingActionTracksDirectoryAndHost(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{localPane: localFilePane{initialized: true, directory: dir, input: dir, files: []remoteFile{{Name: "one"}}, generation: 1}}
	d.beginLeftAction("delete", "one")
	if d.leftPaneAction.kind != "delete" || !d.leftScopeMatches(d.leftPaneAction.scope) {
		t.Fatal("valid action not prepared")
	}
	d.localPane.generation++
	if d.leftScopeMatches(d.leftPaneAction.scope) {
		t.Fatal("stale directory generation was accepted")
	}
	d.beginLeftAction("delete", "missing")
	if d.leftPaneAction.name != "one" {
		t.Fatal("missing entry unexpectedly replaced action")
	}
	d.secondaryPane.sessionID = 7
	if d.leftScopeMatches(d.leftPaneAction.scope) {
		t.Fatal("action survived host switch")
	}
}

func TestLeftPaneFilterClearsSelectionAndKeepsOtherPane(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{workspace: "files", shell: &remoteShell{}, term: &terminal.Terminal{}, currentSession: 1, fileDir: "/remote", files: []remoteFile{{Name: "right"}}, localPane: localFilePane{initialized: true, directory: dir, input: dir, files: []remoteFile{{Name: "alpha"}, {Name: "beta"}}}}
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := v.ClickWith(ui.Cmd, "beta"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("筛选左侧文件"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("筛选左侧本机文件"); err != nil {
		t.Fatal(err)
	}
	v.Type("alpha")
	if len(d.localPane.paneSelection.names) != 0 || d.localPane.selected != "" {
		t.Fatal("filter retained a hidden selection")
	}
	if !v.HasText("alpha") || v.HasText("beta") || !v.HasText("right") {
		t.Fatal("filter affected the wrong list")
	}
}

func TestRightPaneFilterClearsBatchSelection(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{workspace: "files", term: &terminal.Terminal{}, currentSession: 1, fileDir: dir, files: []remoteFile{{Name: "alpha"}, {Name: "beta"}}, localPane: localFilePane{initialized: true, directory: dir, input: dir}}
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := v.ClickWith(ui.Cmd, "beta"); err != nil {
		t.Fatal(err)
	}
	if len(d.filePaneSelection.names) != 2 {
		t.Fatal("batch selection failed")
	}
	if err := v.Click("筛选文件列表"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("筛选文件"); err != nil {
		t.Fatal(err)
	}
	v.Type("alpha")
	if len(d.filePaneSelection.names) != 0 || d.selectedFile != "" {
		t.Fatal("filter retained hidden selection")
	}
}
