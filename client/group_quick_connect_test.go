package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestGroupQuickTargetsUseSubgroupsAndLocalTrust(t *testing.T) {
	cloud, _ := newCloudClient("https://sync.example")
	cloud.token = "token"
	cloud.cloudGroups = true
	cloud.teamID = "team"
	d := &desktop{cloud: cloud, cloudUser: "owner", profiles: []profile{
		{ID: "1", Label: "parent", Group: "work", Settings: connectionSettings{Host: "one.test", Port: "22", User: "alice", Password: "test", Fingerprint: "SHA256:one"}},
		{ID: "2", Label: "child", Group: "work/prod", Settings: connectionSettings{Host: "two.test", Port: "22", User: "bob", KeyPath: "key", Fingerprint: "SHA256:two"}},
		{ID: "3", Label: "untrusted", Group: "work/prod", Settings: connectionSettings{Host: "three.test", Port: "22", User: "eve", Password: "test"}},
		{ID: "4", Label: "outside", Group: "worker", Settings: connectionSettings{Host: "four.test", Port: "22", User: "alice", Password: "test", Fingerprint: "SHA256:four"}},
	}}
	local, failures, err := d.groupQuickTargets("work", false)
	if err != nil || len(local) != 2 || len(failures) != 1 || !strings.Contains(failures[0], "指纹") || local[1].sourceProfileID != "2" {
		t.Fatal("incorrect local group preflight", err, failures)
	}
	d.accounts = []cloudAccount{{ID: "a", Label: "trusted cloud", Host: "one.test", Port: 22, Username: "alice", Password: "test", HostKeyFingerprint: "SHA256:untrusted-remote"}, {ID: "b", Label: "unknown cloud", Host: "unknown.test", Port: 22, Username: "eve", Password: "test", HostKeyFingerprint: "SHA256:remote"}}
	d.cloudGroupSnapshot = cloudGroupSnapshot{Groups: []string{"work", "work/prod"}, Assignments: map[string]string{"a": "work/prod", "b": "work"}, AccountIDs: []string{"a", "b"}, Revision: strings.Repeat("a", 64)}
	remote, failures, err := d.groupQuickTargets("work", true)
	if err != nil || len(remote) != 1 || len(failures) != 1 || remote[0].settings.Fingerprint != "SHA256:one" || remote[0].entry.CloudTeam != "team" {
		t.Fatal("cloud fingerprint or scope was not checked", err, failures)
	}
	if groupCloudSnapshotCurrent(d.cloudGroupSnapshot.Revision, d.cloudGroupSnapshot, d.accounts) != nil {
		t.Fatal("unchanged cloud snapshot rejected")
	}
	changed := d.cloudGroupSnapshot
	changed.Revision = strings.Repeat("b", 64)
	if groupCloudSnapshotCurrent(d.cloudGroupSnapshot.Revision, changed, d.accounts) == nil {
		t.Fatal("changed group snapshot accepted")
	}
	d.cloudGroupsStale = true
	if _, _, err = d.groupQuickTargets("work", true); err == nil {
		t.Fatal("stale group accepted")
	}
	d.cloudGroupsStale = false
	d.sessions = make([]*workspaceSession, maxWorkspaceSessions-1)
	for i := range d.sessions {
		d.sessions[i] = &workspaceSession{}
	}
	if _, _, err = d.groupQuickTargets("work", true); err == nil {
		t.Fatal("session limit ignored")
	}
}

func TestGroupQuickConnectIndependentFailuresAndCancellation(t *testing.T) {
	var mu sync.Mutex
	var peers []net.Conn
	var closed atomic.Int32
	open := func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
		if target.label == "bad" {
			return workspaceRestoredSession{target: target}, errors.New("fixture failure")
		}
		left, right := net.Pipe()
		term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
		mu.Lock()
		peers = append(peers, right)
		mu.Unlock()
		return workspaceRestoredSession{target: target, term: term}, err
	}
	closeItem := func(item workspaceRestoredSession) {
		if item.term != nil {
			_ = item.term.Close()
			closed.Add(1)
		}
	}
	defer func() {
		for _, peer := range peers {
			_ = peer.Close()
		}
	}()
	targets := []workspaceRestoreTarget{{label: "one"}, {label: "bad"}, {label: "two"}}
	results, err := connectGroupTargets(context.Background(), targets, open, closeItem, nil)
	if err != nil || len(results) != 3 || results[0].err != nil || results[1].err == nil || results[2].err != nil {
		t.Fatal("one failed host aborted the others", err)
	}
	for _, result := range results {
		if result.err == nil {
			closeItem(result.session)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := connectGroupTargets(ctx, []workspaceRestoreTarget{{label: "late"}}, func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
			close(started)
			<-release
			return open(target)
		}, closeItem, nil)
		done <- err
	}()
	<-started
	before := closed.Load()
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) || closed.Load() != before+1 {
		t.Fatal("cancelled connection leaked its terminal", err)
	}
}

func TestGroupQuickConnectVerifiesEachSSHHost(t *testing.T) {
	settings, fingerprint, attempts := testSSHServer(t)
	settings.Fingerprint = fingerprint
	bad := settings
	bad.Fingerprint = "SHA256:wrong"
	targets := []workspaceRestoreTarget{{label: "one", settings: settings}, {label: "untrusted", settings: bad}, {label: "two", settings: settings}}
	results, err := connectGroupTargets(context.Background(), targets, func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
		shell, term, err := openSSHSession(target.settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1})
		return workspaceRestoredSession{target: target, shell: shell, term: term}, err
	}, func(item workspaceRestoredSession) {
		if item.term != nil {
			_ = item.term.Close()
		}
	}, nil)
	if err != nil || len(results) != 3 || results[0].err != nil || results[1].err == nil || results[2].err != nil {
		t.Fatal("independent SSH connections failed", err)
	}
	defer func() {
		for _, result := range results {
			if result.err == nil {
				_ = result.session.term.Close()
			}
		}
	}()
	if attempts.Load() != 2 {
		t.Fatal("credentials were sent before host key verification")
	}
}

func TestGroupQuickConnectIsAvailableFromGroup(t *testing.T) {
	d := &desktop{workspace: "connections", profileGroups: []string{"work"}, profiles: []profile{{ID: "1", Label: "host", Group: "work", Settings: connectionSettings{Host: "host.test", Port: "22", User: "alice"}}}}
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.RightClick("打开分组：work"); err != nil {
		t.Fatal(err)
	}
	if err := v.ChooseMenuItem("快速连接"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("打开分组：work"); err != nil {
		t.Fatal(err)
	}
	if !v.HasText("快速连接当前分组") {
		t.Fatal("keyboard-accessible group action missing")
	}
}
