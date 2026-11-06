package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSavedForwardRulesPersistAndRejectStaleEdits(t *testing.T) {
	dir := t.TempDir()
	d := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
	if err := d.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	d.settings = connectionSettings{Host: "host.example", Port: "22", User: "alice"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	hostID, err := d.ensureForwardHostID(d.profiles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := newRandomID()
	if err != nil {
		t.Fatal(err)
	}
	rule := savedForwardRule{ID: id, Name: "数据库", HostID: hostID, Mode: "local", ListenPort: "0", TargetHost: "127.0.0.1", TargetPort: "5432"}
	if err := d.saveForwardRule(rule, nil); err != nil {
		t.Fatal(err)
	}
	var disk diskProfiles
	data, err := os.ReadFile(d.profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &disk) != nil || disk.Version != 6 || disk.Items[0].StableID != hostID {
		t.Fatal("stable host ID did not persist")
	}
	var reopened desktop
	if err := reopened.loadProfiles(d.profilePath); err != nil {
		t.Fatal(err)
	}
	if p, ok := reopened.forwardRuleHost(hostID); !ok || p.Settings.Host != "host.example" {
		t.Fatal("rule host did not survive restart")
	}
	reopened.profiles[0].Label = "renamed"
	if err := reopened.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	if p, ok := reopened.forwardRuleHost(hostID); !ok || p.Label != "renamed" {
		t.Fatal("host edit broke rule association")
	}
	old := rule
	changed := rule
	changed.Name = "更新"
	if err := d.saveForwardRule(changed, &old); err != nil {
		t.Fatal(err)
	}
	stale := old
	stale.Name = "过期"
	if err := d.saveForwardRule(stale, &old); err == nil {
		t.Fatal("stale editor overwrote newer rule")
	}
	if err := d.deleteForwardRule(old); err == nil {
		t.Fatal("stale delete removed newer rule")
	}
	file, err := readForwardRules(d.forwardRulesPath)
	if err != nil || len(file.Items) != 1 || file.Items[0].Name != "更新" {
		t.Fatal("newer rule lost", err)
	}
}

func TestSavedForwardIndependentSSHAndHostKey(t *testing.T) {
	target := echoFixture(t)
	_, port, _ := net.SplitHostPort(target.Addr().String())
	settings, fingerprint, attempts := testSSHServer(t)
	rule := savedForwardRule{ID: strings.Repeat("a", 32), Name: "fixture", HostID: strings.Repeat("b", 32), Mode: "local", ListenPort: "0", TargetHost: "127.0.0.1", TargetPort: port}
	settings.Fingerprint = "SHA256:not-the-server"
	if _, _, err := openSavedForward(context.Background(), rule, settings, nil); err == nil || attempts.Load() != 0 {
		t.Fatal("wrong SSH host key was accepted or credentials sent")
	}
	settings.Fingerprint = fingerprint
	client, f, err := openSavedForward(context.Background(), rule, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer f.Close()
	conn, err := net.DialTimeout("tcp", f.address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write([]byte("hello"))
	reply := make([]byte, 5)
	if _, err = io.ReadFull(conn, reply); err != nil || string(reply) != "hello" {
		t.Fatalf("relay %q: %v", reply, err)
	}
	f.Close()
	if _, err = conn.Read(reply); err == nil {
		t.Fatal("active connection survived rule stop")
	}
}

func TestSSHHandshakeCancellation(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = connectSSHClient(ctx, connectionSettings{Host: host, Port: port, User: "test", Password: "fixture", Fingerprint: "SHA256:fixture"})
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("handshake cancellation failed: %v", err)
	}
	if c := <-accepted; c != nil {
		c.Close()
	}
}

func TestSavedForwardStopsWhenHostChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := profile{StableID: strings.Repeat("a", 32), Settings: connectionSettings{Host: "host.example", Port: "22", User: "alice", Fingerprint: "SHA256:test"}}
	d := &desktop{profiles: []profile{p}, savedForwardRuns: map[string]*savedForwardRun{"rule": {cancel: cancel, hostID: p.StableID, binding: forwardHostBinding(p)}}}
	d.stopChangedForwardRules()
	if ctx.Err() != nil {
		t.Fatal("unchanged host stopped")
	}
	d.profiles[0].Settings.Host = "new.example"
	d.stopChangedForwardRules()
	if ctx.Err() == nil || len(d.savedForwardRuns) != 0 {
		t.Fatal("changed host did not stop rule")
	}
}
