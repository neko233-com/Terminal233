package main

import (
	"context"
	"github.com/egoist/mygo/ui"
	"path/filepath"
	"testing"
)

func TestTransferCancellationIsAvailableWhileBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &desktop{workspace: "files", busy: true, cancelTransfer: cancel}
	view := ui.NewTester(d.view, 1200, 1200)
	if err := view.Click("取消传输"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled || !view.HasText("正在取消传输") {
		t.Fatal("cancel did not reach the active transfer")
	}
}

func TestBackgroundTransferKeepsItsSessionState(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &desktop{
		workspace: "terminal", busy: true, cancelTransfer: cancel, transferSessionID: 1,
		currentSession: 1, fileDir: "/one", fileStatus: "正在传输…",
		sessions: []*workspaceSession{{id: 1, label: "one", fileDir: "/one", files: []remoteFile{{Name: "before"}}}, {id: 2, label: "two", fileDir: "/two", fileStatus: "第二个会话"}},
	}
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("two"); err != nil {
		t.Fatal(err)
	}
	if d.currentSession != 2 || d.fileStatus != "第二个会话" {
		t.Fatal("could not switch to the other session during transfer")
	}
	d.transferProgress(1, nil, "/one", 2048)
	if d.sessions[0].fileStatus != "正在传输，已读取 2.0 KiB" || d.fileStatus != "第二个会话" {
		t.Fatal("background progress changed the wrong session")
	}
	d.finishTransfer(1, nil, "/one", true, nil)
	if d.busy || d.cancelTransfer != nil || d.sessions[0].fileStatus != "传输完成" || !d.sessions[0].refreshPending || d.fileStatus != "第二个会话" {
		t.Fatal("background result did not remain with its original session")
	}
	d.activateSession(1)
	if d.fileStatus != "传输完成" || d.files != nil {
		t.Fatal("returning to the transfer session lost its result or retained stale files")
	}
}

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

func TestConnectionListShowsCurrentCloudScopeWithoutTrustingIt(t *testing.T) {
	d := &desktop{cloud: &cloudClient{teamID: "team-1"}, teams: []cloudTeam{{ID: "team-1", Name: "研发"}}, accounts: []cloudAccount{{ID: "cloud-1", Label: "测试机", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:cloud"}, {ID: "cloud-2", Label: "Alpha", Host: "other.test", Port: 22, Username: "bob"}}}
	view := ui.NewTester(d.view, 1200, 1400)
	if !view.HasText("云端连接 · 研发") || !view.HasText("全部连接  3") || !view.HasText("测试机 · alice@example.test") {
		t.Fatalf("cloud connection missing from workspace: %v", view.Texts())
	}
	if err := view.Click("名称排序"); err != nil {
		t.Fatal(err)
	}
	a, okA := view.Find("Alpha · bob@other.test")
	b, okB := view.Find("测试机 · alice@example.test")
	if !okA || !okB || a.Y >= b.Y || d.accounts[0].ID != "cloud-1" {
		t.Fatal("cloud sorting changed stored order or displayed incorrect order")
	}
	d.profileSearch = "missing"
	view.Frame()
	if view.HasText("测试机 · alice@example.test") || !view.HasText("没有匹配的连接") {
		t.Fatal("cloud connection did not follow search")
	}
	d.profileSearch = "example.test"
	view.Frame()
	if err := view.Click("测试机 · alice@example.test"); err != nil {
		t.Fatal(err)
	}
	if d.settings.Host != "example.test" || d.fingerprint != "" || d.expectedFingerprint != "SHA256:cloud" || d.trustedProfileFingerprint() != "" {
		t.Fatal("cloud connection was not loaded safely")
	}
}

func TestSwitchCloudScopeClearsOldTeamActions(t *testing.T) {
	d := &desktop{workspace: "cloud", showTeams: true, cloud: &cloudClient{teamID: "old"}, teams: []cloudTeam{{ID: "old", Name: "旧团队"}, {ID: "new", Name: "新团队", Role: "admin"}}, teamInviteURL: "https://example.test/#join=secret", confirmMember: "alice", confirmInvite: "invite-old", confirmCloudDelete: "account-old", teamInvites: []cloudInvite{{ID: "invite-old"}}}
	d.applyCloudScope("new", []cloudAccount{{ID: "account-new"}}, []cloudTeamMember{{Username: "bob"}})
	if d.cloud.teamID != "new" || len(d.accounts) != 1 || d.accounts[0].ID != "account-new" || len(d.members) != 1 || d.members[0].Username != "bob" {
		t.Fatal("scope did not switch to the new team's data")
	}
	if d.confirmMember != "" || d.confirmInvite != "" || d.confirmCloudDelete != "" || d.teamInviteURL != "" || len(d.teamInvites) != 0 {
		t.Fatal("previous team's actions or invite survived the scope change")
	}
	view := ui.NewTester(d.view, 1200, 2200)
	if !view.HasText("当前同步空间：新团队") || !view.HasText("新团队（admin） · 当前") || view.HasText("https://example.test/#join=secret") {
		t.Fatalf("scope is not clearly presented: %v", view.Texts())
	}
	d.applyCloudScope("", nil, nil)
	if d.cloudScopeName() != "个人空间" {
		t.Fatal("personal scope was not identified")
	}
}

func TestConnectionSourcesFilterLocalAndCloud(t *testing.T) {
	d := &desktop{cloud: &cloudClient{}, profiles: []profile{{ID: "local", Label: "本地测试机", Settings: connectionSettings{Host: "local.example", Port: "22", User: "a"}}}, accounts: []cloudAccount{{ID: "cloud", Label: "云端测试机", Host: "cloud.example", Port: 22, Username: "b"}}}
	view := ui.NewTester(d.view, 1200, 2200)
	if err := view.Click("本地仓库"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("本地测试机") || view.HasText("云端测试机 · b@cloud.example") || !view.HasText("复制地址：本地测试机") {
		t.Fatal("local source did not restrict the visible connections")
	}
	if err := view.Click("云端仓库"); err != nil {
		t.Fatal(err)
	}
	if view.HasText("本地测试机") || !view.HasText("云端测试机 · b@cloud.example") || !view.HasText("复制云端地址：云端测试机") {
		t.Fatal("cloud source did not restrict the visible connections")
	}
	if err := view.Click("全部来源"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("本地测试机") || !view.HasText("云端测试机 · b@cloud.example") {
		t.Fatal("all sources did not restore both lists")
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
	if err := test.ChooseMenuItem("移除…"); err != nil {
		t.Fatal(err)
	}
	if len(d.profiles) != 2 || !test.HasText("确定移除连接：alice@one.test？") {
		t.Fatal("remove was not awaiting confirmation")
	}
	if err := test.Click("取消移除"); err != nil {
		t.Fatal(err)
	}
	if len(d.profiles) != 2 || d.confirmProfileDelete != "" {
		t.Fatal("cancel did not preserve the connection")
	}
	if err := test.RightClick("工作 / alice@one.test"); err != nil {
		t.Fatal(err)
	}
	if err := test.ChooseMenuItem("移除…"); err != nil {
		t.Fatal(err)
	}
	if err := test.Click("确认移除连接"); err != nil {
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

func TestConnectionActionRequestsMissingPassword(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "example.test", Port: "22", User: "alice"}, active: connectionSettings{Host: "example.test", Port: "22", User: "alice", Fingerprint: "SHA256:trusted"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1200, 1400)
	if err := view.Click("连接：alice@example.test"); err != nil {
		t.Fatal(err)
	}
	if !d.editConnection || d.fingerprint != "" || d.expectedFingerprint != "SHA256:trusted" || !view.HasText("使用已信任指纹连接") {
		t.Fatal("password connection did not open the verified reconnect form")
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
	if err := test.Click("全部连接  3"); err != nil {
		t.Fatal(err)
	}
	if !test.HasText("个人 / alice@two.test") {
		t.Fatal("all-connections navigation failed")
	}
}

func TestBuiltinLocalTerminalIsDiscoverableAndFiltered(t *testing.T) {
	d := &desktop{}
	view := ui.NewTester(d.view, 1200, 900)
	if !view.HasText("内置本机连接") || !view.HasText("打开本机终端") {
		t.Fatal("built-in local terminal missing from the connection list")
	}
	if !view.HasText("全部连接  1") {
		t.Fatal("built-in terminal is not counted in all connections")
	}
	d.profileSearch = "powershell"
	view.Frame()
	if !view.HasText("内置本机连接") {
		t.Fatal("local terminal was not found by search")
	}
	d.profileSearch = "remote-only"
	view.Frame()
	if view.HasText("内置本机连接") {
		t.Fatal("local terminal did not follow search")
	}
	d.profileSearch, d.profileFavorites = "", true
	view.Frame()
	if view.HasText("内置本机连接") {
		t.Fatal("built-in item appeared in the favorites filter")
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
	if err := test.Click("全部连接  3"); err != nil {
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

func TestConnectionSortingPreservesCustomOrder(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "z.example", Port: "22", User: "alice"}, profileLabel: "Bravo"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.selectedProfile, d.profileLabel, d.settings.Host = "", "Alpha", "a.example"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1200, 1400)
	assertOrder := func(first, second string) {
		t.Helper()
		a, okA := view.Find(first)
		b, okB := view.Find(second)
		if !okA || !okB || a.Y >= b.Y {
			t.Fatalf("unexpected displayed order %s then %s: %+v %+v", first, second, a, b)
		}
	}
	assertOrder("Bravo", "Alpha")
	if err := view.Click("名称排序"); err != nil {
		t.Fatal(err)
	}
	assertOrder("Alpha", "Bravo")
	if err := view.Click("名称排序 ↑"); err != nil {
		t.Fatal(err)
	}
	assertOrder("Bravo", "Alpha")
	if err := view.Click("地址排序"); err != nil {
		t.Fatal(err)
	}
	assertOrder("Alpha", "Bravo")
	if d.profiles[0].Label != "Bravo" {
		t.Fatal("sorting changed the stored custom order")
	}
	if err := view.Click("自定义顺序"); err != nil {
		t.Fatal(err)
	}
	assertOrder("Bravo", "Alpha")
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

func TestCollapsedSidebarKeepsKeyboardNavigation(t *testing.T) {
	d := &desktop{}
	test := ui.NewTester(d.view, 760, 700)
	if err := test.Click("收起侧栏"); err != nil {
		t.Fatal(err)
	}
	if !d.sidebarCollapsed || test.HasText("工作区") || !test.HasText("展开侧栏") {
		t.Fatal("sidebar did not collapse")
	}
	test.Key(ui.Cmd|ui.Shift, ui.Key4)
	if d.workspace != "cloud" || !test.HasText("云端工作区") {
		t.Fatal("keyboard navigation failed with collapsed sidebar")
	}
	if err := test.Click("展开侧栏"); err != nil {
		t.Fatal(err)
	}
	if d.sidebarCollapsed || !test.HasText("工作区") {
		t.Fatal("sidebar did not reopen")
	}
}

func TestNarrowWorkspaceActionsRemainVisible(t *testing.T) {
	for _, tc := range []struct {
		workspace string
		labels    []string
	}{
		{"cloud", []string{"登录同步服务", "注册并登录", "退出同步服务", "同步当前密码连接", "刷新云端连接", "删除选中云端连接（需确认）"}},
		{"files", []string{"上传本地文件", "下载选中文件", "删除选中文件（需确认）"}},
	} {
		t.Run(tc.workspace, func(t *testing.T) {
			d := &desktop{workspace: tc.workspace}
			view := ui.NewTester(d.view, 640, 1800)
			for _, label := range tc.labels {
				r, ok := view.Find(label)
				if !ok || r.X < 0 || r.X+r.W > 640 {
					t.Errorf("action %q is outside the window: found=%t bounds=%+v", label, ok, r)
				}
			}
		})
	}
}

func TestNarrowConnectionListKeepsNameAndAddress(t *testing.T) {
	d := &desktop{profileLabel: "测试服务器", settings: connectionSettings{Host: "example.test", Port: "22", User: "alice"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 640, 700)
	for _, label := range []string{"测试服务器 · example.test:22", "编辑"} {
		r, ok := view.Find(label)
		if !ok || r.X < 0 || r.X+r.W > 640 {
			t.Errorf("connection control %q is outside the window: found=%t bounds=%+v", label, ok, r)
		}
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
