package main

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestBatchSelectionDoesNotBlockTerminalSidePanel(t *testing.T) {
	d := quickCardsFixture(t)
	d.quickBatch = true
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d.workspace, d.term, d.quickOpen = "terminal", term, true
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("+ 新建命令"); err != nil || !d.quickEditorOpen {
		t.Fatalf("batch mode blocked terminal side editor: %v", err)
	}
	d.quickEditorOpen, d.workspace, d.quickPackages = false, "snippets", nil
	view.Frame()
	if err := view.Click("完成选择"); err != nil || d.quickBatch {
		t.Fatalf("cannot leave batch mode after group removal: %v", err)
	}
}

func quickCardsFixture(t *testing.T) *desktop {
	t.Helper()
	d := &desktop{workspace: "snippets", quickPath: filepath.Join(t.TempDir(), "commands.json"), quickPackageName: "生产"}
	d.saveQuickPackage(false)
	if len(d.quickPackages) != 1 {
		t.Fatal(d.quickStatus)
	}
	d.quickPackageFilter = ""
	for _, name := range []string{"目录", "用户"} {
		d.quickName, d.quickText, d.quickPackageID = name, "pwd", ""
		if !d.saveQuickCommand() {
			t.Fatal(d.quickStatus)
		}
	}
	return d
}

func TestQuickPackageCardsMoveMultipleAndNavigate(t *testing.T) {
	d := quickCardsFixture(t)
	view := ui.NewTester(d.view, 1280, 1200)
	if !view.HasText("打开片段分组：生产") || !view.HasText("打开未分组片段") {
		t.Fatal("package cards missing")
	}
	if err := view.Click("选择片段"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"勾选片段：目录", "勾选片段：用户"} {
		if err := view.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.quickChecked) != 2 {
		t.Fatal("batch selection missing")
	}
	from, fromOK := view.Find("选择命令片段：目录")
	to, toOK := view.Find("打开片段分组：生产")
	if !fromOK || !toOK {
		t.Fatal("drag source or target missing")
	}
	view.Press(from.X+from.W/2, from.Y+from.H/2)
	view.Move(to.X+to.W/2, to.Y+to.H/2)
	view.Release(to.X+to.W/2, to.Y+to.H/2)
	if d.quickCommands[0].PackageID != d.quickPackages[0].ID || d.quickCommands[1].PackageID != d.quickPackages[0].ID || len(d.quickChecked) != 0 {
		t.Fatalf("drag did not move both selected snippets: %s", d.quickStatus)
	}
	if err := view.Click("打开未分组片段"); err != nil {
		t.Fatal(err)
	}
	if d.quickPackageFilter != "-" || view.HasText("选择命令片段：目录") {
		t.Fatal("ungrouped card did not filter moved snippets")
	}
	if err := view.Click("打开片段分组：生产"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("选择命令片段：目录") {
		t.Fatal("package card did not show moved snippets")
	}
}

func TestQuickPackageBatchMoveIsAtomicAcrossWindows(t *testing.T) {
	a := quickCardsFixture(t)
	b := &desktop{quickPath: a.quickPath}
	b.reloadQuickCommands()
	items := append([]quickCommand(nil), b.quickCommands...)
	target := b.quickPackages[0]
	changed := a.quickCommands[1]
	a.quickEdit, a.quickName, a.quickText = &changed, changed.Name, "whoami"
	if !a.saveQuickCommand() {
		t.Fatal(a.quickStatus)
	}
	b.moveQuickCommands(items, &target)
	if !strings.Contains(b.quickStatus, "其他窗口修改") {
		t.Fatal("stale batch was not rejected")
	}
	saved, err := readQuickCommandFile(a.quickPath)
	if err != nil || saved.Items[0].PackageID != "" || saved.Items[1].PackageID != "" || saved.Items[1].Command != "whoami" {
		t.Fatalf("stale move partially applied: %#v, %v", saved, err)
	}
	b.reloadQuickCommands()
	items = append([]quickCommand(nil), b.quickCommands...)
	a.quickPackageFilter, a.quickPackageName = target.ID, "renamed"
	a.saveQuickPackage(true)
	b.moveQuickCommands(items, &target)
	if !strings.Contains(b.quickStatus, "目标分组") {
		t.Fatal("stale destination was not rejected")
	}
}

func TestQuickPackageMoveButtonWithoutDrag(t *testing.T) {
	d := quickCardsFixture(t)
	view := ui.NewTester(d.view, 1280, 1200)
	if err := view.Click("选择片段"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("勾选片段：目录"); err != nil {
		t.Fatal(err)
	}
	d.quickMoveTarget = d.quickPackages[0].ID
	view.Frame()
	if err := view.Click("移动"); err != nil {
		t.Fatal(err)
	}
	if d.quickCommands[0].PackageID != d.quickPackages[0].ID || d.quickCommands[1].PackageID != "" {
		t.Fatalf("button did not move only selected snippet: %s", d.quickStatus)
	}
}
