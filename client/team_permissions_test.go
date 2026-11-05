package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTeamMemberSelectionAndRolePermissions(t *testing.T) {
	d := &desktop{
		workspace: "cloud", showTeams: true,
		cloud:   &cloudClient{teamID: "team"},
		teams:   []cloudTeam{{ID: "team", Name: "开发", Role: "owner"}},
		members: []cloudTeamMember{{Username: "alice", Role: "owner"}, {Username: "bob", Role: "member"}, {Username: "carol", Role: "admin"}},
	}
	view := ui.NewTester(d.view, 1200, 2200)
	if !view.HasText("当前团队角色：所有者") {
		t.Fatal("current role is not visible")
	}
	if err := view.Click("bob（普通成员）"); err != nil {
		t.Fatal(err)
	}
	if d.selectedMember != "bob" || d.confirmMember != "" || !d.canManageSelectedMember() {
		t.Fatal("member selection did not enable owner actions")
	}
	if err := view.Click("移除选中成员…"); err != nil {
		t.Fatal(err)
	}
	if d.confirmMember != "bob" || !view.HasText("确认移除成员") {
		t.Fatal("removal was not explicitly confirmed")
	}
	if err := view.Click("carol（管理员）"); err != nil {
		t.Fatal(err)
	}
	if d.selectedMember != "carol" || d.confirmMember != "" {
		t.Fatal("changing selection did not clear the previous confirmation")
	}
	if err := view.Click("alice（所有者）"); err != nil {
		t.Fatal(err)
	}
	if d.canManageSelectedMember() {
		t.Fatal("owner can be changed or removed")
	}
	d.teams[0].Role = "admin"
	d.selectedMember = "bob"
	if !d.canManageInvites() || !d.canWriteCloudScope() || d.canManageSelectedMember() {
		t.Fatal("admin permissions do not match server rules")
	}
	d.teams[0].Role = "member"
	if d.canManageInvites() || d.canWriteCloudScope() || d.canManageSelectedMember() {
		t.Fatal("ordinary member was granted management controls")
	}
	view.Frame()
	if !view.HasText("当前团队为只读，或角色尚未加载；请联系管理员或刷新团队列表。") {
		t.Fatal("read-only status was not explained")
	}
	d.teams = nil
	if d.canManageInvites() || d.canManageSelectedMember() {
		t.Fatal("unknown permissions were treated as management access")
	}
	d.cloud.teamID = ""
	if !d.canWriteCloudScope() {
		t.Fatal("personal scope became read-only")
	}
}

func TestTeamRoleBecomesReadOnlyWhenRefreshFails(t *testing.T) {
	d := &desktop{workspace: "cloud", showTeams: true, cloud: &cloudClient{teamID: "team"}, teams: []cloudTeam{{ID: "team", Name: "开发", Role: "owner"}}, members: []cloudTeamMember{{Username: "bob", Role: "member"}}, selectedMember: "bob"}
	if !d.canWriteCloudScope() || !d.canManageSelectedMember() {
		t.Fatal("initial owner permissions missing")
	}
	d.finishTeamRefresh(nil, errors.New("network unavailable"))
	if !d.teamListStale || d.canWriteCloudScope() || d.canManageInvites() || d.canManageSelectedMember() {
		t.Fatal("stale team role remained trusted")
	}
	view := ui.NewTester(d.view, 1200, 2200)
	if !view.HasText("团队列表可能过期，请刷新团队后再执行管理操作。") {
		t.Fatal("stale permissions warning missing")
	}
	d.finishTeamRefresh([]cloudTeam{{ID: "team", Name: "开发", Role: "member"}}, nil)
	if d.teamListStale || d.currentTeamRole() != "member" || d.canWriteCloudScope() {
		t.Fatal("refreshed permissions were not enforced")
	}
	d.finishTeamOperation("join", "", nil, errors.New("uncertain response"), nil)
	if !d.teamListStale {
		t.Fatal("uncertain mutation left team list trusted")
	}
	d.cloud.teamID = ""
	if !d.canWriteCloudScope() {
		t.Fatal("stale team list disabled personal space")
	}
	d.clearCloudState()
	if d.teamListStale {
		t.Fatal("logout retained stale team flag")
	}
}

func TestFailedCurrentScopeRefreshDisablesWrites(t *testing.T) {
	d := &desktop{cloud: &cloudClient{teamID: "team"}, teams: []cloudTeam{{ID: "team", Role: "admin"}}}
	d.finishCloudScopeSelection("other", cloudScopeState{accountErr: errors.New("failed")})
	if d.teamListStale || d.cloudListStale {
		t.Fatal("failure in another scope invalidated current scope")
	}
	d.finishCloudScopeSelection("team", cloudScopeState{accountErr: errors.New("failed")})
	if !d.teamListStale || !d.cloudListStale || d.canWriteCloudScope() {
		t.Fatal("failed current scope refresh left writes enabled")
	}
}

func TestRevokedTeamDropsCachedScopeAndRequiresPersonalRefresh(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		d := &desktop{cloud: &cloudClient{teamID: "team"}, settingsFromCloud: true, settings: connectionSettings{Password: "secret"}, cloudFormSources: map[string]string{"team": "account"}, accounts: []cloudAccount{{ID: "account", Password: "secret"}}, selectedCloudProfile: "account", members: []cloudTeamMember{{Username: "bob"}}, confirmCloudDelete: "account", teamInviteURL: "sensitive"}
		var teams []cloudTeam
		if disabled {
			teams = []cloudTeam{{ID: "team", Disabled: true}}
		}
		d.finishTeamRefresh(teams, nil)
		if d.cloud.teamID != "" || !d.cloudListStale || len(d.accounts) != 0 || len(d.members) != 0 || d.selectedCloudProfile != "" || d.confirmCloudDelete != "" || d.teamInviteURL != "" || d.settings.Password != "" || d.settingsFromCloud {
			t.Fatalf("revoked scope remained available: %+v", d)
		}
		if d.teamListStale || !strings.Contains(d.teamStatus, "请刷新个人连接") {
			t.Fatal("recovery path not shown")
		}
	}
	d := &desktop{cloud: &cloudClient{teamID: "team"}, accounts: []cloudAccount{{ID: "old", Password: "secret"}}}
	d.finishTeamOperation("join", "", nil, nil, nil)
	if d.cloud.teamID != "" || !d.cloudListStale || len(d.accounts) != 0 {
		t.Fatal("team refresh after mutation did not clear revoked scope")
	}
}

func TestCloudRefreshFailureInvalidatesCachedTeamPermissions(t *testing.T) {
	d := &desktop{cloud: &cloudClient{teamID: "team"}, teams: []cloudTeam{{ID: "team", Role: "admin"}}, accounts: []cloudAccount{{ID: "old"}}}
	d.finishCloudRefresh(nil, errors.New("network error"))
	if !d.cloudListStale || !d.teamListStale || d.canWriteCloudScope() || len(d.accounts) != 1 {
		t.Fatal("failed refresh did not mark cached data stale")
	}
	d.finishTeamRefresh([]cloudTeam{{ID: "team", Role: "admin"}}, nil)
	if d.teamListStale || !d.cloudListStale {
		t.Fatal("team refresh incorrectly validated account list")
	}
	d.finishCloudRefresh([]cloudAccount{{ID: "fresh"}}, nil)
	if d.cloudListStale || !d.canWriteCloudScope() || len(d.accounts) != 1 || d.accounts[0].ID != "fresh" {
		t.Fatal("independent refreshes did not restore state")
	}
}
