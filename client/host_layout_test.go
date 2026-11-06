package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func layoutTestProfiles() []profile {
	return []profile{
		{ID: "1", Label: "alpha", Settings: connectionSettings{Host: "z.test", Port: "22", User: "alice"}},
		{ID: "2", Label: "beta", Settings: connectionSettings{Host: "a.test", Port: "2222", User: "bob"}},
		{ID: "3", Label: "gamma", Settings: connectionSettings{Host: "c.test", Port: "22", User: "carol"}},
	}
}

func TestHostLayoutSwitchPersistsAndPreservesSelection(t *testing.T) {
	prefs := filepath.Join(t.TempDir(), "preferences.json")
	d := &desktop{workspace: "connections", profiles: layoutTestProfiles(), selectedProfile: "1", checkedProfiles: map[profileID]bool{"2": true}}
	if err := d.loadPreferences(prefs); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1400, 900)
	a, _ := view.Find("alpha")
	b, _ := view.Find("beta")
	if a.Y != b.Y || b.X <= a.X {
		t.Fatal("initial host layout is not a grid")
	}
	if err := view.Click("切换为列表视图"); err != nil {
		t.Fatal(err)
	}
	a, okA := view.Find("alpha")
	b, okB := view.Find("beta")
	if !okA || !okB || a.X != b.X || b.Y <= a.Y || a.W < 700 {
		t.Fatalf("hosts did not form full-width rows: %+v %+v", a, b)
	}
	if d.selectedProfile != "1" || !d.checkedProfiles["2"] {
		t.Fatal("switching layout lost selection")
	}
	if err := writePreference(prefs, "light", true); err != nil {
		t.Fatal(err)
	}
	var restored desktop
	if err := restored.loadPreferences(prefs); err != nil || !restored.hostListView || !restored.lightTheme {
		t.Fatal("layout preference did not merge or persist", err)
	}
	if err := view.Click("切换为网格视图"); err != nil {
		t.Fatal(err)
	}
	a, _ = view.Find("alpha")
	b, _ = view.Find("beta")
	if a.Y != b.Y || b.X <= a.X {
		t.Fatal("grid layout did not return")
	}
	p, err := readPreferences(prefs)
	if err != nil || p.HostListView {
		t.Fatal("grid preference was not saved", err)
	}
}

func TestHostListSortColumnsAndNarrowLayouts(t *testing.T) {
	d := &desktop{workspace: "connections", hostListView: true, profiles: layoutTestProfiles()}
	view := ui.NewTester(d.view, 1400, 900)
	if err := view.Click("本机按地址排序"); err != nil {
		t.Fatal(err)
	}
	a, _ := view.Find("alpha")
	b, _ := view.Find("beta")
	if d.profileSort != "host" || d.profileSortDescending || b.Y >= a.Y {
		t.Fatal("address sort was not applied")
	}
	if err := view.Click("本机按地址排序"); err != nil {
		t.Fatal(err)
	}
	a, _ = view.Find("alpha")
	b, _ = view.Find("beta")
	if !d.profileSortDescending || a.Y >= b.Y {
		t.Fatal("descending address sort was not applied")
	}
	for _, size := range [][2]int{{640, 480}, {1000, 700}, {1400, 900}} {
		for _, light := range []bool{false, true} {
			d := &desktop{workspace: "connections", hostListView: true, lightTheme: light, railExpanded: true, profiles: layoutTestProfiles()}
			v := ui.NewTester(d.view, size[0], size[1])
			toggle, ok := v.Find("切换为网格视图")
			if !ok || toggle.X < 0 || toggle.X+toggle.W > float32(size[0]) || toggle.Y+toggle.H > float32(size[1]) {
				t.Fatalf("layout toggle unavailable at %v: %+v", size, toggle)
			}
			label := "alpha"
			if size[0] < 900 {
				label = "alpha · z.test:22"
			}
			r, ok := v.Find(label)
			if !ok || r.X < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
				t.Fatalf("list row unavailable at %v: %+v", size, r)
			}
		}
	}
}

func TestHostListReusesGroupDragAndModifiedSelection(t *testing.T) {
	d := &desktop{workspace: "connections", hostListView: true, profiles: layoutTestProfiles(), profileGroups: []string{"target"}}
	if err := d.loadProfiles(filepath.Join(t.TempDir(), "profiles.json")); err != nil {
		t.Fatal(err)
	}
	d.profiles, d.profileGroups = layoutTestProfiles(), []string{"target"}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	v := ui.NewTester(d.view, 1400, 1000)
	if err := v.ClickWith(ui.Cmd, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := v.ClickWith(ui.Shift, "gamma"); err != nil {
		t.Fatal(err)
	}
	if len(d.checkedProfiles) != 3 {
		t.Fatal("list range selection failed")
	}
	from, ok := v.Find("alpha")
	if !ok {
		t.Fatal("drag source missing")
	}
	to, ok := v.Find("打开分组：target")
	if !ok {
		t.Fatal("group row missing")
	}
	v.Press(from.X+from.W/2, from.Y+from.H/2)
	v.Move(to.X+to.W/2, to.Y+to.H/2)
	v.Release(to.X+to.W/2, to.Y+to.H/2)
	for _, p := range d.profiles {
		if p.Group != "target" {
			t.Fatalf("list drag did not move the selected hosts: %s", d.status)
		}
	}
}

func TestCloudHostListSelectionAndDraftProtection(t *testing.T) {
	d := &desktop{workspace: "connections", hostListView: true, profileSource: "cloud", cloud: &cloudClient{}, accounts: []cloudAccount{{ID: "a", Label: "alpha", Host: "a.test", Port: 22, Username: "alice"}, {ID: "b", Label: "beta", Host: "b.test", Port: 22, Username: "bob"}}}
	v := ui.NewTester(d.view, 1400, 900)
	a, okA := v.Find("alpha · alice@a.test")
	b, okB := v.Find("beta · bob@b.test")
	if !okA || !okB || a.X != b.X || b.Y <= a.Y {
		t.Fatal("cloud hosts did not use list rows")
	}
	if err := v.ClickWith(ui.Cmd, "alpha · alice@a.test"); err != nil {
		t.Fatal(err)
	}
	if err := v.ClickWith(ui.Cmd, "beta · bob@b.test"); err != nil {
		t.Fatal(err)
	}
	if len(d.checkedCloud) != 2 {
		t.Fatal("cloud list selection failed")
	}
	d = &desktop{workspace: "connections", profiles: layoutTestProfiles()}
	d.loadProfile(d.profiles[0])
	d.settings.Password = "unsaved-test-secret"
	v = ui.NewTester(d.view, 1400, 900)
	if err := v.Click("切换为列表视图"); err != nil {
		t.Fatal(err)
	}
	if !d.editConnection || d.settings.Password != "unsaved-test-secret" || !d.hasUnsavedConnection() {
		t.Fatal("layout change modified the host draft")
	}
	if err := v.Click("beta"); err != nil {
		t.Fatal(err)
	}
	v.Key(0, ui.KeyEnter)
	if d.settings.Host != "z.test" || d.settings.Password != "unsaved-test-secret" {
		t.Fatal("list keyboard navigation discarded an unfinished draft")
	}
}

func TestHostLayoutsEnterAndContextMenuUseUntrustedConnectionFlow(t *testing.T) {
	for _, list := range []bool{false, true} {
		d := &desktop{workspace: "connections", hostListView: list, profiles: layoutTestProfiles()}
		v := ui.NewTester(d.view, 1400, 900)
		if err := v.Click("alpha"); err != nil {
			t.Fatal(err)
		}
		if d.editConnection {
			t.Fatal("single click should only select a host")
		}
		v.Key(0, ui.KeyEnter)
		if !d.editConnection || d.settings.Host != "z.test" || d.fingerprint != "" || d.busy {
			t.Fatal("Enter did not open the untrusted form")
		}
		if err := v.RightClick("beta"); err != nil {
			t.Fatal(err)
		}
		if err := v.ChooseMenuItem("连接"); err != nil {
			t.Fatal(err)
		}
		if !d.editConnection || d.settings.Host != "a.test" || d.fingerprint != "" || d.busy {
			t.Fatal("context connection bypassed the untrusted form")
		}
	}
}
