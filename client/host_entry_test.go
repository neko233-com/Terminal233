package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestHostEntryParsesAddressesWithoutCredentialsOrCommands(t *testing.T) {
	for _, tc := range []struct{ input, host, user, port string }{
		{"api.example.test", "api.example.test", "", "22"},
		{"192.0.2.1", "192.0.2.1", "", "22"},
		{"[2001:db8::1]", "2001:db8::1", "", "22"},
		{"2001:db8::1", "2001:db8::1", "", "22"},
		{"ssh alice@example.test -p 2222", "example.test", "alice", "2222"},
		{" alice@example.test ", "example.test", "alice", "22"},
	} {
		got, err := parseHostEntry(tc.input)
		if err != nil || got.Host != tc.host || got.User != tc.user || got.Port != tc.port || got.Password != "" || got.KeyPath != "" || got.Fingerprint != "" {
			t.Fatalf("unexpected address parse for %q: %v", tc.input, err)
		}
	}
	for _, input := range []string{"", "-", "-bad.test", "bad-.test", "bad..test", "bad host", "https://example.test", "alice:secret@example.test", "example.test;whoami", "$(whoami)", "example.test\r\n", "[bad]", "a\x00b", "a\u202eb", strings.Repeat("a", 513)} {
		got, err := parseHostEntry(input)
		if err == nil || got != (connectionSettings{}) || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe or invalid address accepted")
		}
	}
}

func TestHostEntryFitsAndContinuesToUntrustedEditor(t *testing.T) {
	for _, size := range [][2]int{{640, 480}, {720, 760}, {1200, 760}, {1600, 900}} {
		for _, light := range []bool{false, true} {
			d := &desktop{workspace: "connections", railExpanded: true, lightTheme: light, settings: connectionSettings{Host: "old", User: "old", Password: "old-test-secret", KeyPath: "old-key", Fingerprint: "SHA256:old"}, fingerprint: "SHA256:old", selectedProfile: "old"}
			view := ui.NewTester(d.view, size[0], size[1])
			for _, label := range []string{"新主机 IP 或主机名", "继续填写主机信息"} {
				bounds, ok := view.Find(label)
				if !ok || bounds.X < 0 || bounds.Y < 0 || bounds.X+bounds.W > float32(size[0]) || bounds.Y+bounds.H > float32(size[1]) {
					t.Fatalf("%s unavailable at %v: %+v", label, size, bounds)
				}
			}
			if err := view.Click("新主机 IP 或主机名"); err != nil {
				t.Fatal(err)
			}
			view.Type("example.test")
			view.Key(0, ui.KeyEnter)
			if !d.editConnection || d.settings.Host != "example.test" || d.settings.Port != "22" || d.settings.User != "" || d.settings.Password != "" || d.settings.KeyPath != "" || d.settings.Fingerprint != "" || d.fingerprint != "" || d.selectedProfile != "" {
				t.Fatal("host entry reused credentials or did not open the editor")
			}
			if !view.Focused("用户名") || !d.hasUnsavedConnection() {
				t.Fatal("host entry did not focus authentication or retain its draft")
			}
			view.Type("alice")
			if d.settings.User != "alice" || d.settings.Host != "example.test" {
				t.Fatal("typing did not enter the authentication field")
			}
		}
	}
}

func TestHostEntryDoesNotHideExistingOrFilteredContent(t *testing.T) {
	for _, d := range []*desktop{
		{profiles: []profile{{ID: "1"}}}, {profileGroups: []string{"group"}}, {profileSearch: "search"},
		{profileFavorites: true}, {profileRecent: true}, {profileFilter: "group"}, {cloud: &cloudClient{}},
		{showCompactProfileTools: true}, {showGroupControls: true}, {profileSource: "cloud"}, {editConnection: true},
	} {
		if d.hostEntryVisible() {
			t.Fatal("onboarding hid existing content")
		}
	}
}

func TestHostEntryPreservesIncompleteDraftAndRejectsInvalidInput(t *testing.T) {
	d := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Host: "draft.test", Port: "22"}}
	d.captureConnectionDraft()
	d.settings.Password = "temporary-test-secret"
	d.newHostEntry = "other.test"
	d.continueHostEntry()
	if d.settings.Host != "draft.test" || d.settings.Password != "temporary-test-secret" {
		t.Fatal("unfinished draft was discarded")
	}
	d = &desktop{workspace: "connections"}
	view := ui.NewTester(d.view, 1200, 760)
	if err := view.Click("新主机 IP 或主机名"); err != nil {
		t.Fatal(err)
	}
	view.Type("bad host")
	view.Key(0, ui.KeyEnter)
	if d.editConnection || !view.HasText("主机地址输入错误") {
		t.Fatal("invalid address did not keep an inline error")
	}
	view.Key(ui.Cmd, ui.KeyA)
	view.Type("ssh alice@example.test -p 2222")
	view.Key(0, ui.KeyEnter)
	if !d.editConnection || d.settings.User != "alice" || d.settings.Port != "2222" || !view.Focused("SSH 密码") {
		t.Fatal("SSH address did not focus its credential field")
	}
}
