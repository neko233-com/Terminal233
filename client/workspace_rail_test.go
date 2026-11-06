package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCompactRailNavigationAndPreferences(t *testing.T) {
	d := &desktop{workspace: "connections", preferencesPath: filepath.Join(t.TempDir(), "preferences.json")}
	view := ui.NewTester(d.view, 1200, 760)
	if d.sidebarWidth(1200) != 64 || !view.HasText("SSH 连接") || !view.HasText("端口转发") || !view.HasText("展开文字导航") {
		t.Fatal("compact navigation is incomplete")
	}
	if err := view.Click("端口转发"); err != nil || d.workspace != "forward" {
		t.Fatalf("compact navigation failed: %v", err)
	}
	if err := view.Click("展开文字导航"); err != nil {
		t.Fatal(err)
	}
	if !d.railExpanded || d.sidebarWidth(1200) != 235 || !view.HasText("工作区") {
		t.Fatal("expanded navigation did not open")
	}
	prefs, err := readPreferences(d.preferencesPath)
	if err != nil || !prefs.SidebarExpanded {
		t.Fatalf("expanded preference not saved: %v", err)
	}
	if err := view.Click("使用图标导航"); err != nil {
		t.Fatal(err)
	}
	prefs, err = readPreferences(d.preferencesPath)
	if err != nil || prefs.SidebarExpanded || d.railExpanded {
		t.Fatalf("compact preference not saved: %v", err)
	}
	if err := view.Click("连接筛选"); err != nil || !d.railExpanded || !d.showConnectionNavigation || !view.HasText("全部连接") {
		t.Fatalf("filter did not reveal navigation tree: %v", err)
	}
}
