package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestQuickPackagesPersistAndProtectConcurrentChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quick.json")
	a := &desktop{quickPath: path, quickPackageName: "生产"}
	a.saveQuickPackage(false)
	if len(a.quickPackages) != 1 {
		t.Fatal(a.quickStatus)
	}
	group := a.quickPackages[0]
	a.quickPackageID, a.quickName, a.quickText = group.ID, "目录", "pwd"
	if !a.saveQuickCommand() {
		t.Fatal(a.quickStatus)
	}
	b := &desktop{quickPath: path}
	b.reloadQuickCommands()
	if len(b.quickPackages) != 1 || b.quickCommands[0].PackageID != group.ID {
		t.Fatal("group assignment was not preserved")
	}
	a.quickName, a.quickText = "用户", "whoami"
	if !a.saveQuickCommand() {
		t.Fatal(a.quickStatus)
	}
	b.quickPackageFilter = group.ID
	b.deleteQuickPackage()
	b.deleteQuickPackage()
	if !strings.Contains(b.quickStatus, "其他窗口修改") {
		t.Fatal("stale group removal was not rejected")
	}
	a.quickPackageFilter = group.ID
	a.deleteQuickPackage()
	a.deleteQuickPackage()
	saved, err := readQuickCommandFile(path)
	if err != nil || saved.Version != 3 || len(saved.Packages) != 0 || len(saved.Items) != 2 || saved.Items[0].PackageID != "" || saved.Items[1].PackageID != "" {
		t.Fatalf("group removal should retain ungrouped snippets and version 3: %#v, %v", saved, err)
	}
	bad := quickCommandFile{Version: 3, Items: []quickCommand{{ID: strings.Repeat("0", 32), Name: "x", Command: "pwd", PackageID: group.ID}}}
	data, _ := json.Marshal(bad)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readQuickCommandFile(path); err == nil {
		t.Fatal("orphaned group reference accepted")
	}
}

func TestQuickPackageCreateRenameAndFilter(t *testing.T) {
	d := &desktop{workspace: "snippets", quickPath: filepath.Join(t.TempDir(), "quick.json")}
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("分组"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("分组名称"); err != nil {
		t.Fatal(err)
	}
	view.Type("生产")
	if err := view.Click("新建分组"); err != nil {
		t.Fatal(err)
	}
	if len(d.quickPackages) != 1 || d.quickPackageFilter != d.quickPackages[0].ID {
		t.Fatalf("group was not created and selected: %s", d.quickStatus)
	}
	d.quickPackageName = "Production"
	view.Frame()
	if err := view.Click("重命名分组"); err != nil {
		t.Fatal(err)
	}
	if d.quickPackages[0].Name != "Production" {
		t.Fatal("rename failed")
	}
	id := d.quickPackages[0].ID
	items := []quickCommand{{Name: "a", PackageID: id}, {Name: "b"}}
	if got := filterQuickPackage(items, id); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("group filter: %#v", got)
	}
	if got := filterQuickPackage(items, "-"); len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("ungrouped filter: %#v", got)
	}
	if err := view.Click("+ 新建命令"); err != nil {
		t.Fatal(err)
	}
	if d.quickPackageID != id {
		t.Fatal("new snippet did not inherit the selected group")
	}
}

func TestQuickPackageMoveAndStaleAssignment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quick.json")
	a := &desktop{quickPath: path, quickPackageName: "Ops"}
	a.saveQuickPackage(false)
	group := a.quickPackages[0]
	a.quickPackageName = "ops"
	a.saveQuickPackage(false)
	if !strings.Contains(a.quickStatus, "同名") || len(a.quickPackages) != 1 {
		t.Fatal("duplicate group was not rejected")
	}
	a.quickPackageID, a.quickName, a.quickText = "", "检查", "pwd"
	if !a.saveQuickCommand() {
		t.Fatal(a.quickStatus)
	}
	item := a.quickCommands[0]
	a.quickEdit, a.quickName, a.quickText, a.quickPackageID = &item, item.Name, item.Command, group.ID
	if !a.saveQuickCommand() || a.quickCommands[0].PackageID != group.ID {
		t.Fatalf("moving snippet to group failed: %s", a.quickStatus)
	}
	b := &desktop{quickPath: path}
	b.reloadQuickCommands()
	stale := b.quickCommands[0]
	a.quickPackageFilter = group.ID
	a.deleteQuickPackage()
	a.deleteQuickPackage()
	b.quickEdit, b.quickName, b.quickText, b.quickPackageID = &stale, stale.Name, stale.Command, group.ID
	if b.saveQuickCommand() || !strings.Contains(b.quickStatus, "分组已被删除") {
		t.Fatal("saving into a removed group should fail")
	}
	saved, err := readQuickCommandFile(path)
	if err != nil || len(saved.Items) != 1 || saved.Items[0].PackageID != "" {
		t.Fatalf("stale save changed retained snippet: %#v, %v", saved, err)
	}
}
