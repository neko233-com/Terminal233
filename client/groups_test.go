package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestGroupsCreateRenameRemoveAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.groupInput = "公司"
	d.createGroup()
	if d.profileFilter != "公司" {
		t.Fatalf("group not created: %s", d.status)
	}
	d.groupInput = "生产"
	d.createGroup()
	if d.profileFilter != "公司/生产" {
		t.Fatalf("subgroup not created: %s", d.status)
	}
	d.profiles = []profile{{ID: "1", Label: "service", Group: "公司/生产", Settings: connectionSettings{Host: "host.test", Port: "22", User: "alice"}}}
	d.nextProfileID = 1
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	groups, counts := d.groupTree()
	if len(groups) != 2 || counts["公司"] != 1 || counts["公司/生产"] != 1 {
		t.Fatal("group hierarchy or counts incorrect")
	}
	d.profileFilter = "公司"
	if !d.profileVisible(d.profiles[0]) {
		t.Fatal("parent filter did not include descendants")
	}
	d.groupInput = "组织"
	d.changeGroup(false)
	if d.profiles[0].Group != "组织/生产" || d.profileFilter != "组织" {
		t.Fatalf("rename lost subtree: %s", d.status)
	}
	d.confirmGroupDelete = true
	d.changeGroup(true)
	if d.profiles[0].Group != "生产" || d.profileFilter != "" {
		t.Fatalf("delete did not promote children: %s", d.status)
	}
	restored := &desktop{}
	if err := restored.loadProfiles(path); err != nil || len(restored.profiles) != 1 || restored.profiles[0].Group != "生产" || !restored.groupExists("生产") {
		t.Fatalf("group state did not survive reload: %v", err)
	}
	restored.groupInput = "空组"
	restored.createGroup()
	var again desktop
	if err := again.loadProfiles(path); err != nil || !again.groupExists("空组") {
		t.Fatal("empty group was not persisted")
	}
}

func TestGroupConflictAndStaleWriterPreserveData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	a := &desktop{}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.profileGroups = []string{"a", "a/x", "x"}
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	a.profileFilter, a.confirmGroupDelete = "a", true
	a.changeGroup(true)
	if !a.groupExists("a/x") || !strings.Contains(a.status, "目标分组已存在") {
		t.Fatal("conflicting promotion was allowed")
	}
	b := &desktop{}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.profileFilter, a.groupInput = "", "new"
	a.createGroup()
	b.profileFilter, b.groupInput = "a", "renamed"
	b.changeGroup(false)
	if !b.profileStoreConflict || !b.groupExists("a/x") || b.groupExists("renamed/x") {
		t.Fatal("stale group operation changed in-memory data")
	}
}

func openConnectionNavigation(t *testing.T, view *ui.Tester) {
	t.Helper()
	if err := view.Click("连接筛选"); err != nil {
		t.Fatal(err)
	}
}

func TestGroupControlsAndLegacyProfileGroups(t *testing.T) {
	d := &desktop{workspace: "connections", profiles: []profile{{ID: "1", Label: "server", Group: "legacy/child", Settings: connectionSettings{Host: "host.test", Port: "22", User: "alice"}}}}
	view := ui.NewTester(d.view, 1400, 1400)
	openConnectionNavigation(t, view)
	if !view.HasText("分组：legacy") || !view.HasText("分组：legacy/child") {
		t.Fatal("legacy group ancestors not shown")
	}
	if err := view.Click("分组：legacy"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("分组管理"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("server") || !view.HasText("创建子分组") {
		t.Fatal("group selection did not show descendant or controls")
	}
	if err := view.Click("移除当前分组…"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("确认移除分组") {
		t.Fatal("group removal confirmation missing")
	}
}

func TestProfileDragToGroupPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.profileGroups = []string{"target"}
	d.profiles = []profile{{ID: "1", Label: "server", Settings: connectionSettings{Host: "host.test", Port: "22", User: "alice"}}}
	d.nextProfileID = 1
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1400, 1400)
	openConnectionNavigation(t, view)
	from, ok := view.Find("server")
	if !ok {
		t.Fatal("connection missing")
	}
	to, ok := view.Find("分组：target")
	if !ok {
		t.Fatal("target group missing")
	}
	view.Press(from.X+from.W/2, from.Y+from.H/2)
	view.Move(to.X+to.W/2, to.Y+to.H/2)
	view.Release(to.X+to.W/2, to.Y+to.H/2)
	if d.profiles[0].Group != "target" {
		t.Fatalf("drop did not move connection: %s", d.status)
	}
	var restored desktop
	if err := restored.loadProfiles(path); err != nil || restored.profiles[0].Group != "target" {
		t.Fatal("dragged group was not saved")
	}
}

func TestGroupStorageVersionProtectsEmptyGroupsFromOlderClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	legacy := []byte(`{"Version":1,"Items":[{"Label":"server","Group":"legacy","Host":"host.test","Port":"22","User":"alice"}],"Groups":["empty"]}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	d := &desktop{}
	if err := d.loadProfiles(path); err != nil || !d.groupExists("empty") {
		t.Fatal("version 1 groups were not preserved")
	}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored diskProfiles
	if err := json.Unmarshal(data, &stored); err != nil || stored.Version != 2 || len(stored.Groups) != 1 {
		t.Fatal("groups not protected by version 2")
	}
	var restored desktop
	if err := restored.loadProfiles(path); err != nil || !restored.groupExists("empty") || restored.profiles[0].Group != "legacy" {
		t.Fatal("version 2 data not restored")
	}
	restored.profileGroups = nil
	if err := restored.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if err := json.Unmarshal(data, &stored); err != nil || stored.Version != 2 {
		t.Fatal("storage version was downgraded")
	}
}

func TestGroupTreeCanCollapseAndExpand(t *testing.T) {
	d := &desktop{workspace: "connections", profileGroups: []string{"parent", "parent/child", "other"}}
	view := ui.NewTester(d.view, 1400, 1400)
	openConnectionNavigation(t, view)
	if !view.HasText("分组：parent/child") {
		t.Fatal("child missing")
	}
	if err := view.Click("折叠分组：parent"); err != nil {
		t.Fatal(err)
	}
	if view.HasText("分组：parent/child") || !view.HasText("分组：other") {
		t.Fatal("collapse hid wrong groups")
	}
	if err := view.Click("展开分组：parent"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("分组：parent/child") {
		t.Fatal("expand did not restore child")
	}
}

func TestGroupContextMenuOpensManagement(t *testing.T) {
	d := &desktop{workspace: "connections", profileGroups: []string{"parent", "parent/child"}}
	view := ui.NewTester(d.view, 1400, 1400)
	openConnectionNavigation(t, view)
	if err := view.RightClick("分组：parent/child"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("管理分组"); err != nil {
		t.Fatal(err)
	}
	if d.profileFilter != "parent/child" || !d.showGroupControls || !view.HasText("重命名当前分组") {
		t.Fatal("group menu did not open selected group management")
	}
}
