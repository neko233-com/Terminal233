package main

import (
	"github.com/egoist/mygo/ui"
	"path/filepath"
	"testing"
)

func TestNativeViewLoadsSyncedAccountWithoutTrustingIt(t *testing.T) {
	d := &desktop{settings: connectionSettings{Port: "22"}, accounts: []cloudAccount{{ID: "test", Label: "示例服务器", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:old"}}}
	test := ui.NewTester(d.view, 1200, 3200)
	if !test.HasText("SSH 连接") || !test.HasText("远程文件") || !test.HasText("连接列表") {
		t.Fatalf("missing native controls: %v", test.Texts())
	}
	if err := test.Click("云同步与团队"); err != nil {
		t.Fatal(err)
	}
	if err := test.Click("示例服务器"); err != nil {
		t.Fatal(err)
	}
	if d.settings.Host != "example.test" || d.fingerprint != "" || d.expectedFingerprint != "SHA256:old" {
		t.Fatalf("loaded unsafe state: %+v", d.settings)
	}
}

func TestConnectionListSearchAndContextMenu(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice", Password: "private"}, profileGroup: "工作"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host = "two.test"
	d.selectedProfile = ""
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	test := ui.NewTester(d.view, 1200, 2200)
	if !test.HasText("工作 / alice@one.test") || !test.HasText("工作 / alice@two.test") {
		t.Fatalf("missing profiles: %v", test.Texts())
	}
	if err := test.RightClick("工作 / alice@one.test"); err != nil {
		t.Fatal(err)
	}
	if err := test.ChooseMenuItem("移除"); err != nil {
		t.Fatal(err)
	}
	if len(d.profiles) != 1 || d.profiles[0].Settings.Host != "two.test" {
		t.Fatalf("remove failed: %+v", d.profiles)
	}
	d.profileSearch = "not-found"
	test.Frame()
	if test.HasText("工作 / alice@two.test") {
		t.Fatal("search did not filter profile")
	}
}

func TestConnectionGroupNavigation(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}, profileGroup: "工作"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host, d.profileGroup = "two.test", "个人"
	d.selectedProfile = ""
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	test := ui.NewTester(d.view, 1200, 1200)
	if err := test.Click("工作  1"); err != nil {
		t.Fatal(err)
	}
	if !test.HasText("工作 / alice@one.test") || test.HasText("个人 / alice@two.test") {
		t.Fatalf("group filter failed: %v", test.Texts())
	}
	if err := test.Click("全部连接  2"); err != nil {
		t.Fatal(err)
	}
	if !test.HasText("个人 / alice@two.test") {
		t.Fatal("all-connections navigation failed")
	}
}

func TestFavoritesAndCustomNameSearch(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}, profileLabel: "生产服务器"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.selectedProfile, d.profileLabel, d.settings.Host = "", "另一台", "two.test"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	test := ui.NewTester(d.view, 1200, 1200)
	if err := test.Click("加入收藏：生产服务器"); err != nil {
		t.Fatal(err)
	}
	if !d.profiles[0].Favorite {
		t.Fatal("favorite was not set")
	}
	if err := test.Click("收藏的连接  1"); err != nil {
		t.Fatal(err)
	}
	if !test.HasText("生产服务器") || test.HasText("另一台") {
		t.Fatalf("favorite filter failed: %v", test.Texts())
	}
	if err := test.Click("全部连接  2"); err != nil {
		t.Fatal(err)
	}
	d.profileSearch = "two.test"
	test.Frame()
	if !test.HasText("另一台") || test.HasText("生产服务器") {
		t.Fatalf("host search failed: %v", test.Texts())
	}
}

func TestRecentConnectionsUseSuccessfulConnectionOrder(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.profiles[0].LastConnected = 10
	d.selectedProfile, d.settings.Host = "", "two.test"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.profiles[1].LastConnected = 20
	test := ui.NewTester(d.view, 1200, 1200)
	if err := test.Click("最近连接  2"); err != nil {
		t.Fatal(err)
	}
	first, ok := test.Find("alice@two.test")
	if !ok {
		t.Fatal("latest connection missing")
	}
	second, ok := test.Find("alice@one.test")
	if !ok {
		t.Fatal("older connection missing")
	}
	if first.Y >= second.Y {
		t.Fatalf("recent connections out of order: %v %v", first, second)
	}
}

func TestThemeSwitchKeepsWorkspaceUsable(t *testing.T) {
	d := &desktop{}
	test := ui.NewTester(d.view, 1200, 900)
	if err := test.Click("浅色"); err != nil {
		t.Fatal(err)
	}
	if !d.lightTheme || !test.HasText("深色") || !test.HasText("连接列表") {
		t.Fatal("light theme did not keep the workspace available")
	}
	if err := test.Click("深色"); err != nil {
		t.Fatal(err)
	}
	if d.lightTheme || !test.HasText("浅色") {
		t.Fatal("dark theme was not restored")
	}
}

func TestWorkspaceKeyboardNavigation(t *testing.T) {
	d := &desktop{}
	test := ui.NewTester(d.view, 1200, 900)
	test.Key(ui.Cmd|ui.Shift, ui.Key3)
	if d.workspace != "files" || !test.HasText("文件管理") {
		t.Fatal("file workspace shortcut failed")
	}
	test.Key(ui.Cmd|ui.Shift, ui.Key4)
	if d.workspace != "cloud" || !test.HasText("云端工作区") {
		t.Fatal("cloud workspace shortcut failed")
	}
	test.Key(ui.Cmd|ui.Shift, ui.Key1)
	if d.workspace != "connections" || !test.HasText("连接列表") {
		t.Fatal("connection workspace shortcut failed")
	}
}

func TestReloadAfterProfileConflictRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	first := &desktop{settings: connectionSettings{Host: "first.test", Port: "22", User: "alice"}}
	second := &desktop{settings: connectionSettings{Host: "second.test", Port: "22", User: "bob"}}
	if err := first.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := second.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := first.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := first.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	if err := second.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := second.saveProfiles(); err == nil {
		t.Fatal("expected conflict")
	}
	test := ui.NewTester(second.view, 1200, 1200)
	if err := test.Click("重新载入本机连接"); err != nil {
		t.Fatal(err)
	}
	if len(second.profiles) != 1 || second.profiles[0].Settings.Host != "second.test" {
		t.Fatal("first click discarded changes")
	}
	if err := test.Click("确认放弃本窗口未保存更改并重新载入"); err != nil {
		t.Fatal(err)
	}
	if second.profileStoreConflict || len(second.profiles) != 1 || second.profiles[0].Settings.Host != "first.test" {
		t.Fatalf("reload failed: %+v", second.profiles)
	}
}

func TestConnectionListDragOrder(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host = "two.test"
	d.selectedProfile = ""
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	test := ui.NewTester(d.view, 1200, 2200)
	from, ok := test.Find("alice@two.test")
	if !ok {
		t.Fatal("source not found")
	}
	to, ok := test.Find("alice@one.test")
	if !ok {
		t.Fatal("target not found")
	}
	test.Press(from.X+from.W/2, from.Y+from.H/2)
	test.Move(to.X+to.W/2, to.Y+to.H/2)
	test.Release(to.X+to.W/2, to.Y+to.H/2)
	if d.profiles[0].Settings.Host != "two.test" {
		t.Fatalf("drag did not reorder: %+v", d.profiles)
	}
	test.Frame()
	first, _ := test.Find("alice@two.test")
	second, _ := test.Find("alice@one.test")
	if first.Y >= second.Y {
		t.Fatalf("view did not render new order: %v %v", first, second)
	}
}

func TestDroppedFilesWithoutConnectionGivesFeedback(t *testing.T) {
	d := &desktop{settings: connectionSettings{Port: "22"}}
	test := ui.NewTester(d.view, 1200, 2200)
	if err := test.Click("远程文件"); err != nil {
		t.Fatal(err)
	}
	zone, ok := test.Find("拖入一个文件上传")
	if !ok {
		t.Fatal("drop zone missing")
	}
	if !test.DropFiles(zone.X+zone.W/2, zone.Y+zone.H/2, "example.txt") {
		t.Fatal("drop not accepted")
	}
	if d.fileStatus != "请先连接后再上传" {
		t.Fatalf("unexpected feedback: %q", d.fileStatus)
	}
}

func TestFileListFiltersNamesAndDisplaysMetadata(t *testing.T) {
	d := &desktop{workspace: "files", files: []remoteFile{{Name: "report.txt", Size: 2048, Modified: 1700000000}, {Name: "archive", IsDir: true}}}
	test := ui.NewTester(d.view, 1200, 1400)
	if !test.HasText("report.txt") || !test.HasText("2.0 KiB") || !test.HasText("📁 archive") {
		t.Fatalf("file details missing: %v", test.Texts())
	}
	d.fileFilter = "REPORT"
	test.Frame()
	if !test.HasText("report.txt") || test.HasText("📁 archive") {
		t.Fatalf("file filter failed: %v", test.Texts())
	}
	d.fileFilter = "missing"
	test.Frame()
	if !test.HasText("没有匹配的文件") {
		t.Fatal("missing empty search state")
	}
}

func TestFileFilterFollowsActiveSession(t *testing.T) {
	d := &desktop{currentSession: 1, fileFilter: "alpha", sessions: []*workspaceSession{{id: 1}, {id: 2, fileFilter: "beta"}}}
	d.activateSession(2)
	if d.fileFilter != "beta" {
		t.Fatal("second session filter was not restored")
	}
	d.fileFilter = "changed"
	d.activateSession(1)
	if d.fileFilter != "alpha" {
		t.Fatal("first session filter was not restored")
	}
	d.activateSession(2)
	if d.fileFilter != "changed" {
		t.Fatal("second session filter was not retained")
	}
}
