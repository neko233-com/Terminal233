package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestProfileControlSelectionAndGroupDrag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.profileGroups = []string{"target"}
	d.profiles = []profile{{ID: "1", Label: "alpha", Settings: connectionSettings{Host: "a.test", Port: "22", User: "alice"}}, {ID: "2", Label: "beta", Settings: connectionSettings{Host: "b.test", Port: "22", User: "alice"}}, {ID: "3", Label: "gamma", Settings: connectionSettings{Host: "c.test", Port: "22", User: "alice"}}}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	v := ui.NewTester(d.view, 1400, 1000)
	if err := v.ClickWith(ui.Cmd, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := v.ClickWith(ui.Cmd, "beta"); err != nil {
		t.Fatal(err)
	}
	if !d.checkedProfiles["1"] || !d.checkedProfiles["2"] || d.checkedProfiles["3"] {
		t.Fatal("control selection failed")
	}
	from, ok := v.Find("alpha")
	if !ok {
		t.Fatal("drag source missing")
	}
	to, ok := v.Find("打开分组：target")
	if !ok {
		t.Fatal("group missing")
	}
	v.Press(from.X+from.W/2, from.Y+from.H/2)
	v.Move(to.X+to.W/2, to.Y+to.H/2)
	v.Release(to.X+to.W/2, to.Y+to.H/2)
	if d.profiles[0].Group != "target" || d.profiles[1].Group != "target" || d.profiles[2].Group != "" || len(d.checkedProfiles) != 0 {
		t.Fatalf("batch drag incorrect: %s", d.status)
	}
	var restored desktop
	if err := restored.loadProfiles(path); err != nil || restored.profiles[0].Group != "target" || restored.profiles[1].Group != "target" {
		t.Fatal("batch move did not persist", err)
	}
	d.checkedProfiles = map[profileID]bool{"1": true, "2": true}
	d.moveDraggedProfilesToGroup("3", "target")
	if d.profiles[2].Group != "target" || len(d.checkedProfiles) != 2 {
		t.Fatal("dragging unselected host changed selection")
	}
}

func TestProfileShiftSelectionUsesVisibleOrder(t *testing.T) {
	d := &desktop{workspace: "connections", profiles: []profile{{ID: "1", Label: "alpha", Settings: connectionSettings{Host: "a.test", Port: "22", User: "alice"}}, {ID: "2", Label: "beta", Settings: connectionSettings{Host: "b.test", Port: "22", User: "alice"}}, {ID: "3", Label: "gamma", Settings: connectionSettings{Host: "c.test", Port: "22", User: "alice"}}}}
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.ClickWith(ui.Cmd, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := v.ClickWith(ui.Shift, "gamma"); err != nil {
		t.Fatal(err)
	}
	if len(d.checkedProfiles) != 3 {
		t.Fatal("shift did not select the visible range")
	}
	d.profileSearch = "gamma"
	v.Frame()
	if err := v.ClickWith(ui.Cmd, "gamma"); err != nil {
		t.Fatal(err)
	}
	if !d.checkedProfiles["1"] || !d.checkedProfiles["2"] || d.checkedProfiles["3"] {
		t.Fatal("control toggle lost hidden selection")
	}
}

func TestControlDeselectDoesNotRestoreOldAnchor(t *testing.T) {
	visible := []profileID{"1", "2"}
	selected, anchor := profileModifiedSelection(map[profileID]bool{}, profileID(""), profileID("1"), visible, ui.Cmd)
	selected, anchor = profileModifiedSelection(selected, anchor, profileID("1"), visible, ui.Cmd)
	selected, _ = profileModifiedSelection(selected, anchor, profileID("2"), visible, ui.Cmd)
	if len(selected) != 1 || !selected["2"] {
		t.Fatal("deselected host was reselected")
	}
}

func TestProfileGroupBatchConflictIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	a, b := &desktop{}, &desktop{}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.profileGroups = []string{"target"}
	a.profiles = []profile{{ID: "1", Label: "one", Settings: connectionSettings{Host: "a.test", Port: "22", User: "u"}}, {ID: "2", Label: "two", Settings: connectionSettings{Host: "b.test", Port: "22", User: "u"}}}
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.profiles[0].Label = "new"
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	b.checkedProfiles = map[profileID]bool{"1": true, "2": true}
	b.moveDraggedProfilesToGroup("1", "target")
	if b.profiles[0].Group != "" || b.profiles[1].Group != "" || len(b.checkedProfiles) != 2 || !b.profileStoreConflict {
		t.Fatal("failed batch move lost data or selection")
	}
}

func TestBatchDragIncludesHiddenSelection(t *testing.T) {
	d := &desktop{workspace: "connections", profileGroups: []string{"target"}, profiles: []profile{{ID: "1", Label: "alpha", Settings: connectionSettings{Host: "a.test", Port: "22", User: "u"}}, {ID: "2", Label: "beta", Settings: connectionSettings{Host: "b.test", Port: "22", User: "u"}}}, checkedProfiles: map[profileID]bool{"1": true, "2": true}, profileSearch: "alpha", profileSort: "name"}
	v := ui.NewTester(d.view, 1400, 900)
	openConnectionNavigation(t, v)
	from, ok := v.Find("alpha")
	if !ok {
		t.Fatal("source missing")
	}
	to, ok := v.Find("分组：target")
	if !ok {
		t.Fatal("group target missing")
	}
	v.Press(from.X+from.W/2, from.Y+from.H/2)
	v.Move(to.X+to.W/2, to.Y+to.H/2)
	v.Release(to.X+to.W/2, to.Y+to.H/2)
	if d.profiles[0].Group != "target" || d.profiles[1].Group != "target" {
		t.Fatal("hidden selected host was not included")
	}
}

func TestCloudBatchMoveRejectsStaleOrMissingIDs(t *testing.T) {
	state := cloudGroupSnapshot{Groups: []string{"old", "target"}, Assignments: map[string]string{"1": "old"}, AccountIDs: []string{"1", "2"}, Revision: strings.Repeat("a", 64)}
	accounts := []cloudAccount{{ID: "1"}, {ID: "2"}}
	next, changed, err := movedCloudGroupState(state, accounts, []string{"1", "2"}, "target")
	if err != nil || changed != 2 || next.Assignments["1"] != "target" || next.Assignments["2"] != "target" || state.Assignments["1"] != "old" {
		t.Fatal("cloud move not isolated", err)
	}
	if _, _, err = movedCloudGroupState(state, accounts, []string{"1", "missing"}, "target"); err == nil || state.Assignments["1"] != "old" {
		t.Fatal("missing selected account accepted")
	}
	if _, _, err = movedCloudGroupState(state, accounts[:1], []string{"1"}, "target"); err == nil {
		t.Fatal("inconsistent cloud snapshot accepted")
	}
}

func TestCloudControlSelectionStaysInCurrentScope(t *testing.T) {
	d := &desktop{workspace: "connections", profileSource: "cloud", cloud: &cloudClient{}, accounts: []cloudAccount{{ID: "a", Label: "alpha", Host: "a.test", Username: "alice"}, {ID: "b", Label: "beta", Host: "b.test", Username: "bob"}}}
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.ClickWith(ui.Cmd, "alpha · alice@a.test"); err != nil {
		t.Fatal(err)
	}
	if err := v.ClickWith(ui.Cmd, "beta · bob@b.test"); err != nil {
		t.Fatal(err)
	}
	if len(d.checkedCloud) != 2 {
		t.Fatal("cloud control selection failed")
	}
	d.applyCloudScope("other", nil, nil)
	if len(d.checkedCloud) != 0 || d.cloudSelectionAnchor != "" {
		t.Fatal("selection leaked to a different team")
	}
}

func TestCloudBatchDragRequiresWritePermission(t *testing.T) {
	d := &desktop{cloud: &cloudClient{teamID: "team", cloudGroups: true}, teams: []cloudTeam{{ID: "team", Role: "member"}}, checkedCloud: map[string]cloudAccount{"a": {ID: "a"}, "b": {ID: "b"}}}
	d.moveDraggedCloudProfilesToGroup("a", "target")
	if len(d.checkedCloud) != 2 || d.busy || !strings.Contains(d.status, "不可写") {
		t.Fatal("read-only batch drag changed state or selection")
	}
}
