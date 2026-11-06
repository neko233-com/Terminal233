package main

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestKnownHostsRevocationCoversProfilesSessionsAndForwards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	key := knownHostKey{Host: "host.example", Port: "22", Fingerprint: "SHA256:trusted"}
	d.profiles = []profile{
		{ID: "1", StableID: strings.Repeat("a", 32), Label: "one", Settings: connectionSettings{Host: key.Host, Port: key.Port, User: "alice", Fingerprint: key.Fingerprint}},
		{ID: "2", Label: "two", Settings: connectionSettings{Host: key.Host, Port: key.Port, User: "bob", Fingerprint: key.Fingerprint}},
		{ID: "3", Label: "other", Settings: connectionSettings{Host: key.Host, Port: "2222", User: "alice", Fingerprint: "SHA256:other"}},
	}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.savedForwardRuns = map[string]*savedForwardRun{"rule": {cancel: cancel, hostID: d.profiles[0].StableID, binding: forwardHostBinding(d.profiles[0])}}
	d.sessions = []*workspaceSession{{id: 1, shell: &remoteShell{}, active: d.profiles[0].Settings}, {id: 2, shell: &remoteShell{}, active: d.profiles[1].Settings}}
	d.currentSession, d.active = 1, d.sessions[0].active
	entries := d.knownHosts()
	if len(entries) != 2 || entries[0].Profiles != 2 || entries[0].Sessions != 2 {
		t.Fatalf("incorrect known-host aggregation: %+v", entries)
	}
	if err := d.forgetKnownHost(key); err != nil {
		t.Fatal(err)
	}
	if d.profiles[0].Settings.Fingerprint != "" || d.profiles[1].Settings.Fingerprint != "" || d.profiles[2].Settings.Fingerprint != "SHA256:other" {
		t.Fatal("incorrect saved trust removal")
	}
	if d.active.Fingerprint != "" || d.sessions[0].active.Fingerprint != "" || d.sessions[1].active.Fingerprint != "" || ctx.Err() == nil {
		t.Fatal("session trust or dependent forwarding was retained")
	}
	if _, err := d.trustedWorkspaceCloudFingerprint(cloudAccount{Host: key.Host, Port: 22, Username: "alice"}); err == nil {
		t.Fatal("revoked session still establishes cloud trust")
	}
	var reloaded desktop
	if err := reloaded.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if reloaded.profiles[0].Settings.Fingerprint != "" || reloaded.profiles[2].Settings.Fingerprint != "SHA256:other" {
		t.Fatal("revocation not durable")
	}
	if err := d.forgetKnownHost(key); err == nil {
		t.Fatal("accepted already forgotten key")
	}
}

func TestKnownHostRevocationRejectsStaleState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	a, b := &desktop{}, &desktop{}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.profiles = []profile{{ID: "1", Label: "host", Settings: connectionSettings{Host: "host.example", Port: "22", User: "alice", Fingerprint: "SHA256:trusted"}}}
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	b.sessions = []*workspaceSession{{shell: &remoteShell{}, active: b.profiles[0].Settings}}
	b.active = b.sessions[0].active
	a.profiles[0].Label = "changed elsewhere"
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	if err := b.forgetKnownHost(knownHostKeyFor(b.active)); err == nil || !b.profileStoreConflict {
		t.Fatal("stale revocation overwrote newer file")
	}
	if b.active.Fingerprint == "" || b.sessions[0].active.Fingerprint == "" || b.profiles[0].Settings.Fingerprint == "" {
		t.Fatal("failed revocation changed in-memory trust")
	}
}

func TestKnownHostsNavigationAndConfirmation(t *testing.T) {
	d := &desktop{workspace: "connections", profiles: []profile{{ID: "1", Settings: connectionSettings{Host: "host.example", Port: "22", User: "alice", Fingerprint: "SHA256:trusted"}}}}
	view := ui.NewTester(d.view, 1200, 760)
	if err := view.Click("已知主机"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "known-hosts" || !view.HasText("host.example:22") || !view.HasText("SHA256:trusted") {
		t.Fatal("known host missing from workspace")
	}
	if err := view.Click("撤销信任：host.example:22"); err != nil {
		t.Fatal(err)
	}
	if d.profiles[0].Settings.Fingerprint == "" || !view.HasText("确认撤销：host.example:22") {
		t.Fatal("confirmation did not preserve trust")
	}
	if err := view.Click("取消撤销：host.example:22"); err != nil {
		t.Fatal(err)
	}
	if d.profiles[0].Settings.Fingerprint == "" || view.HasText("确认撤销：host.example:22") {
		t.Fatal("cancel did not preserve trust")
	}
	if err := view.Click("撤销信任：host.example:22"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("确认撤销：host.example:22"); err != nil {
		t.Fatal(err)
	}
	if d.profiles[0].Settings.Fingerprint != "" || !view.HasText("下次连接需要重新核对指纹") {
		t.Fatal("confirmed trust removal failed")
	}
}

func TestRevokedSessionCannotReconnectWithoutNewTrust(t *testing.T) {
	left, right := net.Pipe()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	_ = right.Close()
	select {
	case <-term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not exit")
	}
	d := &desktop{term: term, shell: &remoteShell{}, active: connectionSettings{Host: "host.example", Port: "22", User: "alice"}}
	d.reconnectCurrentSession()
	if d.busy || !strings.Contains(d.status, "重新连接并核对指纹") {
		t.Fatal("revoked session did not require fresh trust")
	}
}
