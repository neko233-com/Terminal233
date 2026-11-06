package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWorkspaceHostSelectionUsesLocalTrustAndScope(t *testing.T) {
	cloud, err := newCloudClient("https://cloud.example")
	if err != nil {
		t.Fatal(err)
	}
	cloud.teamID, cloud.token = "team-a", "token"
	account := cloudAccount{ID: "same-id", Label: "远程", Host: "host.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:cloud-untrusted"}
	key := workspaceCloudSelectionKey(cloud, "owner", account.ID)
	d := &desktop{cloud: cloud, cloudUser: "owner", accounts: []cloudAccount{account}, workspaceHostSelected: map[string]bool{"local": true, key: true}}
	if _, err = d.workspaceFromSelection(); err == nil || !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("cloud fingerprint trusted without confirmation: %v", err)
	}
	d.profiles = []profile{{ID: "1", Label: "trusted", Settings: connectionSettings{Host: account.Host, Port: "22", User: account.Username, KeyPath: "key", Fingerprint: "SHA256:local"}}}
	w, err := d.workspaceFromSelection()
	if err != nil || len(w.Entries) != 2 || w.Entries[1].Fingerprint != "SHA256:local" || w.Entries[1].CloudTeam != "team-a" {
		t.Fatalf("incorrect selected workspace: %+v %v", w, err)
	}
	if _, err = d.resolveWorkspace(w); err != nil {
		t.Fatal(err)
	}
	cloud.teamID = "team-b"
	if _, err = d.workspaceFromSelection(); err == nil {
		t.Fatal("stale selection crossed team scopes")
	}
	cloud.teamID = "team-a"
	d.cloudListStale = true
	if _, err = d.workspaceFromSelection(); err == nil {
		t.Fatal("accepted stale cloud list")
	}
	d.cloudListStale = false
	d.profiles = append(d.profiles, profile{Settings: connectionSettings{Host: account.Host, Port: "22", User: account.Username, Fingerprint: "SHA256:conflict"}})
	if _, err = d.workspaceFromSelection(); err == nil {
		t.Fatal("accepted conflicting locally trusted fingerprints")
	}
}

func TestNewWorkspaceSelectsSavedHosts(t *testing.T) {
	p := profile{ID: "host1", Label: "测试主机", Settings: connectionSettings{Host: "host.test", Port: "22", User: "alice", KeyPath: "key", Fingerprint: "SHA256:trusted"}}
	d := &desktop{workspace: "new-tab", newTabOpen: true, showWorkspaceTemplates: true, profiles: []profile{p}, workspaceTemplateName: "运维"}
	view := ui.NewTester(d.view, 1200, 1000)
	if err := view.Click("从主机创建工作区"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("选择工作区主机：本机终端"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("选择工作区主机：测试主机 · 本机 · host1"); err != nil {
		t.Fatal(err)
	}
	w, err := d.workspaceFromSelection()
	if err != nil || w.Name != "运维" || len(w.Entries) != 2 || w.Entries[1].Label != p.Label {
		t.Fatalf("bad selection: %+v %v", w, err)
	}
	targets, err := d.resolveWorkspace(w)
	if err != nil || len(targets) != 2 || targets[1].label != p.Label {
		t.Fatalf("bad targets: %+v %v", targets, err)
	}
	d.profiles = nil
	if _, err = d.workspaceFromSelection(); err == nil {
		t.Fatal("accepted deleted profile")
	}
}

func TestWorkspaceSelectionSearchKeyboardAndLimit(t *testing.T) {
	d := &desktop{workspace: "connections", profiles: []profile{
		{ID: "one", Label: "开发", Settings: connectionSettings{Host: "dev.test", Port: "22", User: "alice"}},
		{ID: "two", Label: "生产", Settings: connectionSettings{Host: "prod.test", Port: "22", User: "alice"}},
	}}
	d.openNewTab()
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("从主机创建工作区"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("选择工作区主机：本机终端"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("搜索主机或会话"); err != nil {
		t.Fatal(err)
	}
	view.Type("生产")
	view.Key(0, ui.KeyEnter)
	if !d.workspaceHostSelected["local"] || !d.workspaceHostSelected["profile:two"] || d.workspaceHostSelected["profile:one"] {
		t.Fatal("search or Enter lost the selection")
	}
	view.Key(0, ui.KeyEscape)
	if d.newTabQuery != "" || !d.showWorkspaceHostSelection || len(d.workspaceHostSelected) != 2 {
		t.Fatal("clearing search lost the workspace selection")
	}
	view.Key(0, ui.KeyEscape)
	if d.showWorkspaceHostSelection || len(d.workspaceHostSelected) != 0 || !d.newTabOpen {
		t.Fatal("Escape did not cancel selection")
	}
	d.showWorkspaceHostSelection = true
	d.sessions = make([]*workspaceSession, maxWorkspaceSessions-1)
	d.toggleWorkspaceResult(newTabResult{kind: "local"})
	d.toggleWorkspaceResult(newTabResult{kind: "profile", id: "one"})
	if len(d.workspaceHostSelected) != 1 || d.workspaceTemplatesStatus == "" {
		t.Fatal("selection exceeded the session limit")
	}
}

func TestNewTabShowsSavedWorkspacesWithoutOpeningManagement(t *testing.T) {
	w := sampleWorkspace("00000000000000000000000000000001", "运维")
	d := &desktop{workspace: "new-tab", newTabOpen: true, workspaceTemplates: []savedWorkspace{w}}
	view := ui.NewTester(d.view, 1200, 900)
	if _, ok := view.Find("打开工作区：运维"); !ok {
		t.Fatal("saved workspace was hidden")
	}
	if _, ok := view.Find("删除工作区：运维"); ok {
		t.Fatal("delete action was shown outside management")
	}
	if err := view.Click("工作区"); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Find("删除工作区：运维"); !ok {
		t.Fatal("workspace management did not show delete")
	}
}

func TestWorkspaceSelectionControlsFitMinimumWindow(t *testing.T) {
	d := &desktop{workspace: "connections"}
	d.openNewTab()
	view := ui.NewTester(d.view, 640, 480)
	if err := view.Click("从主机创建工作区"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"搜索主机或会话", "新工作区名称", "选择工作区主机：本机终端", "取消", "打开工作区"} {
		r, ok := view.Find(label)
		if !ok || r.X < 0 || r.X+r.W > 640 || r.Y < 0 || r.Y+r.H > 480 {
			t.Fatalf("%s unavailable at minimum size: %+v", label, r)
		}
	}
}
