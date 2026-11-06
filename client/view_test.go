package main

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func precedesInGrid(a, b ui.Rect) bool {
	return a.Y < b.Y || (a.Y == b.Y && a.X < b.X)
}

func TestTransferErrorsAndLocalRefresh(t *testing.T) {
	exists := &os.PathError{Op: "open", Path: "existing", Err: os.ErrExist}
	if got := transferErrorText(exists); got != "目标文件已存在，不会覆盖" {
		t.Fatalf("unexpected existing-file message: %s", got)
	}
	cleanup := fmt.Errorf("清理目标文件失败：%w", &os.PathError{Op: "remove", Path: "target", Err: os.ErrPermission})
	if got := transferErrorText(cleanup); !strings.Contains(got, "清理目标文件失败") {
		t.Fatal("cleanup failure context was lost")
	}
	dir := t.TempDir()
	if !transferUpdatesDirectory(nil, dir, filepath.Join(dir, "copy"), false) || transferUpdatesDirectory(nil, dir, filepath.Join(dir, "other", "copy"), false) || transferUpdatesDirectory(&remoteShell{}, dir, filepath.Join(dir, "copy"), false) || !transferUpdatesDirectory(&remoteShell{}, dir, "source", true) {
		t.Fatal("file view refresh did not match the changed directory")
	}
	d := &desktop{busy: true, currentSession: 2, sessions: []*workspaceSession{{id: 1, fileDir: dir, files: []remoteFile{{Name: "old"}}}, {id: 2}}}
	d.finishTransfer(1, nil, dir, true, nil)
	if !d.sessions[0].refreshPending || d.sessions[0].files != nil {
		t.Fatal("background copy did not mark its source session for refresh")
	}
}

func TestUpdateAvailabilityIsVisibleInWorkspace(t *testing.T) {
	d := &desktop{updateAvailable: &updateCandidate{Version: "1.2.3"}, updateStatus: "发现新版 1.2.3；Windows 安装包未签名。"}
	view := ui.NewTester(d.view, 1200, 1600)
	if err := view.Click("设置（有更新）"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("检查更新") || !view.HasText("下载安装包…") || !view.HasText(d.updateStatus) {
		t.Fatal("update status or download action is missing")
	}
}

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
	d := &desktop{cloud: &cloudClient{}, settings: connectionSettings{Port: "22"}, accounts: []cloudAccount{{ID: "test", Label: "示例服务器", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:old"}}}
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
	openConnectionNavigation(t, view)
	if !view.HasText("云端连接 · 研发") || !view.HasText("全部连接  3") || !view.HasText("测试机 · alice@example.test") {
		t.Fatalf("cloud connection missing from workspace: %v", view.Texts())
	}
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("名称排序"); err != nil {
		t.Fatal(err)
	}
	a, okA := view.Find("Alpha · bob@other.test")
	b, okB := view.Find("测试机 · alice@example.test")
	if !okA || !okB || !precedesInGrid(a, b) || d.accounts[0].ID != "cloud-1" {
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
	if d.selectedCloudProfile != "cloud-1" || d.editConnection || d.settings.Password != "" {
		t.Fatal("single click did not safely select cloud connection")
	}
	if err := view.Click("测试机 · alice@example.test"); err != nil {
		t.Fatal(err)
	}
	if d.settings.Host != "example.test" || d.fingerprint != "" || d.expectedFingerprint != "SHA256:cloud" || d.trustedProfileFingerprint() != "" {
		t.Fatal("cloud connection was not loaded safely")
	}
}

func TestSwitchCloudScopeClearsOldTeamActions(t *testing.T) {
	d := &desktop{workspace: "cloud", showTeams: true, selectedCloudProfile: "account-old", cloud: &cloudClient{teamID: "old"}, teams: []cloudTeam{{ID: "old", Name: "旧团队"}, {ID: "new", Name: "新团队", Role: "admin"}}, teamInviteURL: "https://example.test/#join=secret", confirmMember: "alice", confirmInvite: "invite-old", confirmCloudDelete: "account-old", teamInvites: []cloudInvite{{ID: "invite-old"}}}
	d.applyCloudScope("new", []cloudAccount{{ID: "account-new"}}, []cloudTeamMember{{Username: "bob"}})
	if d.cloud.teamID != "new" || len(d.accounts) != 1 || d.accounts[0].ID != "account-new" || len(d.members) != 1 || d.members[0].Username != "bob" {
		t.Fatal("scope did not switch to the new team's data")
	}
	if d.confirmMember != "" || d.confirmInvite != "" || d.confirmCloudDelete != "" || d.teamInviteURL != "" || d.selectedCloudProfile != "" || len(d.teamInvites) != 0 {
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

func TestCloudSessionCleanupClearsSensitiveAndPendingState(t *testing.T) {
	client := &cloudClient{token: "session-secret", adminTOTP: "123456", teamID: "team"}
	d := &desktop{cloud: client, profileSource: "cloud", cloudPassword: "login-secret", cloudRegistration: "registration-secret", teamInviteURL: "https://example.test/#join=secret", teamInviteInput: "invite-secret", teamInvitePassword: "invite-password", confirmMember: "alice", confirmInvite: "invite", confirmCloudDelete: "account", adminTOTP: "123456", adminConfirmUser: "alice", adminConfirmTeam: "team", adminConfirmRestore: true, accounts: []cloudAccount{{ID: "account"}}, teams: []cloudTeam{{ID: "team"}}, members: []cloudTeamMember{{Username: "alice"}}, teamInvites: []cloudInvite{{ID: "invite"}}, adminUsers: []cloudAdminUser{{Username: "alice"}}, adminTeams: []cloudTeam{{ID: "team"}}}
	d.loadCloudAccount(cloudAccount{Host: "example.test", Port: 22, Username: "alice", Password: "ssh-secret"})
	d.clearCloudState()
	if d.cloud != nil || client.token != "" || client.adminTOTP != "" || d.settings.Password != "" || d.settingsFromCloud || d.cloudPassword != "" || d.cloudRegistration != "" || d.teamInviteURL != "" || d.teamInviteInput != "" || d.teamInvitePassword != "" || d.adminTOTP != "" {
		t.Fatal("session cleanup retained cloud secrets")
	}
	if d.profileSource != "" || d.confirmMember != "" || d.confirmInvite != "" || d.confirmCloudDelete != "" || d.adminConfirmUser != "" || d.adminConfirmTeam != "" || d.adminConfirmRestore || len(d.accounts)+len(d.teams)+len(d.members)+len(d.teamInvites)+len(d.adminUsers)+len(d.adminTeams) != 0 {
		t.Fatal("session cleanup retained stale cloud data or actions")
	}
	local := &desktop{settings: connectionSettings{Password: "unsaved-local-password"}}
	local.clearCloudState()
	if local.settings.Password != "unsaved-local-password" {
		t.Fatal("cleanup altered an unrelated local form")
	}
}

func TestChangedHostKeyRequiresExplicitVerification(t *testing.T) {
	settings := connectionSettings{Host: "example.test", Port: "22", User: "alice"}
	d := &desktop{workspace: "connections", editConnection: true, settings: settings, expectedFingerprint: "SHA256:old"}
	d.finishHostInspection(settings, "SHA256:new", nil)
	if d.fingerprint != "" || d.changedFingerprint != "SHA256:new" || d.expectedFingerprint != "SHA256:old" {
		t.Fatal("changed host key was accepted without verification")
	}
	view := ui.NewTester(d.view, 1200, 1800)
	if !view.HasText("原指纹：SHA256:old") || !view.HasText("新指纹：SHA256:new") {
		t.Fatal("both fingerprints must be visible for comparison")
	}
	if err := view.Click("连接并核验主机指纹"); err != nil {
		t.Fatal(err)
	}
	if d.busy || d.fingerprint != "" || d.changedFingerprint != "SHA256:new" {
		t.Fatal("primary connection action must remain disabled until the changed host key is confirmed")
	}
	if err := view.Click("已核实，使用新指纹"); err != nil {
		t.Fatal(err)
	}
	if d.fingerprint != "SHA256:new" || d.changedFingerprint != "" || d.expectedFingerprint != "SHA256:old" {
		t.Fatal("verification did not stage the new key without overwriting the stored key")
	}
	other := settings
	other.Host = "other.test"
	d.fingerprint = ""
	d.finishHostInspection(other, "SHA256:unrelated", nil)
	if d.fingerprint != "" || d.changedFingerprint != "" {
		t.Fatal("stale inspection result was accepted")
	}
}

func TestConnectionFormNamesCloudSyncDestination(t *testing.T) {
	d := &desktop{workspace: "connections", editConnection: true, cloud: &cloudClient{teamID: "team"}, teams: []cloudTeam{{ID: "team", Name: "研发"}}, settings: connectionSettings{Host: "example.test", Port: "22", User: "alice", Password: "secret"}, fingerprint: "SHA256:verified"}
	view := ui.NewTester(d.view, 1200, 1800)
	if !view.HasText("同步目标：研发；团队空间的成员可访问共享连接。") || !view.HasText("确认指纹并同步密码连接") {
		t.Fatal("connection form did not clearly identify the sync destination")
	}
}

func TestConnectionSourcesFilterLocalAndCloud(t *testing.T) {
	d := &desktop{cloud: &cloudClient{}, profiles: []profile{{ID: "local", Label: "本地测试机", Settings: connectionSettings{Host: "local.example", Port: "22", User: "a"}}}, accounts: []cloudAccount{{ID: "cloud", Label: "云端测试机", Host: "cloud.example", Port: 22, Username: "b"}}}
	view := ui.NewTester(d.view, 1200, 2200)
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("本地仓库"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("本地测试机") || view.HasText("云端测试机 · b@cloud.example") {
		t.Fatal("local source did not restrict the visible connections")
	}
	if err := view.Click("收起连接操作"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("本地 · 更多") || view.HasText("云端测试机 · b@cloud.example") {
		t.Fatal("collapsed tools should indicate and preserve the selected source")
	}
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("云端仓库"); err != nil {
		t.Fatal(err)
	}
	if view.HasText("本地测试机") || !view.HasText("云端测试机 · b@cloud.example") {
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

func TestConnectionRowSelectionAndDoubleClick(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host, d.selectedProfile = "two.test", ""
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.selectedProfile = ""
	view := ui.NewTester(d.view, 1200, 1400)
	if err := view.Click("alice@one.test"); err != nil {
		t.Fatal(err)
	}
	if d.selectedProfile != "1" || d.editConnection {
		t.Fatal("single click did not select without leaving list")
	}
	if err := view.Click("alice@one.test"); err != nil {
		t.Fatal(err)
	}
	if !d.editConnection || d.settings.Host != "one.test" || d.fingerprint != "" {
		t.Fatal("double click did not open safe connection form")
	}
	d.editConnection = false
	view.Frame()
	if err := view.RightClick("alice@two.test"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("连接"); err != nil {
		t.Fatal(err)
	}
	if !d.editConnection || d.settings.Host != "two.test" || d.fingerprint != "" {
		t.Fatal("context connection did not open safe form")
	}
}

func TestConnectionActionRequestsMissingPassword(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "example.test", Port: "22", User: "alice"}, active: connectionSettings{Host: "example.test", Port: "22", User: "alice", Fingerprint: "SHA256:trusted"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1200, 1400)
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
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
	openConnectionNavigation(t, test)
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
	openConnectionNavigation(t, view)
	if !view.HasText("打开本机终端") {
		t.Fatal("built-in local terminal missing from the connection list")
	}
	if !view.HasText("全部连接  1") {
		t.Fatal("built-in terminal is not counted in all connections")
	}
	d.profileSearch = "powershell"
	view.Frame()
	if !view.HasText("打开本机终端") {
		t.Fatal("local terminal was not found by search")
	}
	d.profileSearch = "remote-only"
	view.Frame()
	if view.HasText("打开本机终端") {
		t.Fatal("local terminal did not follow search")
	}
	d.profileSearch, d.profileFavorites = "", true
	view.Frame()
	if view.HasText("打开本机终端") {
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
	openConnectionNavigation(t, test)
	if err := test.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
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
	openConnectionNavigation(t, test)
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
	if !precedesInGrid(first, second) {
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
		if !okA || !okB || !precedesInGrid(a, b) {
			t.Fatalf("unexpected displayed order %s then %s: %+v %+v", first, second, a, b)
		}
	}
	assertOrder("Bravo", "Alpha")
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
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
		loggedIn  bool
		labels    []string
	}{
		{"cloud", false, []string{"登录同步服务", "注册并登录"}},
		{"cloud", true, []string{"退出同步服务", "同步当前密码连接", "刷新云端连接", "删除选中云端连接（需确认）"}},
		{"files", false, []string{"上传本地文件", "下载选中文件", "删除选中文件（需确认）"}},
	} {
		t.Run(fmt.Sprintf("%s-%t", tc.workspace, tc.loggedIn), func(t *testing.T) {
			d := &desktop{workspace: tc.workspace}
			if tc.loggedIn {
				d.cloud = &cloudClient{}
			}
			if tc.workspace == "files" {
				d.files = []remoteFile{{Name: "example.txt"}}
				d.showFileTools = true
			}
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
	if !precedesInGrid(first, second) {
		t.Fatalf("view did not render new order: %v %v", first, second)
	}
}

func TestDroppedFilesWithoutConnectionGivesFeedback(t *testing.T) {
	d := &desktop{settings: connectionSettings{Port: "22"}}
	test := ui.NewTester(d.view, 1200, 2200)
	if err := test.Click("远程文件"); err != nil {
		t.Fatal(err)
	}
	zone, ok := test.Find("拖入文件上传（最多 100 个）")
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

func TestSplitSessionSwitchAndClosePreservesIndependentState(t *testing.T) {
	d := &desktop{workspace: "terminal", currentSession: 1, splitSession: 2, splitFirstSession: 1, fileDir: "/one", sessions: []*workspaceSession{
		{id: 1, label: "one", fileDir: "/one"},
		{id: 2, label: "two", fileDir: "/two"},
		{id: 3, label: "three", fileDir: "/three"},
	}}
	d.activateSession(2)
	if d.currentSession != 2 || d.splitSession != 1 || d.splitFirstSession != 1 || d.fileDir != "/two" {
		t.Fatal("switching to the second pane lost split or session state")
	}
	d.activateSession(3)
	if d.currentSession != 3 || d.splitSession != 1 || d.splitFirstSession != 1 || d.fileDir != "/three" {
		t.Fatal("opening another tab changed the pinned pane")
	}
	d.closeSession(1)
	if d.splitSession != 0 || d.splitFirstSession != 0 || d.currentSession != 3 || d.fileDir != "/three" {
		t.Fatal("closing the split pane changed the active session")
	}
}

func TestAdjacentSessionSelectionWrapsAndPreservesSessionState(t *testing.T) {
	d := &desktop{workspace: "terminal", currentSession: 2, fileDir: "/two", sessions: []*workspaceSession{
		{id: 1, label: "one", fileDir: "/one"},
		{id: 2, label: "two", fileDir: "/two"},
		{id: 3, label: "three", fileDir: "/three"},
	}}
	d.selectAdjacentSession(1)
	if d.currentSession != 3 || d.fileDir != "/three" {
		t.Fatal("next session state was not restored")
	}
	d.selectAdjacentSession(1)
	if d.currentSession != 1 || d.fileDir != "/one" {
		t.Fatal("next session did not wrap")
	}
	d.selectAdjacentSession(-1)
	if d.currentSession != 3 || d.fileDir != "/three" {
		t.Fatal("previous session did not wrap")
	}
	d.busy = true
	d.selectAdjacentSession(-1)
	if d.currentSession != 3 {
		t.Fatal("busy operation did not prevent switching")
	}
}

func TestFileSortFilterAndHiddenSelection(t *testing.T) {
	files := []remoteFile{{Name: "large", Size: 200, Modified: 10}, {Name: ".hidden", Size: 20, Modified: 30}, {Name: "folder", IsDir: true}, {Name: "small", Size: 3, Modified: 20}}
	d := &desktop{workspace: "files", files: files, selectedFile: ".hidden", fileRename: ".hidden"}
	view := ui.NewTester(d.view, 1200, 1500)
	if err := view.Click("排序与显示"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("隐藏点文件"); err != nil {
		t.Fatal(err)
	}
	if d.selectedFile != "" || d.fileRename != "" || view.HasText(".hidden") {
		t.Fatal("hidden selected file remained actionable or visible")
	}
	if err := view.Click("按大小"); err != nil {
		t.Fatal(err)
	}
	a, oka := view.Find("small")
	b, okb := view.Find("large")
	folder, okc := view.Find("📁 folder")
	if !oka || !okb || !okc || folder.Y >= a.Y || a.Y >= b.Y {
		t.Fatal("size order or directory precedence is wrong")
	}
	if err := view.Click("按大小 ↑"); err != nil {
		t.Fatal(err)
	}
	a, _ = view.Find("small")
	b, _ = view.Find("large")
	if b.Y >= a.Y {
		t.Fatal("descending size order is wrong")
	}
	if files[0].Name != "large" {
		t.Fatal("sorting mutated the source snapshot")
	}
}

func TestFileSortingUnknownValuesAndSessionState(t *testing.T) {
	items := []remoteFile{{Name: "unknown", Size: -1}, {Name: "small", Size: 1}, {Name: "large", Size: 9}, {Name: "dir", IsDir: true}}
	for _, descending := range []bool{false, true} {
		got := displayedFiles(items, "", "size", descending, false)
		if got[0].Name != "dir" || got[len(got)-1].Name != "unknown" {
			t.Fatalf("invalid order: %+v", got)
		}
	}
	d := &desktop{currentSession: 1, fileSort: "size", fileSortDescending: true, fileHideDotfiles: true, sessions: []*workspaceSession{{id: 1}, {id: 2, fileSort: "time"}}}
	d.activateSession(2)
	if d.fileSort != "time" || d.fileSortDescending || d.fileHideDotfiles {
		t.Fatal("other session state not restored")
	}
	d.activateSession(1)
	if d.fileSort != "size" || !d.fileSortDescending || !d.fileHideDotfiles {
		t.Fatal("first session state not restored")
	}
}

func TestFileWorkspaceIdentifiesCurrentSession(t *testing.T) {
	d := &desktop{workspace: "files", currentSession: 2, sessions: []*workspaceSession{{id: 1, label: "server one"}, {id: 2, label: "server two"}}}
	view := ui.NewTester(d.view, 1200, 1200)
	if !view.HasText("所属会话：server two") || view.HasText("所属会话：server one") {
		t.Fatal("file workspace identifies wrong session")
	}
}

func TestFileListPrecedesControlsAndRemainsBounded(t *testing.T) {
	files := make([]remoteFile, 30)
	for i := range files {
		files[i] = remoteFile{Name: fmt.Sprintf("file-%02d", i), Size: int64(i)}
	}
	d := &desktop{workspace: "files", files: files, showFileTools: true}
	view := ui.NewTester(d.view, 1200, 1500)
	first, ok := view.Find("file-00")
	controls, hasControls := view.Find("本地文件路径")
	if !ok || !hasControls || first.Y >= controls.Y {
		t.Fatal("file list must precede transfer controls")
	}
	if controls.Y-first.Y > 440 {
		t.Fatalf("large directory pushed controls too far away: %v", controls.Y-first.Y)
	}
}

func TestCloudUpdateUsesOnlyExplicitSourceAndScope(t *testing.T) {
	account := cloudAccount{ID: "one", Label: "已有连接", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:key"}
	d := &desktop{workspace: "connections", editConnection: true, cloud: &cloudClient{teamID: "team-one"}, accounts: []cloudAccount{account}}
	d.loadCloudAccount(account)
	d.fingerprint = "SHA256:key"
	view := ui.NewTester(d.view, 1200, 1600)
	if !view.HasText("将更新已载入的云端连接；其他相同地址的连接不会被修改。") {
		t.Fatal("explicit update target is unclear")
	}
	d.cloud.teamID = "team-two"
	view.Frame()
	if !view.HasText("将新建云端连接；相同地址不会自动覆盖。") {
		t.Fatal("cross-team save did not switch to create")
	}
	d.currentSession = 1
	d.sessions = []*workspaceSession{{id: 1, cloudSources: map[string]string{"team-one": "one"}}}
	if d.currentCloudSource() != "" {
		t.Fatal("other team's source leaked into current scope")
	}
	d.cloud.teamID = "team-one"
	if d.currentCloudSource() != "one" {
		t.Fatal("explicit session source was lost")
	}
	d.clearCloudState()
	if len(d.sessions[0].cloudSources) != 0 || len(d.cloudFormSources) != 0 {
		t.Fatal("cloud source survived logout")
	}
}
