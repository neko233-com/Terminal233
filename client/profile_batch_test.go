package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestProfileBatchSelectionCopyAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.profiles = []profile{
		{ID: "1", Label: "alpha", Group: "one", Favorite: true, LastConnected: 4, Settings: connectionSettings{Host: "alpha.test", Port: "22", User: "alice", Fingerprint: "SHA256:trusted", Password: "never-save"}},
		{ID: "2", Label: "beta", Group: "two", Settings: connectionSettings{Host: "beta.test", Port: "22", User: "bob"}},
	}
	d.nextProfileID = 2
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1400, 1400)
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("勾选本机连接：alpha"); err != nil {
		t.Fatal(err)
	}
	d.profileSearch = "beta"
	view.Frame()
	if err := view.Click("勾选当前显示的本机连接"); err != nil {
		t.Fatal(err)
	}
	if !d.checkedProfiles["1"] || !d.checkedProfiles["2"] {
		t.Fatal("visible selection did not preserve the hidden selection")
	}
	if !view.HasText("已勾选 2 个本机连接（包括筛选隐藏项）") {
		t.Fatal("hidden selection was not disclosed")
	}
	if err := view.Click("复制选中连接"); err != nil {
		t.Fatal(err)
	}
	if len(d.profiles) != 4 || d.profiles[2].Settings.Password != "" || d.profiles[2].Favorite || d.profiles[2].LastConnected != 0 || d.profiles[2].Settings.Fingerprint != "SHA256:trusted" {
		t.Fatal("copied profiles lost trusted identity or retained credentials and history")
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "never-save") {
		t.Fatal("saved profiles contain credentials")
	}
	d.profileSearch = ""
	view.Frame()
	if err := view.Click("勾选本机连接：alpha（副本）"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("移除选中连接…"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("确认移除全部 1 个已勾选的本机连接？") {
		t.Fatal("batch delete did not ask for confirmation")
	}
	if err := view.Click("确认批量移除"); err != nil {
		t.Fatal(err)
	}
	restored := &desktop{}
	if err := restored.loadProfiles(path); err != nil || len(restored.profiles) != 3 {
		t.Fatal("confirmed deletion did not persist")
	}
}

func TestProfileBatchConflictPreservesSelectionAndData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	a, b := &desktop{}, &desktop{}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.profiles = []profile{{ID: "1", Label: "one", Settings: connectionSettings{Host: "one.test", Port: "22", User: "u"}}}
	a.nextProfileID = 1
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.profiles[0].Label = "newer"
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	b.checkedProfiles = map[profileID]bool{"1": true}
	b.copyCheckedProfiles()
	if len(b.profiles) != 1 || b.nextProfileID != 1 || !b.checkedProfiles["1"] || !b.profileStoreConflict {
		t.Fatal("failed copy changed the stale window or lost selection")
	}
	b.confirmProfileBatchDelete = true
	b.deleteCheckedProfiles()
	if len(b.profiles) != 1 || !b.checkedProfiles["1"] || b.confirmProfileBatchDelete {
		t.Fatal("failed deletion changed data or left confirmation armed")
	}
	if err := b.loadProfiles(path); err != nil || b.profiles[0].Label != "newer" || len(b.checkedProfiles) != 0 {
		t.Fatal("reload did not preserve the newer version and clear selection")
	}
}
