package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCloudGroupNavigationAndScopeIsolation(t *testing.T) {
	c := &cloudClient{token: "session", cloudGroups: true}
	d := &desktop{workspace: "connections", profileSource: "cloud", cloud: c, accounts: []cloudAccount{{ID: "one", Label: "server", Host: "one.test", Username: "alice"}, {ID: "two", Label: "other", Host: "two.test", Username: "alice"}}}
	state := cloudGroupSnapshot{Groups: []string{"work", "work/prod", "empty"}, Assignments: map[string]string{"one": "work/prod"}, AccountIDs: []string{"one", "two"}, Revision: strings.Repeat("a", 64)}
	d.setCloudGroupState(state, nil)
	if d.cloudGroupsStale {
		t.Fatal("valid group snapshot rejected")
	}
	view := ui.NewTester(d.view, 1400, 1400)
	if !view.HasText("云端分组：work/prod") {
		t.Fatal("cloud group missing")
	}
	if err := view.Click("云端分组：work"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("server · alice@one.test") || view.HasText("other · alice@two.test") {
		t.Fatal("cloud group filter did not include only descendants")
	}
	if err := view.Click("分组管理"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("创建云端子分组") {
		t.Fatal("cloud group controls missing")
	}
	d.applyCloudScope("new", nil, nil)
	if d.profileFilter != "" || !d.cloudGroupsStale || len(d.cloudGroupSnapshot.Groups) != 0 {
		t.Fatal("group state leaked across scopes")
	}
	d.profileSource = "cloud"
	d.profileFilter = "previous"
	d.clearCloudState()
	if d.profileFilter != "" || d.profileSource != "" {
		t.Fatal("cloud selection persisted after logout")
	}
}

func TestCloudGroupRejectsInconsistentAccountSnapshot(t *testing.T) {
	d := &desktop{cloud: &cloudClient{cloudGroups: true}, accounts: []cloudAccount{{ID: "one"}}}
	state := cloudGroupSnapshot{Groups: []string{}, Assignments: map[string]string{}, AccountIDs: []string{"two"}, Revision: strings.Repeat("a", 64)}
	d.setCloudGroupState(state, nil)
	if !d.cloudGroupsStale || len(d.cloudGroupSnapshot.AccountIDs) != 0 {
		t.Fatal("inconsistent account and group snapshots accepted")
	}
}
