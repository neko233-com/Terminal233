package main

import (
	"math"
	"net"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestNewTabSearchSwitchAndRestore(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, currentSession: 1,
		sessions: []*workspaceSession{{id: 1, label: "生产终端", term: term}},
		profiles: []profile{{ID: "1", Label: "开发主机", Group: "work", Settings: connectionSettings{Host: "dev.example", User: "alice", Port: "22"}}}}
	view := ui.NewTester(d.view, 1280, 900)
	view.Key(ui.Cmd, ui.KeyT)
	if !d.newTabOpen || d.workspace != "new-tab" || !view.HasText("搜索主机或会话") || !view.HasText("打开本机终端") {
		t.Fatal("new tab did not open with search and local terminal")
	}
	if d.currentSession != 1 || d.term != term {
		t.Fatal("opening the new tab replaced the active session")
	}
	d.newTabQuery = "dev.example"
	view.Frame()
	if !view.HasText("打开主机：开发主机 · 本机") || view.HasText("切换到会话：生产终端 · 会话 1") {
		t.Fatal("new-tab search did not filter hosts and sessions")
	}
	d.newTabQuery = "生产"
	view.Frame()
	if err := view.Click("切换到会话：生产终端 · 会话 1"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "terminal" || !d.newTabOpen || d.currentSession != 1 {
		t.Fatal("selecting the existing session lost the new tab")
	}
	if err := view.Click("切换到新标签页"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "new-tab" || d.newTabQuery != "生产" {
		t.Fatal("returning to the new tab lost search state")
	}
	if err := view.Click("关闭新标签页"); err != nil {
		t.Fatal(err)
	}
	if d.newTabOpen || d.workspace != "terminal" || d.currentSession != 1 || !view.Focused("Terminal") {
		t.Fatal("closing the new tab did not restore and focus the previous session")
	}
}

func TestNewTabProtectsHostDraftAndQuickConnect(t *testing.T) {
	d := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Host: "before", Port: "22", User: "alice"},
		profiles: []profile{{ID: "1", Label: "other", Settings: connectionSettings{Host: "other.test", Port: "22", User: "bob"}}}}
	d.captureConnectionDraft()
	d.settings.Host = "unsaved"
	d.openNewTab()
	d.newTabQuery = "bob@other.test"
	d.newTabQuickConnect()
	if d.workspace != "new-tab" || d.settings.Host != "unsaved" {
		t.Fatal("quick connect discarded an unsaved draft")
	}
	d.newTabQuery = "other"
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("打开主机：other · 本机"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "new-tab" || d.settings.Host != "unsaved" {
		t.Fatal("saved host discarded an unsaved draft")
	}
	if err := view.Click("搜索主机或会话"); err != nil {
		t.Fatal(err)
	}
	view.Key(0, ui.KeyEnter)
	if d.workspace != "new-tab" || d.settings.Host != "unsaved" {
		t.Fatal("keyboard selection bypassed unsaved draft protection")
	}
	d.settings.Host = "before"
	d.newTabQuery = "ssh bob@other.test -p 2222"
	d.newTabQuickConnect()
	if d.workspace != "connections" || d.newTabOpen || d.settings.Host != "other.test" || d.settings.Port != "2222" || d.fingerprint != "" {
		t.Fatal("quick connect did not open a verification-first host form")
	}
}

func TestNewTabFitsMinimumWindow(t *testing.T) {
	d := &desktop{workspace: "connections"}
	d.openNewTab()
	view := ui.NewTester(d.view, 640, 480)
	for _, label := range []string{"搜索主机或会话", "打开本机终端"} {
		r, ok := view.Find(label)
		if !ok || r.Y < 0 || r.Y+r.H > 480 {
			t.Fatalf("%s is not visible at minimum window size: %+v", label, r)
		}
	}
}

func TestNewTabSearchIsCenteredInWorkspace(t *testing.T) {
	for _, tc := range []struct {
		width   int
		sidebar bool
	}{{640, false}, {1280, false}, {1600, false}, {1280, true}} {
		d := &desktop{workspace: "connections"}
		d.openNewTab()
		d.terminalNavigationOpen = tc.sidebar
		view := ui.NewTester(d.view, tc.width, 900)
		r, ok := view.Find("搜索主机或会话")
		left := float32(0)
		if tc.sidebar {
			left = workspaceSidebarWidth(float32(tc.width))
		}
		center := (left + float32(tc.width)) / 2
		if !ok || math.Abs(float64(r.X+r.W/2-center)) > 2 || r.W > 760 {
			t.Errorf("search not centered at width %d sidebar %v: %+v", tc.width, tc.sidebar, r)
		}
	}
}

func TestNewTabListsRecentLocalHostsFirst(t *testing.T) {
	d := &desktop{profiles: []profile{
		{ID: "1", Label: "older", LastConnected: 1},
		{ID: "2", Label: "newer", LastConnected: 10},
		{ID: "3", Label: "never"},
	}}
	results := d.newTabResults("")
	if len(results) != 4 || results[1].id != "2" || results[2].id != "1" || results[3].id != "3" || d.profiles[0].ID != "1" {
		t.Fatalf("recent ordering changed profiles or is incorrect: %#v", results)
	}
}

func TestNewTabKeyboardSearchSelectsSavedHost(t *testing.T) {
	d := &desktop{workspace: "connections", profiles: []profile{
		{ID: "1", Label: "一号", Group: "生产", Settings: connectionSettings{Host: "first.example", Port: "22", User: "bob"}},
		{ID: "2", Label: "二号", Group: "生产", Settings: connectionSettings{Host: "second.example", Port: "22", User: "alice", Fingerprint: "SHA256:saved"}},
	}}
	d.openNewTab()
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("搜索主机或会话"); err != nil {
		t.Fatal(err)
	}
	view.Type("生产 example")
	view.Key(0, ui.KeyDown)
	if d.newTabSelected != "new-tab-profile-2" {
		t.Fatalf("arrow selection = %q", d.newTabSelected)
	}
	view.Key(0, ui.KeyEnter)
	if d.workspace != "connections" || d.settings.Host != "second.example" || d.selectedProfile != "2" || d.expectedFingerprint != "SHA256:saved" {
		t.Fatal("Enter did not open the selected saved host")
	}

	d.editConnection = false
	d.openNewTab()
	view.Frame()
	if err := view.Click("搜索主机或会话"); err != nil {
		t.Fatal(err)
	}
	view.Type("bob@first.example")
	view.Key(0, ui.KeyEnter)
	if d.selectedProfile != "1" || d.settings.Host != "first.example" {
		t.Fatal("exact user@host should select the saved host before creating a new one")
	}
}

func TestNewTabKeyboardQuickConnectAndEscape(t *testing.T) {
	d := &desktop{workspace: "connections"}
	d.openNewTab()
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("搜索主机或会话"); err != nil {
		t.Fatal(err)
	}
	view.Key(0, ui.KeyEnter)
	if d.workspace != "new-tab" {
		t.Fatal("empty search unexpectedly opened a terminal")
	}
	view.Type("ssh alice@example.test -p 2222")
	view.Key(0, ui.KeyEnter)
	if d.workspace != "connections" || d.settings.Host != "example.test" || d.settings.Port != "2222" || d.fingerprint != "" {
		t.Fatal("explicit SSH input did not open the verification-first form")
	}
	d.editConnection = false
	d.openNewTab()
	view.Frame()
	if err := view.Click("搜索主机或会话"); err != nil {
		t.Fatal(err)
	}
	view.Type("no matches")
	view.Key(0, ui.KeyEscape)
	if d.workspace != "new-tab" || d.newTabQuery != "" {
		t.Fatal("first Escape did not clear search")
	}
	view.Key(0, ui.KeyEscape)
	if d.workspace != "connections" || d.newTabOpen {
		t.Fatal("second Escape did not close the new tab")
	}
}

func TestNewTabSearchMatchesEndpointAndCloudWithoutSecrets(t *testing.T) {
	d := &desktop{cloud: &cloudClient{}, accounts: []cloudAccount{{ID: "a", Label: "云主机", Host: "example.test", Port: 2222, Username: "alice", Password: "not-searchable-secret"}}}
	results := d.newTabResults("alice@example.test:2222")
	if len(results) < 1 || results[0].key != "new-tab-cloud-a" || results[0].detail != "alice@example.test:2222 · 云端" {
		t.Fatalf("endpoint search failed: %#v", results)
	}
	if got := d.newTabResults("not-searchable-secret"); len(got) != 0 {
		t.Fatal("credential text leaked into search")
	}
}
